package report

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jobs-build/assimilate/spec"

	"github.com/jobs-build/jobs-action/checks"
)

// fakeAPI records calls. createErr and updateErr, when set, decide each
// call's error from its 1-based call number.
type fakeAPI struct {
	mu        sync.Mutex
	creates   []checks.Run
	updates   []update
	createErr func(n int) error
	updateErr func(n int) error
}

type update struct {
	id  int64
	run checks.Run
}

func (f *fakeAPI) Create(_ context.Context, r checks.Run) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creates = append(f.creates, r)
	n := len(f.creates)
	if f.createErr != nil {
		if err := f.createErr(n); err != nil {
			return 0, err
		}
	}
	return int64(100 + n), nil
}

func (f *fakeAPI) Update(_ context.Context, id int64, r checks.Run) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates = append(f.updates, update{id, r})
	if f.updateErr != nil {
		return f.updateErr(len(f.updates))
	}
	return nil
}

func (f *fakeAPI) snapshot() ([]checks.Run, []update) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]checks.Run(nil), f.creates...), append([]update(nil), f.updates...)
}

// warnings collects warn calls.
type warnings struct {
	mu   sync.Mutex
	list []string
}

func (w *warnings) add(s string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.list = append(w.list, s)
}

func (w *warnings) all() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.list...)
}

var threeTests = []Test{
	{Path: "/a", Platform: "linux/amd64"},
	{Path: "/b", Platform: "linux/amd64"},
	{Path: "/", Platform: "linux/amd64"},
}

// fastRetries shrinks the waits between attempts for the test.
func fastRetries(t *testing.T) {
	t.Helper()
	old := retryWaits
	retryWaits = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	t.Cleanup(func() { retryWaits = old })
}

// serialCreates makes Open create one check at a time, in order.
func serialCreates(t *testing.T) {
	t.Helper()
	old := createPool
	createPool = 1
	t.Cleanup(func() { createPool = old })
}

func TestOpenCreatesInProgress(t *testing.T) {
	api, w := &fakeAPI{}, &warnings{}
	c := NewChecks(api, "abc", threeTests, w.add)
	c.Open(context.Background())
	creates, updates := api.snapshot()
	if len(creates) != 3 || len(updates) != 0 {
		t.Fatalf("creates = %d, updates = %d", len(creates), len(updates))
	}
	names := map[string]bool{}
	for _, r := range creates {
		names[r.Name] = true
		if r.Status != "in_progress" || r.HeadSHA != "abc" || r.Conclusion != "" {
			t.Errorf("create %+v", r)
		}
		if r.Output.Title != "running" || r.Output.Summary == "" {
			t.Errorf("create output %+v", r.Output)
		}
	}
	for _, tt := range threeTests {
		if !names[tt.CheckName()] {
			t.Errorf("no check named %q", tt.CheckName())
		}
	}
	if len(w.all()) != 0 {
		t.Fatalf("warnings: %v", w.all())
	}
}

func TestCompleteSuccessAndFailure(t *testing.T) {
	serialCreates(t)
	api, w := &fakeAPI{}, &warnings{}
	c := NewChecks(api, "abc", threeTests, w.add)
	c.Open(context.Background())
	c.Complete(0, Outcome{State: spec.StateDone, Duration: 41 * time.Second, RequestID: "req-7"})
	c.Complete(1, Outcome{State: spec.StateFailed, Duration: 72 * time.Second, Err: "exit 1", Tail: []string{"first", "last line"}})
	c.Wait()
	_, updates := api.snapshot()
	if len(updates) != 2 {
		t.Fatalf("updates = %d", len(updates))
	}
	byID := map[int64]checks.Run{}
	for _, u := range updates {
		byID[u.id] = u.run
	}
	pass, fail := byID[101], byID[102]
	if pass.Status != "completed" || pass.Conclusion != "success" || pass.Output.Title != "passed in 41s" || pass.Output.Text != "" {
		t.Errorf("pass = %+v", pass)
	}
	if !strings.Contains(pass.Output.Summary, "a on linux/amd64") || !strings.Contains(pass.Output.Summary, "req-7") {
		t.Errorf("pass summary = %q", pass.Output.Summary)
	}
	if fail.Status != "completed" || fail.Conclusion != "failure" || fail.Output.Title != "failed after 1m12s" {
		t.Errorf("fail = %+v", fail)
	}
	if !strings.Contains(fail.Output.Text, "exit 1") || !strings.Contains(fail.Output.Text, "last line") {
		t.Errorf("fail text = %q", fail.Output.Text)
	}
}

