package report

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jobs-build/assimilate/spec"

	"github.com/jobs-build/jobs-action/checks"
)

// API is the part of the Checks API the reporter uses; *checks.Client
// implements it.
type API interface {
	Create(ctx context.Context, r checks.Run) (int64, error)
	Update(ctx context.Context, id int64, r checks.Run) error
}

// Tunables, as variables so tests can shrink them.
var (
	// createPool, completePool and cancelPool bound concurrent API requests.
	// GitHub's secondary limit is about 100 concurrent requests; cancelPool is
	// the largest because everything it sends must land within seconds.
	createPool   = 8
	completePool = 8
	cancelPool   = 16
	// retryWaits are the waits before a completing request's second, third
	// and fourth attempts. A check left "in progress" is the worst outcome, so
	// completion is the one request worth retrying.
	retryWaits = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
	// maxRetryAfter caps how long a rate limit's Retry-After is honoured.
	// GitHub's secondary limits usually ask for a minute. The caller bounds
	// the total wait.
	maxRetryAfter = 60 * time.Second
	// createLinger is how long a create already in flight may run on after
	// the run is cancelled.
	createLinger = 2 * time.Second
	// requestTimeout bounds one create or completing attempt.
	requestTimeout = 15 * time.Second
	// cancelTimeout bounds the single attempt CancelOpen gives each check: the
	// process is about to be killed.
	cancelTimeout = 4 * time.Second
)

// Checks reports each test as one check run: created in progress before the
// run, completed when the test ends. Two writes per test.
//
// It never returns an error. Reporting is a courtesy next to the verdict, so
// every failure becomes a warning.
type Checks struct {
	api     API
	headSHA string
	tests   []Test
	warn    func(string)

	mu     sync.Mutex
	ids    []int64 // 0 = no check run exists for the test
	closed []bool  // a completion was claimed; the first one wins
	off    bool    // no further check runs are created
	wg     sync.WaitGroup
	sem    chan struct{}
}

// NewChecks reports tests to api as check runs on headSHA. A nil api or an
// empty headSHA turns reporting off: every method is then a no-op. warn
// receives one-line warnings and must be safe for concurrent use.
func NewChecks(api API, headSHA string, tests []Test, warn func(string)) *Checks {
	c := &Checks{
		api:     api,
		headSHA: headSHA,
		tests:   tests,
		warn:    warn,
		ids:     make([]int64, len(tests)),
		closed:  make([]bool, len(tests)),
		sem:     make(chan struct{}, completePool),
	}
	c.off = api == nil || headSHA == ""
	return c
}

// Open creates every check run, in progress, and returns when all requests
// have finished. If the token may not write check runs, or a rate limit is
// hit, it warns once and creates no more; check runs that already exist are
// still completed later.
func (c *Checks) Open(ctx context.Context) {
	if c.disabled() {
		return
	}
	work := make(chan int)
	var wg sync.WaitGroup
	for range min(createPool, len(c.tests)) {
		wg.Go(func() {
			for i := range work {
				c.create(ctx, i)
			}
		})
	}
	for i := range c.tests {
		work <- i
	}
	close(work)
	wg.Wait()
}

func (c *Checks) disabled() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.off
}

func (c *Checks) create(ctx context.Context, i int) {
	if c.disabled() || ctx.Err() != nil {
		return
	}
	t := c.tests[i]
	// The request is not tied to ctx. A create aborted mid-flight may already
	// have been accepted, and then nobody knows the id of a check that stays
	// in progress. After a cancellation it gets createLinger to finish, so
	// that CancelOpen can close it.
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), requestTimeout)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { time.AfterFunc(createLinger, cancel) })
	defer stop()
	id, err := c.api.Create(rctx, checks.Run{
		Name:    t.CheckName(),
		HeadSHA: c.headSHA,
		Status:  "in_progress",
		Output:  checks.Output{Title: "running", Summary: summaryLine(t, Outcome{})},
	})
	if err == nil {
		c.mu.Lock()
		c.ids[i] = id
		c.mu.Unlock()
		return
	}
	if ctx.Err() != nil {
		return // the run was cancelled; nothing to say about one check
	}
	var apiErr *checks.Error
	if errors.As(err, &apiErr) && (apiErr.Permission() || apiErr.RateLimited) {
		c.mu.Lock()
		first := !c.off
		c.off = true
		c.mu.Unlock()
		if !first {
			return
		}
		if apiErr.RateLimited {
			c.warn("GitHub rate limit reached while creating check runs; reporting through the job summary only")
		} else {
			c.warn(fmt.Sprintf(`cannot create check runs (%v): the token needs "checks: write" (a personal access token cannot write checks); reporting through the job summary only`, err))
		}
		return
	}
	c.warn(fmt.Sprintf("check run %q not created: %v", t.CheckName(), err))
}

