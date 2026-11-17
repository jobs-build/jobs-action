// Package action is the jobs-action command: find every TESTS.jobs under a
// root, run each on a jobs-iroh server through assimilate's orchestrator, and
// report the results to GitHub.
package action

import (
	"context"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jobs-build/assimilate/builds"
	"github.com/jobs-build/assimilate/spec"

	"github.com/jobs-build/jobs-action/checks"
	"github.com/jobs-build/jobs-action/report"
)

// DefaultGrace is how long Run keeps working after cancellation. GitHub kills
// a cancelled step about ten seconds after its first signal; the rest of the
// time is for the final writes.
const DefaultGrace = 6 * time.Second

// DefaultReportWait is how long a finished run waits for GitHub to take its
// check run completions. It is long enough to sit out a secondary rate limit,
// and short enough that a GitHub outage cannot run a passing job into its
// timeout.
const DefaultReportWait = 90 * time.Second

// serverMark stands in for the server endpoint ID wherever it would be
// printed or reported. Knowing the ID is what grants access to the server.
const serverMark = "<server>"

// Env is what the binary reads from GitHub Actions' environment.
type Env struct {
	Actions     bool   // GITHUB_ACTIONS == "true"
	Token       string // GITHUB_TOKEN
	Repository  string // GITHUB_REPOSITORY, owner/name
	SHA         string // GITHUB_SHA
	EventName   string // GITHUB_EVENT_NAME
	EventPath   string // GITHUB_EVENT_PATH
	APIURL      string // GITHUB_API_URL
	StepSummary string // GITHUB_STEP_SUMMARY
	Output      string // GITHUB_OUTPUT
}

// EnvFromOS reads Env from the process environment.
func EnvFromOS() Env {
	return Env{
		Actions:     os.Getenv("GITHUB_ACTIONS") == "true",
		Token:       os.Getenv("GITHUB_TOKEN"),
		Repository:  os.Getenv("GITHUB_REPOSITORY"),
		SHA:         os.Getenv("GITHUB_SHA"),
		EventName:   os.Getenv("GITHUB_EVENT_NAME"),
		EventPath:   os.Getenv("GITHUB_EVENT_PATH"),
		APIURL:      os.Getenv("GITHUB_API_URL"),
		StepSummary: os.Getenv("GITHUB_STEP_SUMMARY"),
		Output:      os.Getenv("GITHUB_OUTPUT"),
	}
}

// Config is one invocation.
type Config struct {
	Server   string   // jobs-iroh server endpoint ID; a credential
	Addrs    []string // optional direct host:port addresses
	Platform string   // os/arch every test runs on
	Root     string   // absolute project root: the tree that is ingested and searched
	DataDir  string   // local store (Main only)
	List     bool     // print the tests and exit
	Env      Env
	Stdout   io.Writer
	Stderr   io.Writer
}

// Deps are Run's seams to the outside.
type Deps struct {
	// Discover returns the "/"-rooted directories under root that hold a
	// TESTS.jobs, sorted (jobs.Local.TestDirs).
	Discover func(ctx context.Context, root string) ([]string, error)
	// Dial connects to the server. The returned func closes the connection.
	Dial func(ctx context.Context) (builds.Backend, func(), error)
	// API writes check runs; nil means no check reporting.
	API report.API
	Now func() time.Time
	// Grace is how long Run keeps working after ctx is cancelled; 0 means
	// DefaultGrace.
	Grace time.Duration
	// ReportWait is how long a finished run waits for its check runs to be
	// completed on GitHub; 0 means DefaultReportWait.
	ReportWait time.Duration
}

var platformRE = regexp.MustCompile(`^[a-z0-9_]+/[a-z0-9_]+$`)

