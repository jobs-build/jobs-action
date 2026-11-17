package action

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jobs-build/assimilate/builds"
	"github.com/jobs-build/assimilate/jobs"
	"github.com/jobs-build/assimilate/spec"
	"github.com/jobs-build/jobs-iroh/api"

	"github.com/jobs-build/jobs-action/checks"
)

// guard bounds waits only a broken Run would hit.
const guard = 10 * time.Second

// script is what the fake backend does for one test directory.
type script struct {
	logs   []string
	nodes  []api.NodeSnap // sent as one snapshot before the terminal state
	state  spec.BuildState
	err    error
	submit error
	block  bool // Follow waits for Cancel, then ends cancelled
	hang   bool // Follow and Cancel never return (until the test ends)
}

// backend is a scripted builds.Backend, keyed by spec path.
type backend struct {
	scripts map[string]script
	started chan string   // a blocking Follow announces itself here
	release chan struct{} // closed at test end: frees hung calls

	mu        sync.Mutex
	submitted []spec.BuildSpec
	cancelled map[string]chan struct{}
}

func newBackend(t *testing.T, scripts map[string]script) *backend {
	b := &backend{
		scripts:   scripts,
		started:   make(chan string, 16),
		release:   make(chan struct{}),
		cancelled: map[string]chan struct{}{},
	}
	t.Cleanup(func() { close(b.release) })
	return b
}

func (b *backend) SourceFor(ctx context.Context, root string, s spec.BuildSpec) (jobs.Source, error) {
	return jobs.Source{}, ctx.Err()
}

func (b *backend) PushSource(ctx context.Context, src *jobs.Source, prog jobs.ProgressFunc) error {
	prog(1, 1)
	return nil
}

func (b *backend) Submit(ctx context.Context, src jobs.Source, s spec.BuildSpec) (jobs.Handle, error) {
	b.mu.Lock()
	b.submitted = append(b.submitted, s)
	b.cancelled[s.Path] = make(chan struct{})
	b.mu.Unlock()
	if err := b.scripts[s.Path].submit; err != nil {
		return jobs.Handle{}, err
	}
	return jobs.Handle{RequestID: s.Path, K: "k" + s.Path}, nil
}

func (b *backend) cancelChan(path string) chan struct{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.cancelled[path]
}

func (b *backend) Follow(ctx context.Context, h jobs.Handle, sink jobs.Sink) (spec.BuildState, error) {
	sc := b.scripts[h.RequestID]
	sink.State("running", "0/1 built")
	for _, l := range sc.logs {
		sink.Log("testrun_ab", l)
	}
	sink.Snapshot(api.Snapshot{RequestID: "req" + h.RequestID, Nodes: sc.nodes})
	switch {
	case sc.hang:
		b.started <- h.RequestID
		<-b.release
		return spec.StateCancelled, nil
	case sc.block:
		b.started <- h.RequestID
		select {
		case <-b.cancelChan(h.RequestID):
		case <-b.release:
		}
		return spec.StateCancelled, nil
	}
	if sc.err != nil {
		return "", sc.err
	}
	state := sc.state
	if state == "" {
		state = spec.StateDone
	}
	return state, nil
}

func (b *backend) Cancel(ctx context.Context, h jobs.Handle) error {
	if b.scripts[h.RequestID].hang {
		<-b.release
		return nil
	}
	ch := b.cancelChan(h.RequestID)
	select {
	case <-ch:
	default:
		close(ch)
	}
	return nil
}

// ghAPI is a recording report.API.
type ghAPI struct {
	mu        sync.Mutex
	creates   []checks.Run
	updates   map[int64][]checks.Run
	createErr error
	stall     chan struct{} // when set, Update blocks until it is closed
}

func (g *ghAPI) Create(_ context.Context, r checks.Run) (int64, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.createErr != nil {
		g.creates = append(g.creates, r)
		return 0, g.createErr
	}
	g.creates = append(g.creates, r)
	return int64(len(g.creates)), nil
}