// Complete finishes test i's check run with outcome o, in the background.
// Only the first completion of a test counts; later ones are ignored, so a
// terminal event that arrives after CancelOpen changes nothing.
func (c *Checks) Complete(i int, o Outcome) {
	id, ok := c.claim(i)
	if !ok {
		return
	}
	c.wg.Go(func() {
		c.sem <- struct{}{}
		defer func() { <-c.sem }()
		c.complete(i, id, o)
	})
}

// claim marks test i completed and returns its check run id; ok is false
// when it was already claimed or has no check run.
func (c *Checks) claim(i int) (id int64, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if i < 0 || i >= len(c.closed) || c.closed[i] {
		return 0, false
	}
	c.closed[i] = true
	return c.ids[i], c.ids[i] != 0
}

func (c *Checks) complete(i int, id int64, o Outcome) {
	run := completedRun(c.tests[i], o)
	var err error
	for attempt := 0; ; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		err = c.api.Update(ctx, id, run)
		cancel()
		if err == nil {
			return
		}
		// Every failure is retried, a bare 403 included: the same token
		// created this check, so a 403 here is far more likely a secondary
		// rate limit than a refusal.
		if attempt >= len(retryWaits) {
			break
		}
		wait := retryWaits[attempt]
		var apiErr *checks.Error
		if errors.As(err, &apiErr) && apiErr.RetryAfter > 0 {
			wait = min(apiErr.RetryAfter, maxRetryAfter)
		}
		time.Sleep(wait)
	}
	c.warn(fmt.Sprintf("check run %q could not be completed and may stay in progress: %v", c.tests[i].CheckName(), err))
}

// CancelOpen completes every check run that is still open as cancelled and
// returns when the requests have finished or timed out. Each gets one
// attempt: it runs when the process has seconds left.
func (c *Checks) CancelOpen() {
	type open struct {
		i  int
		id int64
	}
	var todo []open
	c.mu.Lock()
	for i := range c.closed {
		if c.closed[i] {
			continue
		}
		c.closed[i] = true
		if c.ids[i] != 0 {
			todo = append(todo, open{i, c.ids[i]})
		}
	}
	c.mu.Unlock()
	if len(todo) == 0 {
		return
	}
	work := make(chan open)
	var wg sync.WaitGroup
	for range min(cancelPool, len(todo)) {
		wg.Go(func() {
			for o := range work {
				ctx, cancel := context.WithTimeout(context.Background(), cancelTimeout)
				err := c.api.Update(ctx, o.id, completedRun(c.tests[o.i], Outcome{State: spec.StateCancelled}))
				cancel()
				if err != nil {
					c.warn(fmt.Sprintf("check run %q could not be marked cancelled and may stay in progress: %v", c.tests[o.i].CheckName(), err))
				}
			}
		})
	}
	for _, o := range todo {
		work <- o
	}
	close(work)
	wg.Wait()
}

// Wait returns when every completion started by Complete has finished.
func (c *Checks) Wait() { c.wg.Wait() }

func completedRun(t Test, o Outcome) checks.Run {
	run := checks.Run{
		Status:     "completed",
		Conclusion: o.Conclusion(),
		Output:     checks.Output{Title: o.Title(), Summary: summaryLine(t, o)},
	}
	if o.State == spec.StateFailed {
		run.Output.Text = FitText(o.Err, o.Tail, TextBudget)
	}
	return run
}

// summaryLine is a check run's output summary: where the test ran, and the
// server's request ID once known.
func summaryLine(t Test, o Outcome) string {
	s := t.Label() + " on " + t.Platform
	if o.RequestID != "" {
		s += " · request " + o.RequestID
	}
	return s
}