// Run is the whole command and returns the exit code: 0 when every test
// passed or none was found, 1 otherwise. ctx is cancelled by the first
// SIGINT or SIGTERM.
func Run(ctx context.Context, cfg Config, d Deps) int {
	scrub := newScrubber(cfg.Server)
	out := &lineWriter{w: cfg.Stdout, scrub: scrub}
	errw := &lineWriter{w: cfg.Stderr, scrub: scrub}
	fatal := func(format string, args ...any) int {
		fmt.Fprintf(errw, "jobs-action: "+format+"\n", args...)
		return 1
	}
	warn := func(msg string) {
		if cfg.Env.Actions {
			fmt.Fprintln(out, report.WarningCommand(scrub(msg)))
		} else {
			fmt.Fprintf(errw, "warning: %s\n", msg)
		}
	}

	if !platformRE.MatchString(cfg.Platform) {
		return fatal("invalid platform %q: want os/arch, for example linux/amd64", cfg.Platform)
	}
	if fi, err := os.Stat(cfg.Root); err != nil || !fi.IsDir() {
		return fatal("root %s is not a directory", cfg.Root)
	}
	dirs, err := d.Discover(ctx, cfg.Root)
	if err != nil {
		return fatal("find tests under %s: %v", cfg.Root, err)
	}
	tests := make([]report.Test, len(dirs))
	specs := make([]spec.BuildSpec, len(dirs))
	for i, dir := range dirs {
		tests[i] = report.Test{Path: dir, Platform: cfg.Platform}
		// assimilate's project-tree test: the whole root tree as context, the
		// directory as Dir, no params. That shape is what makes a pass
		// recorded here a memo hit for a developer, and the other way round.
		specs[i] = spec.WholeTreeTest(dir, cfg.Platform)
		specs[i].Name = tests[i].Label() // display only; not part of the key
	}

	if cfg.List {
		for _, t := range tests {
			fmt.Fprintf(out, "%s\t%s\n", t.Label(), t.Platform)
		}
		return 0
	}
	if len(tests) == 0 {
		fmt.Fprintln(out, report.Headline(nil))
		writeReport(cfg.Env, warn, nil, nil)
		return 0
	}
	if cfg.Server == "" {
		return fatal(`no server: set the "server" input (repository secrets are not available to pull requests from forks)`)
	}

	// Dial before any check run exists, so a failed connection orphans none.
	backend, closeBackend, err := d.Dial(ctx)
	if err != nil {
		return fatal("connect to the jobs server: %v", err)
	}

	now := d.Now
	if now == nil {
		now = time.Now
	}
	plain := report.NewPlain(out, tests, now)
	plain.Note(fmt.Sprintf("%d tests on %s", len(tests), cfg.Platform))
	chk := openChecks(ctx, cfg.Env, d.API, tests, warn)

	// The run outlives ctx: after a signal it is cancelled explicitly and
	// given the grace period to drain.
	runCtx, cancelRun := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelRun()
	events := make(chan spec.Event, 256)
	done := make(chan []builds.Result, 1)
	go func() {
		results, _ := builds.Run(runCtx, cfg.Root, "", specs, backend, events)
		done <- results
		close(events)
	}()

	tracker := report.NewTracker(tests, now)
	tidy := report.NewTidy(len(tests))
	handle := func(ev spec.Event) {
		ev.Line, ev.Info = scrub(ev.Line), scrub(ev.Info)
		ev, ok := tidy.Event(ev)
		if !ok {
			return
		}
		plain.Handle(ev)
		if i, terminal := tracker.Handle(ev); terminal {
			chk.Complete(i, tracker.Outcome(i))
		}
	}

	var (
		stop       = ctx.Done()
		expired    chan struct{} // closed when the grace period is over
		cancelDone chan struct{} // closed when the open checks were cancelled
		abandoned  bool          // the run did not finish within the grace period
	)
loop:
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				break loop
			}
			handle(ev)
		case <-stop:
			stop = nil
			plain.Note("cancelling")
			cancelRun()
			grace := d.Grace
			if grace <= 0 {
				grace = DefaultGrace
			}
			expired = make(chan struct{})
			timer := time.AfterFunc(grace, func() { close(expired) })
			defer timer.Stop()
			// Close the open checks now, not after the drain: the drain can
			// outlast the time the process has left.
			cancelDone = make(chan struct{})
			go func() {
				defer close(cancelDone)
				chk.CancelOpen()
			}()
		case <-expired:
			abandoned = true
			break loop
		}
	}

	outcomes := make([]report.Outcome, len(tests))
	for i := range tests {
		outcomes[i] = tracker.Outcome(i)
	}
	if abandoned {
		// builds.Run is still draining; keep its channel moving so that it
		// can never block on us.
		go func() {
			for range events {
			}
		}()
		plain.Note("the server did not confirm the cancellation in time")
	} else {
		// builds.Result settles any test whose terminal event the tracker did
		// not see. Its cached flag is the server's; the tracker also knows a
		// test whose finished run the request joined.
		for i, r := range <-done {
			o := &outcomes[i]
			if o.State == "" && r.State.Terminal() {
				o.State, o.Err = r.State, scrub(r.Err)
			}
			if o.State == spec.StateDone {
				o.Cached = o.Cached || r.Cached
			}
		}
	}

	// The verdict first; waiting on GitHub comes after it.
	fmt.Fprintln(out, report.Headline(outcomes))
	writeReport(cfg.Env, warn, tests, outcomes)
	code := 0
	for i, o := range outcomes {
		if o.State == spec.StateDone {
			continue
		}
		code = 1
		if cfg.Env.Actions {
			fmt.Fprintln(out, report.ErrorCommand(tests[i], o))
		} else {
			detail := o.Err
			if detail == "" {
				detail = o.Result()
			}
			fmt.Fprintf(errw, "  %s: %s\n", tests[i].Label(), detail)
		}
	}

	if expired == nil {
		reportWait := d.ReportWait
		if reportWait <= 0 {
			reportWait = DefaultReportWait
		}
		timer := time.NewTimer(reportWait)
		defer timer.Stop()
		select {
		case <-waited(chk.Wait):
		case <-timer.C:
			warn(fmt.Sprintf("gave up waiting for GitHub after %s; some check runs may stay in progress", reportWait))
		}
	} else {
		waitUntil(expired, cancelDone)
		waitUntil(expired, waited(chk.Wait))
	}
	if !abandoned && closeBackend != nil {
		closeBackend()
	}
	return code
}

