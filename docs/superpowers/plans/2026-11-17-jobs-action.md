# jobs-action Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A GitHub Action, backed by one Go binary, that runs every `TESTS.jobs` of a checkout on a jobs-iroh server and reports each test as a check run plus a job summary.

**Architecture:** The binary imports assimilate's `jobs`, `builds` and `spec` packages for discovery, push, submit, follow and cancel. New code is three packages: `checks` (GitHub Checks API over `net/http`), `report` (per-test tracking, check reporting, summary, plain log) and `action` (wiring, cancellation, exit code). A composite `action.yml` downloads the binary from this repository's release and `exec`s it.

**Tech Stack:** Go 1.26.5, `github.com/jobs-build/assimilate` v0.10.0 (pins jobs-iroh v0.36.0), standard library only otherwise. Nix flake devshell. GitHub Actions composite action.

**Spec:** `docs/superpowers/specs/2026-11-17-jobs-action-design.md`

## Global Constraints

- Module path `github.com/jobs-build/jobs-action`; `go 1.26.5`; licence AGPL-3.0 (copy assimilate's `LICENSE`).
- No dependency beyond assimilate and what it pulls in. Do not import `assimilate/ui` (bubbletea).
- Builds with `CGO_ENABLED=0` for linux/amd64 and linux/arm64. `gofmt -l .` prints nothing. `go vet ./...` clean.
- Check name: `jobs / <path> (<platform>)`; path without the leading slash; the root is `.`.
- Check output limits: `summary` and `text` at most 65535 characters; the binary budgets 60000 bytes for text.
- Two API writes per test: create `in_progress`, then `completed`. No `queued`.
- No GitHub API failure changes the exit code.
- The server endpoint ID never appears in stdout, stderr, the job summary or a check.
- Exit code: 0 when every test ended `done` (or none were found), 1 otherwise.
- Commits are Dragan's: the plan's checkpoints run the checks but do not `git commit`. `git init` only.
- Remove every binary a step builds (Dragan's standing instruction).

## Review Focus

1. **Server ID in error text.** `jobs.Dial` errors read `dial <server-id> (amber): …`. Every string that leaves the process is scrubbed. Test in Task 5.
2. **Test output that breaks the report's markup.** A log line containing three backticks or `</details>` must not end the code block in a check or the summary. Test in Task 2 (fence) and Task 4 (summary).
3. **One enormous log line.** A single 100,000-character line with no newline must still yield check text within the limit. Test in Task 2.
4. **Unreadable event payload.** `GITHUB_EVENT_PATH` missing or not JSON on a `pull_request` event: checks turn off with one warning, the run still gives its verdict. Test in Task 1 (error) and Task 5 (degrade).
5. **A `root` that is not a directory.** Clear error naming the path, exit 1, nothing dialled. Test in Task 5.

Also pinned though cheap: workflow-command injection through test output. Every plain log line starts with `[`, so a test printing `::add-mask::` or `::error::` cannot issue runner commands. Test in Task 4.

---

## File Structure

```
go.mod, go.sum
flake.nix, .envrc, .gitignore, LICENSE
action.yml
cmd/jobs-action/main.go        flags, signal context, os.Exit
checks/checks.go               Checks API client, Error, rate-limit detection
checks/event.go                HeadSHA
checks/checks_test.go, event_test.go
report/test.go                 Test, Label, CheckName, Outcome, titles, FitText
report/tracker.go              Tracker: per-test state from spec.Event
report/checks.go               Checks: open, complete, cancel, degrade
report/summary.go              Tally, Headline, Summary, WriteOutputs, ErrorCommand
report/plain.go                Plain: prefixed log lines
report/*_test.go, report/testdata/summary.golden.md
action/action.go               Config, Env, Deps, Run
action/real.go                 Main: real store, discovery, dial
action/action_test.go
.github/workflows/ci.yml, release.yml
README.md
```

---

### Task 1: Scaffold and the `checks` package

**Files:** Create `go.mod`, `flake.nix`, `.envrc`, `.gitignore`, `LICENSE`, `checks/checks.go`, `checks/event.go`, `checks/checks_test.go`, `checks/event_test.go`.

**Interfaces — Produces:**

```go
package checks

const MaxOutput = 65535

type Output struct{ Title, Summary, Text string }

type Run struct {
    Name, HeadSHA string // create only
    Status        string // "in_progress" | "completed"
    Conclusion    string // "success" | "failure" | "cancelled"; completed only
    Output        Output
}

type Client struct {
    BaseURL string       // GITHUB_API_URL, no trailing slash
    Repo    string       // owner/name
    Token   string
    HTTP    *http.Client // nil = a client with a 10s timeout
}

func (c *Client) Create(ctx context.Context, r Run) (int64, error) // POST /repos/{repo}/check-runs
func (c *Client) Update(ctx context.Context, id int64, r Run) error // PATCH /repos/{repo}/check-runs/{id}

type Error struct {
    Status      int
    RateLimited bool          // 403/429 with Retry-After or X-RateLimit-Remaining: 0
    RetryAfter  time.Duration // 0 when absent
    Message     string        // the response's "message", or the first 200 bytes of the body
}
func (e *Error) Error() string
func (e *Error) Permission() bool // Status is 401, 403 or 404, and !RateLimited

// HeadSHA is the commit a check attaches to: pull_request.head.sha from the
// payload for eventName "pull_request", otherwise sha.
func HeadSHA(eventName, eventPath, sha string) (string, error)
```

Request headers: `Authorization: Bearer <token>`, `Accept: application/vnd.github+json`, `X-GitHub-Api-Version: 2022-11-28`, `User-Agent: jobs-action`. JSON body keys: `name`, `head_sha`, `status`, `conclusion` (omitted unless completed), `output{title,summary,text}` (`text` omitted when empty). `Update` sends no `name`/`head_sha`.

- [ ] **Step 1:** `git init -b main`; write `go.mod`; copy `LICENSE` and adapt `flake.nix` (devshell only: `go`, `gh`), `.envrc`, `.gitignore` (add `/jobs-action`, `/dist/`) from `../assimilate`.
- [ ] **Step 2: Failing tests** (`httptest.Server`):
  - `TestCreateSendsRun`: asserts method `POST`, path `/repos/o/r/check-runs`, the four headers, body `{"name":"jobs / a (linux/amd64)","head_sha":"abc","status":"in_progress","output":{"title":"running","summary":"s"}}` (decoded and compared), returns id `42` from `{"id":42}`.
  - `TestUpdateCompleted`: `PATCH /repos/o/r/check-runs/42`, body has `status":"completed"`, `conclusion":"failure"`, `output.text`, and no `name` or `head_sha`.
  - `TestErrorClassification` (table): 403 bare → `Permission()`; 404 → `Permission()`; 403 + `X-RateLimit-Remaining: 0` → `RateLimited`, not `Permission()`; 429 + `Retry-After: 7` → `RateLimited`, `RetryAfter == 7s`; 500 → neither.
  - `TestHeadSHA` (table): `pull_request` with payload `{"pull_request":{"head":{"sha":"head1"}}}` → `head1`; `pull_request_target` with the same payload → the passed sha; `push`, `workflow_dispatch`, `merge_group` → the passed sha; `pull_request` with a missing file → error; with `not json` → error; with a payload lacking the sha → error.
- [ ] **Step 3:** Run `go test ./checks/` — fails to compile.
- [ ] **Step 4:** Implement.
- [ ] **Step 5:** `go test ./checks/` passes; `gofmt -l .` empty; `go vet ./...` clean.

### Task 2: `report` — tests, outcomes, tracker

**Files:** Create `report/test.go`, `report/tracker.go`, `report/test_test.go`, `report/tracker_test.go`.

**Interfaces — Consumes:** `spec.Event`, `spec.BuildState`, `api.Snapshot`, `wire.KindTestValue`, `wire.KindTestRun`.

**Produces:**

```go
package report

type Test struct{ Path, Platform string } // Path "/"-rooted, as TestDirs returns it

func (t Test) Label() string     // "services/api"; "." for "/"
func (t Test) CheckName() string // "jobs / services/api (linux/amd64)"

type Outcome struct {
    State     spec.BuildState // "" until terminal
    Cached    bool
    Duration  time.Duration // first StateBuilding to terminal; 0 if it never built
    Err       string
    RequestID string
    Tail      []string // last captured output lines, oldest first
}

func (o Outcome) Conclusion() string // done→"success", failed→"failure", else "cancelled"
func (o Outcome) Result() string     // "passed" | "already passed" | "failed" | "cancelled"
func (o Outcome) Title() string      // "passed in 41s" | "already passed (cached)" | "failed after 1m12s" | "failed" | "cancelled"

// FitText joins head and the tail lines in a fenced block within limit bytes.
// Lines are dropped from the front; when any were dropped the block starts
// with "… earlier output cut …". A single line longer than the budget is cut
// from its front. The fence is longer than any backtick run in the lines.
func FitText(head string, tail []string, limit int) string

const TextBudget = 60000
const TailLines = 200

type Tracker struct{ /* … */ }
func NewTracker(tests []Test, now func() time.Time) *Tracker
// Handle folds one event. It returns the test index and true when ev is that
// test's first terminal state. Events with Build < 0 are ignored.
func (t *Tracker) Handle(ev spec.Event) (i int, terminal bool)
func (t *Tracker) Outcome(i int) Outcome
```

Tracker rules: `KindState`/`StateBuilding` records the start once; a terminal `KindState` sets `State`, `Err = ev.Info`, `Duration`, and `Cached` (only for `done`) from the last snapshot with assimilate's rule — any node with `Cached` whose name starts with `wire.KindTestValue+"_"` or `wire.KindTestRun+"_"`. `KindSnapshot` stores the snapshot and its `RequestID`. `KindLog` appends `ev.Line`, prefixed `<node> │ ` when `ev.Node != ""`, to a ring of `TailLines`. Durations are rounded to the second and printed with `time.Duration.String()`.

- [ ] **Step 1: Failing tests:**
  - `TestLabelAndCheckName`: `/services/api` → `services/api`, `jobs / services/api (linux/amd64)`; `/` → `.`, `jobs / . (linux/arm64)`.
  - `TestTitles`: done 41s → `passed in 41s`; done cached → `already passed (cached)`; failed 72s → `failed after 1m12s`; failed with zero duration → `failed`; cancelled → `cancelled`.
  - `TestTrackerPass`: building at t0, done at t0+41s → `(0, true)`, `Duration == 41s`, `Conclusion() == "success"`.
  - `TestTrackerCachedShapes`: a snapshot with node `testvalue_00` cached → `Cached`; with `testrun_00` cached → `Cached`; with only `buildvalue_00` or `import_00` cached → not.
  - `TestTrackerFailureKeepsTail`: 250 log lines then failed with `Info: "exit 1"` → `len(Tail) == 200`, last line is line 250, node lines are prefixed, `Err == "exit 1"`.
  - `TestTrackerTerminalOnce`: a second terminal event returns `false` and does not change the outcome.
  - `TestFitTextCutsFront`: 5000 lines of 40 bytes, limit 60000 → `len(out) <= 60000`, contains the last line, contains `earlier output cut`, does not contain line 1.
  - `TestFitTextHugeLine`: one 100000-byte line → `len(out) <= 60000` and ends with that line's last 100 bytes (inside the fence).
  - `TestFitTextFence`: a tail line of three backticks → the opening fence has at least four backticks and appears exactly twice.
- [ ] **Step 2–4:** fail, implement, pass. Checkpoint as in Task 1.

### Task 3: `report` — check reporter

**Files:** Create `report/checks.go`, `report/checks_test.go`.

**Interfaces — Consumes:** `checks.Run`, `checks.Error`, `Test`, `Outcome`, `FitText`.

**Produces:**

```go
type API interface {
    Create(ctx context.Context, r checks.Run) (int64, error)
    Update(ctx context.Context, id int64, r checks.Run) error
}

type Checks struct{ /* … */ }

// NewChecks reports to api. A nil api, or an empty headSHA, means reporting is
// off and every method is a no-op. warn receives one-line warnings.
func NewChecks(api API, headSHA string, tests []Test, warn func(string)) *Checks

func (c *Checks) Open(ctx context.Context)                   // create all, in_progress; blocks until done
func (c *Checks) Complete(i int, o Outcome)                  // async; first completion per test wins
func (c *Checks) CancelOpen()                                // complete every open check as cancelled; blocks, bounded
func (c *Checks) Wait()                                      // wait for async completions
```

Behaviour: `Open` creates with a pool of 8, title `running`, summary `<label> on <platform>`. On the first create error that is `Permission()` or `RateLimited`, reporting of *new* checks turns off, `warn` is called once (permission: `cannot create check runs: the token needs "checks: write" (a personal access token cannot write checks); reporting through the job summary only`; rate limit: `GitHub rate limit reached while creating check runs; reporting through the job summary only`), and creates not yet started are skipped. Other create errors warn per check and leave that check unreported. `Complete` runs in a pool of 8: up to 4 attempts, waiting 1s, 2s, 4s between them, or `RetryAfter` capped at 10s when the error carries one; a final failure warns and is otherwise ignored. `CancelOpen` marks every open check completed first (so a later `Complete` is a no-op), then sends the `cancelled` updates with a pool of 16, one attempt each under a 4s timeout. Completed summary: `<label> on <platform>` plus ` · request <id>` when known. Text only when `State == failed`: `FitText(o.Err, o.Tail, TextBudget)`. The retry waits and timeouts are package variables so tests shrink them.

- [ ] **Step 1: Failing tests** with a fake `API` recording calls:
  - `TestOpenCreatesInProgress`: 3 tests → 3 creates, each `Status == "in_progress"`, names equal `CheckName()`, `HeadSHA == "abc"`.
  - `TestCompleteSuccessAndFailure`: completing test 0 as done and test 1 as failed with a tail → updates carry `completed`/`success` with no text, and `completed`/`failure` with text containing the error and the last tail line.
  - `TestCompleteFirstWins`: `Complete(0, done)` then `Complete(0, failed)` → one update.
  - `TestPermissionDegrades`: the first create returns `&checks.Error{Status: 403}` → exactly one warning mentioning `checks: write`; `Complete` sends nothing; no panic.
  - `TestRateLimitDegradesButCompletesCreated`: create 1 succeeds, create 2 is rate limited (pool forced to 1 for the test) → one warning; `Complete(0, …)` still updates; `Complete(1, …)` does not.
  - `TestCompleteRetries`: update fails twice with 502 then succeeds → 3 update calls, no warning.
  - `TestCompleteGivesUpQuietly`: update always fails → 4 calls, one warning, `Wait` returns.
  - `TestCancelOpen`: 3 open, one already completed → 2 `cancelled` updates; a later `Complete` on a cancelled test sends nothing.
  - `TestDisabled`: `NewChecks(nil, …)` and `NewChecks(api, "", …)` make no calls.
- [ ] **Step 2–4:** fail, implement, pass with `-race`. Checkpoint.

### Task 4: `report` — summary, outputs, plain log

**Files:** Create `report/summary.go`, `report/plain.go`, `report/summary_test.go`, `report/plain_test.go`, `report/testdata/summary.golden.md`.

**Produces:**

```go
func Tally(os []Outcome) (passed, cached, failed, cancelled int) // a non-terminal outcome counts as cancelled
func Headline(os []Outcome) string  // "3 passed (1 already passed), 1 failed[, N cancelled]"; "no tests found" for none
func Summary(tests []Test, os []Outcome) string // markdown, see spec "Job summary"
func WriteOutputs(path string, os []Outcome) error // appends passed=, cached=, failed=, cancelled= lines; "" path is a no-op
func AppendSummary(path, md string) error          // "" path is a no-op
func ErrorCommand(t Test, o Outcome) string        // "::error title=<check name>::<label>: <result>[: <first line of Err>]"

type Plain struct{ /* … */ }
func NewPlain(w io.Writer, tests []Test) *Plain
func (p *Plain) Handle(ev spec.Event)
```

`Summary`: heading `### jobs tests: <Headline>`, the table (`Test | Platform | Result | Time`; time empty for cached and for zero), then one `<details><summary><label>: <result></summary>` block per failed test holding `FitText(Err, last 50 tail lines, 16000)`; stop adding detail blocks once the document passes 500000 bytes and say `details for N more failed tests omitted`. Table cells and the `<summary>` text are HTML-escaped and have `|` escaped. `ErrorCommand` escapes `%`, `\r`, `\n` in the message and additionally `:` and `,` in the title, per the workflow-command rules. `Plain` prints `[<label>] ▸ <state>[ (<info>)]`, `[<label>] <node> │ <line>` or `[<label>] <line>`, and `[<label>] <info>` only when the info changed for that test; events with `Build < 0` print as `[jobs-action] <line>`.

- [ ] **Step 1: Failing tests:**
  - `TestTallyAndHeadline`: `[done, done cached, failed]` → `2, 1, 1, 0`, `2 passed (1 already passed), 1 failed`; with a cancelled and a non-terminal → `…, 2 cancelled`; empty → `no tests found`.
  - `TestSummaryGolden`: the spec's three-row example plus one failure with a two-line tail, compared with `testdata/summary.golden.md` (`-update` flag rewrites it).
  - `TestSummaryEscapes`: a tail line `</details>` followed by three backticks → the document contains exactly one `</details>` per failed test outside fenced blocks (assert the closing tag count equals the opening count and the hostile line sits between fence lines).
  - `TestSummaryBudget`: 100 failed tests each with a 16 KB tail → `len(doc) < 1<<20` and contains `more failed tests omitted`.
  - `TestWriteOutputs`: file gets exactly `passed=2\ncached=1\nfailed=1\ncancelled=0\n`; empty path returns nil.
  - `TestErrorCommand`: test `/a,b`, err `boom: x\nline2` → `::error title=jobs / a%2Cb (linux/amd64)::a,b: failed: boom: x`.
  - `TestPlainLinesStartWithBracket`: state, info, node log and a log line `::add-mask::x` → every output line starts with `[`.
  - `TestPlainInfoDeduped`: the same info twice prints once.
- [ ] **Step 2–4:** fail, implement, pass. Checkpoint.

### Task 5: `action` — the run

**Files:** Create `action/action.go`, `action/real.go`, `action/action_test.go`.

**Interfaces — Consumes:** everything above; `builds.Run`, `builds.Backend`, `builds.Result`, `spec.WholeTreeTest`, `jobs.Open`, `jobs.Dial`, `(*jobs.Local).TestDirs`.

**Produces:**

```go
package action

type Env struct {
    Actions                                bool // GITHUB_ACTIONS == "true"
    Token, Repository, SHA                 string
    EventName, EventPath, APIURL           string
    StepSummary, Output                    string
}
func EnvFromOS() Env

type Config struct {
    Server   string
    Addrs    []string
    Platform string
    Root     string // absolute
    DataDir  string
    List     bool
    Env      Env
    Stdout   io.Writer
    Stderr   io.Writer
}

type Deps struct {
    Discover func(ctx context.Context, root string) ([]string, error)
    Dial     func(ctx context.Context) (builds.Backend, func(), error)
    API      report.API    // nil = no check reporting
    Now      func() time.Time
    Grace    time.Duration // time allowed after cancellation; 6s in production
}

// Run is the whole command. ctx is cancelled by the first SIGINT/SIGTERM.
func Run(ctx context.Context, cfg Config, d Deps) int
// Main builds the production Deps (store at cfg.DataDir) and calls Run.
func Main(ctx context.Context, cfg Config) int
func SplitAddrs(s string) []string
```

`Run`, in order (spec "The binary", "Cancellation", "Degrading without checks"):

1. Validate `Platform` against `^[a-z0-9]+/[a-z0-9]+$`; `Root` must be an existing directory. Errors print `jobs-action: <msg>` to stderr, exit 1.
2. `Discover`; build `[]report.Test` and `[]spec.BuildSpec` with `spec.WholeTreeTest(dir, platform)` and `Name` set to the label.
3. `List`: print `<label>\t<platform>` per test; exit 0.
4. No tests: print `no tests found`; in Actions append `### jobs tests: no tests found` to the summary and write zero outputs; exit 0.
5. `Server == ""`: error `no server: set the "server" input (repository secrets are not available to pull requests from forks)`; exit 1. Then `Dial`.
6. Checks: off (with one `::warning::`) when not in Actions (silently), `d.API == nil`, the token or repository is empty, or `checks.HeadSHA` fails. Otherwise `Open`.
7. Start `builds.Run(runCtx, cfg.Root, "", specs, backend, events)` in a goroutine; `runCtx` is cancellable but not derived from `ctx`'s cancellation. One loop consumes events: scrub, `Plain.Handle`, `Tracker.Handle`, and on a terminal event `Checks.Complete`.
8. When `ctx` is cancelled: cancel `runCtx`, call `Checks.CancelOpen`, start the `Grace` timer. The loop keeps draining events. It ends when the events channel closes or the timer fires; after the timer a background goroutine keeps draining so `builds.Run` never blocks.
9. Outcomes come from the tracker; when `builds.Run` returned, each outcome's `Cached` is overwritten by `Result.Cached` and a non-terminal one takes the result's state and error. `Checks.Wait` (skipped after the timer fired).
10. Print the headline; in Actions append the summary, write the outputs, print one `ErrorCommand` per test that did not pass. Exit 1 if any outcome is not `done`.

Scrubbing: when `Server != ""`, every occurrence of it in `ev.Line`, `ev.Info`, result errors and fatal error text is replaced with `<server>` before anything is printed or reported. Warnings in Actions are `::warning::<text>`; outside Actions `warning: <text>` on stderr.

`Main`: `jobs.Open(cfg.DataDir)`; `Discover = local.TestDirs`; `Dial` wraps `jobs.Dial(ctx, local, jobs.Options{Server, Addrs})` and returns `client.Close`; `API` is a `*checks.Client` from `cfg.Env` when in Actions with a token, else nil; `Now = time.Now`; `Grace = 6s`.

- [ ] **Step 1: Failing tests** with a scripted fake `builds.Backend` (model it on assimilate's `builds/builds_test.go` fake: `Follow` plays a per-test script of sink calls and returns a state) and a recording fake `report.API`:
  - `TestRunPassCachedFail`: three dirs; scripts: pass, done with a cached `testrun_` node, failed with output → exit 1; API saw 3 creates and 3 completions (`success`, `success` titled `already passed (cached)`, `failure` with the output in text); summary file has the headline `2 passed (1 already passed), 1 failed`; outputs file `passed=2`, `cached=1`, `failed=1`, `cancelled=0`; stdout has one `::error title=jobs / …` line.
  - `TestRunAllPassExitZero`.
  - `TestRunNoTests`: exit 0, `Dial` never called, summary says `no tests found`, outputs all zero.
  - `TestRunList`: prints `a\tlinux/amd64` lines, exit 0, `Dial` never called.
  - `TestRunEmptyServer`: tests exist, `Server == ""` → exit 1, stderr mentions `forks`, `Dial` never called.
  - `TestRunBadRoot`: root is a file → exit 1, stderr names the path, `Discover` never called.
  - `TestRunBadPlatform`: `linux` → exit 1.
  - `TestRunDialFailureScrubbed`: `Dial` returns `errors.New("dial SECRETID (amber): refused")` with `Server: "SECRETID"` → exit 1, no creates, and `SECRETID` appears nowhere in stdout or stderr.
  - `TestRunScrubsEverywhere`: a failing test whose output and error contain the server ID → the ID is absent from stdout, the summary file and every API payload.
  - `TestRunHeadSHAUnreadable`: `EventName: "pull_request"`, `EventPath` missing → one warning, no API calls, verdict unchanged.
  - `TestRunPermissionDenied`: every create returns 403 → one warning, exit code still reflects the tests.
  - `TestRunCancelled`: `Follow` blocks until its ctx is done, then returns cancelled; cancel `ctx` → both checks completed `cancelled`, exit 1, headline `0 passed (0 already passed), 0 failed, 2 cancelled`.
  - `TestRunCancelHungBackend`: `Follow` and `Cancel` never return; `Grace: 50ms`; cancel `ctx` → `Run` returns 1 within a second, checks are `cancelled`, the summary is written.
  - `TestRunOutsideActions`: `Env{}` → no API calls, no files written, verdict and headline still printed.
- [ ] **Step 2–4:** fail, implement, pass with `-race`. Checkpoint.

### Task 6: Binary, action, workflows, README

**Files:** Create `cmd/jobs-action/main.go`, `action.yml`, `.github/workflows/ci.yml`, `.github/workflows/release.yml`, `README.md`.

**`cmd/jobs-action/main.go`:** `var version = "dev"`. Flags: `--server` (default `$JOBS_SERVER`), `--server-addr` (default `$JOBS_SERVER_ADDR`), `--platform` (default `linux/amd64`), `--root` (default `.`, made absolute), `--data-dir` (default `<os.UserCacheDir>/jobs-action/store`), `--list`, `--version`. Context from `signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)` whose stop function is never called, so later signals stay absorbed. `os.Exit(action.Main(ctx, cfg))`.

**`action.yml`:** composite; inputs and outputs as in the spec. Step `download` (`env: JOBS_ACTION_VERSION: v0.1.0`): map `$RUNNER_OS-$RUNNER_ARCH` (`Linux-X64` → `jobs-action-linux-amd64`, `Linux-ARM64` → `jobs-action-linux-arm64`, else `::error::` and exit 1); `curl -fsSL --retry 5 --retry-all-errors` the asset and `checksums.txt` from `https://github.com/jobs-build/jobs-action/releases/download/$JOBS_ACTION_VERSION/` into `$RUNNER_TEMP/jobs-action/bin`; `grep " $asset\$" checksums.txt | sha256sum -c -`; `chmod +x`. Step `run`: inputs arrive through `env:` only (`JOBS_SERVER`, `JOBS_SERVER_ADDR`, `GITHUB_TOKEN`, `JOBS_ACTION_PLATFORM`, `JOBS_ACTION_ROOT`), never interpolated into the script; the script is one `exec` of the binary with `--platform`, `--root` and `--data-dir "$RUNNER_TEMP/jobs-action/store"`. Outputs map to `steps.run.outputs.*`.

**`ci.yml`:** on push to `main` and pull requests; `contents: read`; setup-go from `go.mod`; gofmt check, `go vet ./...`, `go test -race ./...`, and `CGO_ENABLED=0 GOOS=linux GOARCH={amd64,arm64} go build ./cmd/jobs-action`.

**`release.yml`:** on tags `v*.*.*`; `contents: write`. Steps: checkout; fail unless the tag equals the `JOBS_ACTION_VERSION` literal in `action.yml`; `go test ./...`; build both assets into `dist/` with `CGO_ENABLED=0 -trimpath -ldflags "-s -w -X main.version=$TAG"`; `sha256sum` → `dist/checksums.txt`; `gh release create "$TAG" --verify-tag --generate-notes dist/*`; then force-move the major tag (`v0` for `v0.x.y`) to the release commit and push it.

**`README.md`:** what it does; the usage workflow from the spec; inputs and outputs; required permissions; what the PR shows; the memo (KT shared, K per checkout, a cached run still needs a live runner); assimilate template tests are different tests; fork PRs and the `if:` to skip; `pull_request` only; the server ID is a credential; scale (about 100 tests per run); supported runners; the checksum caveat; local use (`jobs-action --list`, running against a server); releasing.

- [ ] **Step 1:** Write the files.
- [ ] **Step 2:** `go build -o /tmp/…scratchpad…/jobs-action ./cmd/jobs-action && <bin> --version` prints `dev`; `<bin> --list --root <a temp tree with two TESTS.jobs>` prints two lines. Remove the binary.
- [ ] **Step 3:** `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /dev/null ./cmd/jobs-action` and the same for `arm64` succeed.
- [ ] **Step 4:** Validate `action.yml` and both workflows parse as YAML, and that the tag check in `release.yml` extracts `v0.1.0` from `action.yml` (run that shell fragment locally).

### Task 7: Whole-tree verification and review

- [ ] `gofmt -l .` empty; `go vet ./...` clean; `go test -race ./...` passes; both cross-builds pass.
- [ ] No built binaries remain in the repo or the scratchpad.
- [ ] A fresh reviewer reads the branch against the spec; fix what it finds.
- [ ] Report to Dragan: what is verified, and what is not (no real server run, no GitHub run; `action.yml` and `release.yml` are exercised only by the first release).