func TestCompleteFirstWins(t *testing.T) {
	api := &fakeAPI{}
	c := NewChecks(api, "abc", threeTests, func(string) {})
	c.Open(context.Background())
	c.Complete(0, Outcome{State: spec.StateDone})
	c.Complete(0, Outcome{State: spec.StateFailed})
	c.Wait()
	if _, updates := api.snapshot(); len(updates) != 1 || updates[0].run.Conclusion != "success" {
		t.Fatalf("updates = %+v", updates)
	}
}

func TestPermissionDegrades(t *testing.T) {
	serialCreates(t)
	api, w := &fakeAPI{createErr: func(int) error { return &checks.Error{Status: 403, Message: "Resource not accessible by integration"} }}, &warnings{}
	c := NewChecks(api, "abc", threeTests, w.add)
	c.Open(context.Background())
	c.Complete(0, Outcome{State: spec.StateDone})
	c.CancelOpen()
	c.Wait()
	creates, updates := api.snapshot()
	if len(creates) != 1 {
		t.Fatalf("creates = %d: the remaining creates should be skipped", len(creates))
	}
	if len(updates) != 0 {
		t.Fatalf("updates = %d", len(updates))
	}
	ws := w.all()
	if len(ws) != 1 || !strings.Contains(ws[0], "checks: write") {
		t.Fatalf("warnings = %q", ws)
	}
}

func TestRateLimitDegradesButCompletesCreated(t *testing.T) {
	serialCreates(t)
	api := &fakeAPI{createErr: func(n int) error {
		if n >= 2 {
			return &checks.Error{Status: 403, RateLimited: true}
		}
		return nil
	}}
	w := &warnings{}
	c := NewChecks(api, "abc", threeTests, w.add)
	c.Open(context.Background())
	c.Complete(0, Outcome{State: spec.StateDone})
	c.Complete(1, Outcome{State: spec.StateDone})
	c.Wait()
	creates, updates := api.snapshot()
	if len(creates) != 2 {
		t.Fatalf("creates = %d, want 2 (the third is skipped)", len(creates))
	}
	if len(updates) != 1 || updates[0].id != 101 {
		t.Fatalf("updates = %+v", updates)
	}
	ws := w.all()
	if len(ws) != 1 || !strings.Contains(ws[0], "rate limit") {
		t.Fatalf("warnings = %q", ws)
	}
}

func TestOtherCreateErrorLeavesTheRestReporting(t *testing.T) {
	serialCreates(t)
	api := &fakeAPI{createErr: func(n int) error {
		if n == 1 {
			return errors.New("connection reset")
		}
		return nil
	}}
	w := &warnings{}
	c := NewChecks(api, "abc", threeTests, w.add)
	c.Open(context.Background())
	creates, _ := api.snapshot()
	if len(creates) != 3 {
		t.Fatalf("creates = %d, want all three attempted", len(creates))
	}
	if ws := w.all(); len(ws) != 1 || !strings.Contains(ws[0], threeTests[0].CheckName()) {
		t.Fatalf("warnings = %q", ws)
	}
}

func TestCompleteRetries(t *testing.T) {
	fastRetries(t)
	api := &fakeAPI{updateErr: func(n int) error {
		if n <= 2 {
			return &checks.Error{Status: 502}
		}
		return nil
	}}
	w := &warnings{}
	c := NewChecks(api, "abc", threeTests, w.add)
	c.Open(context.Background())
	c.Complete(0, Outcome{State: spec.StateDone})
	c.Wait()
	if _, updates := api.snapshot(); len(updates) != 3 {
		t.Fatalf("updates = %d, want 3", len(updates))
	}
	if len(w.all()) != 0 {
		t.Fatalf("warnings = %q", w.all())
	}
}

func TestCompleteGivesUpQuietly(t *testing.T) {
	fastRetries(t)
	api := &fakeAPI{updateErr: func(int) error { return &checks.Error{Status: 502} }}
	w := &warnings{}
	c := NewChecks(api, "abc", threeTests, w.add)
	c.Open(context.Background())
	c.Complete(0, Outcome{State: spec.StateDone})
	c.Wait()
	if _, updates := api.snapshot(); len(updates) != 4 {
		t.Fatalf("updates = %d, want 4 attempts", len(updates))
	}
	if ws := w.all(); len(ws) != 1 || !strings.Contains(ws[0], threeTests[0].CheckName()) {
		t.Fatalf("warnings = %q", ws)
	}
}