// openChecks creates the check runs, or says once why there will be none.
// Outside GitHub Actions there is nothing to say.
func openChecks(ctx context.Context, env Env, api report.API, tests []report.Test, warn func(string)) *report.Checks {
	const fallback = "; reporting through the job summary only"
	headSHA := ""
	switch {
	case !env.Actions || api == nil:
		api = nil
	case env.Token == "" || env.Repository == "":
		warn("no token or repository to create check runs with" + fallback)
		api = nil
	default:
		sha, err := checks.HeadSHA(env.EventName, env.EventPath, env.SHA)
		if err != nil {
			warn(fmt.Sprintf("cannot tell which commit to attach check runs to (%v)%s", err, fallback))
			api = nil
		}
		headSHA = sha
	}
	chk := report.NewChecks(api, headSHA, tests, warn)
	chk.Open(ctx)
	return chk
}

// writeReport writes the job summary and the step outputs. Outside GitHub
// Actions both paths are empty and nothing is written.
func writeReport(env Env, warn func(string), tests []report.Test, outcomes []report.Outcome) {
	if !env.Actions {
		return
	}
	if err := report.AppendSummary(env.StepSummary, report.Summary(tests, outcomes)); err != nil {
		warn(fmt.Sprintf("job summary not written: %v", err))
	}
	if err := report.WriteOutputs(env.Output, outcomes); err != nil {
		warn(fmt.Sprintf("step outputs not written: %v", err))
	}
}

// waited runs fn in the background and returns a channel closed when it
// returns.
func waited(fn func()) <-chan struct{} {
	ch := make(chan struct{})
	go func() {
		defer close(ch)
		fn()
	}()
	return ch
}

// waitUntil waits for done, but no longer than expired.
func waitUntil[T any](expired <-chan struct{}, done <-chan T) {
	select {
	case <-done:
	case <-expired:
	}
}

// SplitAddrs splits a comma-separated list of host:port addresses.
func SplitAddrs(s string) []string {
	var out []string
	for a := range strings.SplitSeq(s, ",") {
		if a = strings.TrimSpace(a); a != "" {
			out = append(out, a)
		}
	}
	return out
}

// lineWriter serialises writes and keeps the server ID out of them. Callers
// write whole lines, so the ID never straddles two writes.
type lineWriter struct {
	mu    sync.Mutex
	w     io.Writer
	scrub func(string) string
}

func (l *lineWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, err := io.WriteString(l.w, l.scrub(string(p))); err != nil {
		return 0, err
	}
	return len(p), nil
}