func (g *ghAPI) Update(_ context.Context, id int64, r checks.Run) error {
	if g.stall != nil {
		<-g.stall
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.updates == nil {
		g.updates = map[int64][]checks.Run{}
	}
	g.updates[id] = append(g.updates[id], r)
	return nil
}

// byName returns each created check's updates, keyed by check name.
func (g *ghAPI) byName() map[string][]checks.Run {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := map[string][]checks.Run{}
	for i, c := range g.creates {
		out[c.Name] = append([]checks.Run(nil), g.updates[int64(i+1)]...)
	}
	return out
}

func (g *ghAPI) calls() (creates, updates int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, u := range g.updates {
		updates += len(u)
	}
	return len(g.creates), updates
}

// payloads is everything sent to the API, as one string.
func (g *ghAPI) payloads() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return fmt.Sprintf("%+v %+v", g.creates, g.updates)
}

// syncBuffer is a bytes.Buffer safe to read while a lingering goroutine may
// still write.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// harness wires Run to fakes.
type harness struct {
	cfg        Config
	deps       Deps
	gh         *ghAPI
	be         *backend
	out, err   *syncBuffer
	dials      int
	discovers  int
	summary    string
	outputFile string
}

func newHarness(t *testing.T, dirs []string, scripts map[string]script) *harness {
	t.Helper()
	tmp := t.TempDir()
	h := &harness{
		gh:         &ghAPI{},
		be:         newBackend(t, scripts),
		out:        &syncBuffer{},
		err:        &syncBuffer{},
		summary:    filepath.Join(tmp, "summary.md"),
		outputFile: filepath.Join(tmp, "output"),
	}
	h.cfg = Config{
		Server:   "SECRETID",
		Platform: "linux/amd64",
		Root:     t.TempDir(),
		Env: Env{
			Actions:     true,
			Token:       "tok",
			Repository:  "o/r",
			SHA:         "sha0",
			EventName:   "push",
			StepSummary: h.summary,
			Output:      h.outputFile,
		},
		Stdout: h.out,
		Stderr: h.err,
	}
	h.deps = Deps{
		Discover: func(context.Context, string) ([]string, error) {
			h.discovers++
			return dirs, nil
		},
		Dial: func(context.Context) (builds.Backend, func(), error) {
			h.dials++
			return h.be, func() {}, nil
		},
		API:   h.gh,
		Now:   time.Now,
		Grace: guard,
	}
	return h
}

func (h *harness) run(ctx context.Context) int { return Run(ctx, h.cfg, h.deps) }

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(b)
}

// one returns the single update a check received.
func one(t *testing.T, updates map[string][]checks.Run, name string) checks.Run {
	t.Helper()
	u, ok := updates[name]
	if !ok {
		t.Fatalf("no check named %q; have %v", name, keys(updates))
	}
	if len(u) != 1 {
		t.Fatalf("check %q got %d updates, want 1", name, len(u))
	}
	return u[0]
}