func TestCompleteHonoursRetryAfter(t *testing.T) {
	fastRetries(t)
	old := maxRetryAfter
	maxRetryAfter = 30 * time.Millisecond
	t.Cleanup(func() { maxRetryAfter = old })
	api := &fakeAPI{updateErr: func(n int) error {
		if n == 1 {
			return &checks.Error{Status: 429, RateLimited: true, RetryAfter: time.Hour} // capped
		}
		return nil
	}}
	c := NewChecks(api, "abc", threeTests, func(string) {})
	c.Open(context.Background())
	start := time.Now()
	c.Complete(0, Outcome{State: spec.StateDone})
	c.Wait()
	if d := time.Since(start); d < 30*time.Millisecond || d > 5*time.Second {
		t.Fatalf("waited %v, want about the capped retry-after", d)
	}
	if _, updates := api.snapshot(); len(updates) != 2 {
		t.Fatalf("updates = %d", len(updates))
	}
}

func TestCancelOpen(t *testing.T) {
	serialCreates(t)
	api := &fakeAPI{}
	c := NewChecks(api, "abc", threeTests, func(string) {})
	c.Open(context.Background())
	c.Complete(0, Outcome{State: spec.StateDone})
	c.Wait()
	c.CancelOpen()
	c.Complete(1, Outcome{State: spec.StateFailed, Err: "late"}) // a terminal event arriving after the cancel
	c.Wait()
	_, updates := api.snapshot()
	if len(updates) != 3 {
		t.Fatalf("updates = %d, want 3", len(updates))
	}
	got := map[int64]string{}
	for _, u := range updates {
		got[u.id] = u.run.Conclusion
		if u.run.Status != "completed" {
			t.Errorf("update %+v", u)
		}
	}
	want := map[int64]string{101: "success", 102: "cancelled", 103: "cancelled"}
	for id, conclusion := range want {
		if got[id] != conclusion {
			t.Errorf("check %d = %q, want %q", id, got[id], conclusion)
		}
	}
}

func TestDisabled(t *testing.T) {
	api := &fakeAPI{}
	for _, c := range []*Checks{
		NewChecks(nil, "abc", threeTests, func(string) {}),
		NewChecks(api, "", threeTests, func(string) {}),
	} {
		c.Open(context.Background())
		c.Complete(0, Outcome{State: spec.StateDone})
		c.CancelOpen()
		c.Wait()
	}
	if creates, updates := api.snapshot(); len(creates)+len(updates) != 0 {
		t.Fatalf("calls made while disabled: %d creates, %d updates", len(creates), len(updates))
	}
}

// A run cancelled before its checks exist creates none and does not warn
// about each one.
func TestOpenCancelledContext(t *testing.T) {
	api, w := &fakeAPI{createErr: func(int) error { return context.Canceled }}, &warnings{}
	c := NewChecks(api, "abc", threeTests, w.add)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.Open(ctx)
	if creates, _ := api.snapshot(); len(creates) != 0 {
		t.Fatalf("creates = %d", len(creates))
	}
	if len(w.all()) != 0 {
		t.Fatalf("warnings = %q", w.all())
	}
}

// A create that GitHub may already have accepted must be allowed to finish
// after the run is cancelled: an aborted request loses the id, and a check
// nobody knows the id of stays in progress.
func TestOpenCreateSurvivesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	api := &lingeringAPI{cancel: cancel}
	c := NewChecks(api, "abc", threeTests[:1], func(string) {})
	c.Open(ctx)
	c.CancelOpen()
	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.updates) != 1 || api.updates[0].run.Conclusion != "cancelled" {
		t.Fatalf("updates = %+v: the check created during cancellation was not closed", api.updates)
	}
}

// lingeringAPI cancels the run while its create is in flight, then answers
// the way GitHub would: the request context decides whether the caller ever
// learns the id.
type lingeringAPI struct {
	fakeAPI
	cancel context.CancelFunc
}

func (l *lingeringAPI) Create(ctx context.Context, r checks.Run) (int64, error) {
	l.cancel()
	time.Sleep(20 * time.Millisecond)
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return 7, nil
}

// A 403 on an update is far more likely a secondary rate limit than a
// refusal: the same token created the check.
func TestCompleteRetriesBare403(t *testing.T) {
	fastRetries(t)
	api := &fakeAPI{updateErr: func(n int) error {
		if n <= 2 {
			return &checks.Error{Status: 403}
		}
		return nil
	}}
	w := &warnings{}
	c := NewChecks(api, "abc", threeTests, w.add)
	c.Open(context.Background())
	c.Complete(0, Outcome{State: spec.StateDone})
	c.Wait()
	if _, updates := api.snapshot(); len(updates) != 3 {
		t.Fatalf("updates = %d, want 3", len(updates))
	}
	if len(w.all()) != 0 {
		t.Fatalf("warnings = %q", w.all())
	}
}