func keys(m map[string][]checks.Run) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestRunPassCachedFail(t *testing.T) {
	h := newHarness(t, []string{"/", "/lib", "/svc"}, map[string]script{
		"/":    {logs: []string{"ok"}},
		"/lib": {nodes: []api.NodeSnap{{Node: "pin_00"}, {Node: "testrun_00", Cached: true}}},
		"/svc": {logs: []string{"--- FAIL: TestQueue"}, state: spec.StateFailed},
	})
	if code := h.run(context.Background()); code != 1 {
		t.Fatalf("exit = %d, want 1\nstderr: %s", code, h.err.String())
	}

	// What was submitted is assimilate's project-tree test: whole tree, no args.
	got := map[string]spec.BuildSpec{}
	for _, s := range h.be.submitted {
		s.Name = ""
		got[s.Path] = s
	}
	for _, dir := range []string{"/", "/lib", "/svc"} {
		if want := spec.WholeTreeTest(dir, "linux/amd64"); !reflect.DeepEqual(got[dir], want) {
			t.Errorf("spec for %s = %+v, want %+v", dir, got[dir], want)
		}
	}

	updates := h.gh.byName()
	if len(updates) != 3 {
		t.Fatalf("checks = %v", keys(updates))
	}
	for _, c := range h.gh.creates {
		if c.Status != "in_progress" || c.HeadSHA != "sha0" {
			t.Errorf("create = %+v", c)
		}
	}
	pass := one(t, updates, "jobs / . (linux/amd64)")
	if pass.Conclusion != "success" || !strings.HasPrefix(pass.Output.Title, "passed") {
		t.Errorf("pass = %+v", pass)
	}
	if !strings.Contains(pass.Output.Summary, "req/") {
		t.Errorf("pass summary lacks the request id: %q", pass.Output.Summary)
	}
	hit := one(t, updates, "jobs / lib (linux/amd64)")
	if hit.Conclusion != "success" || hit.Output.Title != "already passed (cached)" {
		t.Errorf("cached = %+v", hit)
	}
	fail := one(t, updates, "jobs / svc (linux/amd64)")
	if fail.Conclusion != "failure" || !strings.Contains(fail.Output.Text, "--- FAIL: TestQueue") || !strings.Contains(fail.Output.Text, "test failed") {
		t.Errorf("fail = %+v", fail)
	}

	summary := readFile(t, h.summary)
	if !strings.Contains(summary, "### jobs tests: 2 passed (1 already passed), 1 failed") {
		t.Errorf("summary:\n%s", summary)
	}
	if !strings.Contains(summary, "| lib | linux/amd64 | already passed |") || !strings.Contains(summary, "--- FAIL: TestQueue") {
		t.Errorf("summary:\n%s", summary)
	}
	if got, want := readFile(t, h.outputFile), "passed=2\ncached=1\nfailed=1\ncancelled=0\n"; got != want {
		t.Errorf("outputs = %q, want %q", got, want)
	}
	out := h.out.String()
	if n := strings.Count(out, "::error title=jobs / "); n != 1 {
		t.Errorf("error annotations = %d, want 1\n%s", n, out)
	}
	if !strings.Contains(out, "::error title=jobs / svc (linux/amd64)::svc: test failed") {
		t.Errorf("stdout:\n%s", out)
	}
	if !strings.Contains(out, "2 passed (1 already passed), 1 failed") {
		t.Errorf("stdout lacks the headline:\n%s", out)
	}
	if !strings.Contains(out, "[svc] testrun_ab │ --- FAIL: TestQueue") {
		t.Errorf("stdout lacks the test's output:\n%s", out)
	}
}

func TestRunAllPassExitZero(t *testing.T) {
	h := newHarness(t, []string{"/a", "/b"}, map[string]script{})
	if code := h.run(context.Background()); code != 0 {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, h.err.String())
	}
	if strings.Contains(h.out.String(), "::error") {
		t.Fatalf("stdout:\n%s", h.out.String())
	}
	if got := readFile(t, h.outputFile); got != "passed=2\ncached=0\nfailed=0\ncancelled=0\n" {
		t.Fatalf("outputs = %q", got)
	}
}

func TestRunSubmitRejected(t *testing.T) {
	h := newHarness(t, []string{"/a"}, map[string]script{
		"/a": {submit: errors.New("server: unavailable: no live runners for platform linux/amd64")},
	})
	if code := h.run(context.Background()); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	u := one(t, h.gh.byName(), "jobs / a (linux/amd64)")
	if u.Conclusion != "failure" || !strings.Contains(u.Output.Text, "no live runners") {
		t.Fatalf("update = %+v", u)
	}
}

func TestRunNoTests(t *testing.T) {
	h := newHarness(t, nil, nil)
	h.cfg.Server = "" // not needed when there is nothing to run
	if code := h.run(context.Background()); code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if h.dials != 0 {
		t.Fatal("dialled with nothing to run")
	}
	if !strings.Contains(readFile(t, h.summary), "no tests found") || !strings.Contains(h.out.String(), "no tests found") {
		t.Fatalf("summary %q, stdout %q", readFile(t, h.summary), h.out.String())
	}
	if got := readFile(t, h.outputFile); got != "passed=0\ncached=0\nfailed=0\ncancelled=0\n" {
		t.Fatalf("outputs = %q", got)
	}
	if c, u := h.gh.calls(); c+u != 0 {
		t.Fatalf("API calls: %d creates, %d updates", c, u)
	}
}

func TestRunList(t *testing.T) {
	h := newHarness(t, []string{"/", "/a"}, nil)
	h.cfg.List = true
	h.cfg.Server = ""
	if code := h.run(context.Background()); code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if got, want := h.out.String(), ".\tlinux/amd64\na\tlinux/amd64\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if h.dials != 0 {
		t.Fatal("--list dialled the server")
	}
}

func TestRunEmptyServer(t *testing.T) {
	h := newHarness(t, []string{"/a"}, nil)
	h.cfg.Server = ""
	if code := h.run(context.Background()); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(h.err.String(), "forks") || !strings.Contains(h.err.String(), `"server"`) {
		t.Fatalf("stderr = %q", h.err.String())
	}
	if h.dials != 0 {
		t.Fatal("dialled without a server")
	}
}

func TestRunBadRoot(t *testing.T) {
	h := newHarness(t, []string{"/a"}, nil)
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{file, filepath.Join(t.TempDir(), "missing")} {
		h.cfg.Root = root
		if code := h.run(context.Background()); code != 1 {
			t.Fatalf("root %s: exit = %d, want 1", root, code)
		}
		if !strings.Contains(h.err.String(), root) {
			t.Fatalf("stderr does not name %s: %q", root, h.err.String())
		}
	}
	if h.discovers != 0 || h.dials != 0 {
		t.Fatalf("discovers = %d, dials = %d", h.discovers, h.dials)
	}
}

func TestRunBadPlatform(t *testing.T) {
	for _, platform := range []string{"linux", "", "linux/amd64 ", "Linux/AMD64", "a/b/c"} {
		h := newHarness(t, []string{"/a"}, nil)
		h.cfg.Platform = platform
		if code := h.run(context.Background()); code != 1 {
			t.Fatalf("platform %q: exit = %d, want 1", platform, code)
		}
		if !strings.Contains(h.err.String(), "platform") {
			t.Fatalf("stderr = %q", h.err.String())
		}
	}
}

func TestRunDiscoverError(t *testing.T) {
	h := newHarness(t, nil, nil)
	h.deps.Discover = func(context.Context, string) ([]string, error) { return nil, errors.New("ingest: boom") }
	if code := h.run(context.Background()); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(h.err.String(), "ingest: boom") {
		t.Fatalf("stderr = %q", h.err.String())
	}
}

func TestRunDialFailureScrubbed(t *testing.T) {
	h := newHarness(t, []string{"/a"}, nil)
	h.deps.Dial = func(context.Context) (builds.Backend, func(), error) {
		return nil, nil, errors.New("dial SECRETID (amber): refused")
	}
	if code := h.run(context.Background()); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if c, _ := h.gh.calls(); c != 0 {
		t.Fatalf("checks created before the dial succeeded: %d", c)
	}
	if !strings.Contains(h.err.String(), "refused") {
		t.Fatalf("stderr = %q", h.err.String())
	}
	if all := h.out.String() + h.err.String(); strings.Contains(all, "SECRETID") {
		t.Fatalf("the server ID leaked:\n%s", all)
	}
}

func TestRunScrubsEverywhere(t *testing.T) {
	h := newHarness(t, []string{"/a", "/b"}, map[string]script{
		"/a": {logs: []string{"connecting to SECRETID failed"}, state: spec.StateFailed},
		"/b": {err: errors.New("stream to SECRETID reset")},
	})
	if code := h.run(context.Background()); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	for name, text := range map[string]string{
		"stdout":       h.out.String(),
		"stderr":       h.err.String(),
		"summary":      readFile(t, h.summary),
		"API payloads": h.gh.payloads(),
	} {
		if strings.Contains(text, "SECRETID") {
			t.Errorf("the server ID leaked into %s:\n%s", name, text)
		}
	}
	if !strings.Contains(h.gh.payloads(), "connecting to <server> failed") {
		t.Errorf("the scrubbed line is missing from the check: %s", h.gh.payloads())
	}
}

func TestRunHeadSHAFromPullRequest(t *testing.T) {
	h := newHarness(t, []string{"/a"}, nil)
	payload := filepath.Join(t.TempDir(), "event.json")
	if err := os.WriteFile(payload, []byte(`{"pull_request":{"head":{"sha":"prhead"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	h.cfg.Env.EventName, h.cfg.Env.EventPath = "pull_request", payload
	if code := h.run(context.Background()); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if len(h.gh.creates) != 1 || h.gh.creates[0].HeadSHA != "prhead" {
		t.Fatalf("creates = %+v", h.gh.creates)
	}
}

func TestRunHeadSHAUnreadable(t *testing.T) {
	h := newHarness(t, []string{"/a"}, map[string]script{"/a": {state: spec.StateFailed}})
	h.cfg.Env.EventName, h.cfg.Env.EventPath = "pull_request", filepath.Join(t.TempDir(), "missing.json")
	if code := h.run(context.Background()); code != 1 {
		t.Fatalf("exit = %d, want 1 (the test failed)", code)
	}
	if c, u := h.gh.calls(); c+u != 0 {
		t.Fatalf("API calls: %d creates, %d updates", c, u)
	}
	if n := strings.Count(h.out.String(), "::warning::"); n != 1 {
		t.Fatalf("warnings = %d, want 1\n%s", n, h.out.String())
	}
	if !strings.Contains(readFile(t, h.summary), "0 passed (0 already passed), 1 failed") {
		t.Fatalf("summary:\n%s", readFile(t, h.summary))
	}
}

func TestRunNoToken(t *testing.T) {
	h := newHarness(t, []string{"/a"}, nil)
	h.cfg.Env.Token = ""
	if code := h.run(context.Background()); code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if c, u := h.gh.calls(); c+u != 0 {
		t.Fatalf("API calls: %d creates, %d updates", c, u)
	}
	if n := strings.Count(h.out.String(), "::warning::"); n != 1 {
		t.Fatalf("warnings = %d, want 1\n%s", n, h.out.String())
	}
}

func TestRunPermissionDenied(t *testing.T) {
	for _, state := range []spec.BuildState{spec.StateDone, spec.StateFailed} {
		h := newHarness(t, []string{"/a", "/b"}, map[string]script{"/a": {state: state}})
		h.gh.createErr = &checks.Error{Status: 403, Message: "Resource not accessible by integration"}
		want := 0
		if state == spec.StateFailed {
			want = 1
		}
		if code := h.run(context.Background()); code != want {
			t.Fatalf("state %s: exit = %d, want %d", state, code, want)
		}
		if n := strings.Count(h.out.String(), "::warning::"); n != 1 {
			t.Fatalf("warnings = %d, want 1\n%s", n, h.out.String())
		}
		if !strings.Contains(h.out.String(), "checks: write") {
			t.Fatalf("stdout:\n%s", h.out.String())
		}
		if readFile(t, h.summary) == "" {
			t.Fatal("the summary must still be written")
		}
	}
}

func TestRunCancelled(t *testing.T) {
	h := newHarness(t, []string{"/a", "/b"}, map[string]script{"/a": {block: true}, "/b": {block: true}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-h.be.started
		<-h.be.started
		cancel()
	}()
	done := make(chan int, 1)
	go func() { done <- h.run(ctx) }()
	select {
	case code := <-done:
		if code != 1 {
			t.Fatalf("exit = %d, want 1", code)
		}
	case <-time.After(guard):
		t.Fatal("Run did not return after cancellation")
	}
	updates := h.gh.byName()
	for _, name := range []string{"jobs / a (linux/amd64)", "jobs / b (linux/amd64)"} {
		if u := one(t, updates, name); u.Status != "completed" || u.Conclusion != "cancelled" {
			t.Errorf("%s = %+v", name, u)
		}
	}
	if !strings.Contains(readFile(t, h.summary), "0 passed (0 already passed), 0 failed, 2 cancelled") {
		t.Fatalf("summary:\n%s", readFile(t, h.summary))
	}
	if got := readFile(t, h.outputFile); got != "passed=0\ncached=0\nfailed=0\ncancelled=2\n" {
		t.Fatalf("outputs = %q", got)
	}
}

// A server that stops answering must not keep the process past its deadline:
// GitHub kills it about ten seconds after the first signal.
func TestRunCancelHungBackend(t *testing.T) {
	h := newHarness(t, []string{"/a", "/b"}, map[string]script{"/a": {hang: true}, "/b": {hang: true}})
	h.deps.Grace = 300 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-h.be.started
		<-h.be.started
		cancel()
	}()
	done := make(chan int, 1)
	start := time.Now()
	go func() { done <- h.run(ctx) }()
	select {
	case code := <-done:
		if code != 1 {
			t.Fatalf("exit = %d, want 1", code)
		}
	case <-time.After(guard):
		t.Fatal("Run did not return: a hung backend held it past the deadline")
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("Run took %v with a 300ms grace", d)
	}
	updates := h.gh.byName()
	for _, name := range []string{"jobs / a (linux/amd64)", "jobs / b (linux/amd64)"} {
		if u := one(t, updates, name); u.Conclusion != "cancelled" {
			t.Errorf("%s = %+v", name, u)
		}
	}
	if !strings.Contains(readFile(t, h.summary), "2 cancelled") {
		t.Fatalf("summary:\n%s", readFile(t, h.summary))
	}
	if got := readFile(t, h.outputFile); got != "passed=0\ncached=0\nfailed=0\ncancelled=2\n" {
		t.Fatalf("outputs = %q", got)
	}
}

func TestRunOutsideActions(t *testing.T) {
	h := newHarness(t, []string{"/a", "/b"}, map[string]script{"/b": {state: spec.StateFailed}})
	h.cfg.Env = Env{}
	if code := h.run(context.Background()); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if c, u := h.gh.calls(); c+u != 0 {
		t.Fatalf("API calls: %d creates, %d updates", c, u)
	}
	if readFile(t, h.summary) != "" || readFile(t, h.outputFile) != "" {
		t.Fatal("files written outside GitHub Actions")
	}
	out := h.out.String()
	if strings.Contains(out, "::error") || strings.Contains(out, "::warning") {
		t.Fatalf("workflow commands printed outside GitHub Actions:\n%s", out)
	}
	if !strings.Contains(out, "1 passed (0 already passed), 1 failed") {
		t.Fatalf("stdout lacks the headline:\n%s", out)
	}
	if !strings.Contains(h.err.String(), "b: test failed") {
		t.Fatalf("stderr does not list the failure: %q", h.err.String())
	}
}

func TestSplitAddrs(t *testing.T) {
	if got := SplitAddrs(" 10.0.0.1:4433, ,host:1 "); !reflect.DeepEqual(got, []string{"10.0.0.1:4433", "host:1"}) {
		t.Fatalf("SplitAddrs = %q", got)
	}
	if got := SplitAddrs(""); got != nil {
		t.Fatalf("SplitAddrs(\"\") = %q", got)
	}
}

// Main with --list goes through the real store: discovery sees the ingested
// tree, so .amberignore applies and a directory named TESTS.jobs is no test.
func TestMainListDiscoversIngestedTree(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("TESTS.jobs", "def test(): pass\n")
	write("services/api/TESTS.jobs", "def test(): pass\n")
	write("lib/TESTS.jobs", "def test(): pass\n")
	write("ignored/TESTS.jobs", "def test(): pass\n")
	write(".amberignore", "/ignored\n")
	if err := os.MkdirAll(filepath.Join(root, "other", "TESTS.jobs"), 0o755); err != nil {
		t.Fatal(err)
	}
	var out, errw syncBuffer
	code := Main(context.Background(), Config{
		Platform: "linux/arm64",
		Root:     root,
		DataDir:  t.TempDir(),
		List:     true,
		Stdout:   &out,
		Stderr:   &errw,
	})
	if code != 0 {
		t.Fatalf("exit = %d\nstderr: %s", code, errw.String())
	}
	if got, want := out.String(), ".\tlinux/arm64\nlib\tlinux/arm64\nservices/api\tlinux/arm64\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

// GitHub not answering must not hold a finished run: the verdict is in, and
// waiting on a courtesy could run the job into its timeout.
func TestRunReportWaitBounded(t *testing.T) {
	h := newHarness(t, []string{"/a", "/b"}, nil)
	h.gh.stall = make(chan struct{})
	t.Cleanup(func() { close(h.gh.stall) })
	h.deps.ReportWait = 100 * time.Millisecond
	done := make(chan int, 1)
	go func() { done <- h.run(context.Background()) }()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit = %d, want 0: both tests passed", code)
		}
	case <-time.After(guard):
		t.Fatal("Run waited on GitHub without bound")
	}
	if n := strings.Count(h.out.String(), "::warning::"); n != 1 || !strings.Contains(h.out.String(), "in progress") {
		t.Fatalf("want one warning about checks left in progress:\n%s", h.out.String())
	}
	if !strings.Contains(readFile(t, h.summary), "2 passed") {
		t.Fatal("the summary must be written before waiting on GitHub")
	}
}
