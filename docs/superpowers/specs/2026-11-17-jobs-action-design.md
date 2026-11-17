# jobs-action: run a repository's TESTS.jobs from GitHub Actions

Date: 2026-11-17
Status: approved 2026-11-17; implemented, with the review's corrections folded in

## Intent

jobs-iroh v0.36.0 runs hermetic tests: a `TESTS.jobs` next to a `BUILD.jobs`
defines `test()`, the server runs it in the build sandbox, and a pass is
recorded under KT, a key derived from what the sandbox saw. A test whose KT
already has a pass is not run again. `assimilate test` drives this for a
monorepo from a developer's terminal.

There is no way yet to get the same thing on a pull request. The wish: a
GitHub Action that runs every `TESTS.jobs` in the repository against a
jobs-iroh server and reports the result on the PR the way a CI app would,
with one named check per test, a pass or fail conclusion, and the failing
output one click away.

The action is for any repository that has `TESTS.jobs` files. It must not
need assimilate's monorepo layout.

## Goals

- `uses: jobs-build/jobs-action@v0` in a workflow finds every `TESTS.jobs`
  in the checkout, submits each to a jobs-iroh server as a test, follows
  them, and fails the step when any test did not pass.
- Each test appears on the commit as its own check run with a live status
  (in progress, then completed), a one-line result, and on failure the
  error and the tail of the failing output.
- The workflow run page gets a job summary: one headline line and a table
  with a row per test.
- Tests already passed on the server show as passed without running, and
  say so.
- The pass memo is shared between CI and developers. A test the action
  runs builds the same definition shape as `assimilate test` builds for a
  project-tree test and as `jobs-client remote-test` builds from that
  directory with the git root as context, so an unchanged test has the
  same KT and a pass recorded by one is a memo hit for the other. (K is
  not shared; see Constraints.)
- The action downloads one prebuilt Go binary from this repository's own
  GitHub release. Nothing is compiled in the caller's workflow.

## Non-goals

- Running tests on the GitHub runner itself (`jobs-client test`). A server
  is always required.
- assimilate's template tests, `assimilate-templates/`, and
  `assimilate-tests.yaml`. None of them are read. If a repository has
  them, they are ignored.
- More than one platform per invocation. The platform is one action input,
  default `linux/amd64`. A caller who wants two platforms uses a workflow
  matrix; check names carry the platform so the rows stay distinct.
- Test params. Every test is submitted with no params.
- Check runs on pull requests from forks. The built-in token is read-only
  there and repository secrets are absent. See "Degrading without checks".
- Inline diff annotations. A test has no file and line to point at.
- A sticky PR comment.
- Its own app identity. Checks appear as "github-actions".
- Building anything (`BUILD.jobs` without a test), deploying, or pulling
  the pass memo home to the runner's store.
- Caching the runner's local store between workflow runs.
- macOS and Windows runners.

## Anti-goals

- A check left "in progress" forever after the step ended. Every check the
  action created must reach a terminal conclusion on every exit path the
  process can still act on, including cancellation.
- A reporting problem that changes the verdict. A GitHub API error must
  never turn a passing run into a failing step, or hide a failing test.
- Silently skipping a test. Every `TESTS.jobs` in the ingested tree is in
  the run.
- A definition shape that differs from assimilate's project-tree test
  (whole root tree as context, the test's directory as `Dir`, no params),
  which would give the same test a different KT and lose memo hits.
- The server endpoint ID in any output the binary writes, in any
  spelling. Knowing it is what grants access to the server. The libraries
  print it as lowercase hex whatever form the secret has, and a failed
  address lookup names it in z-base-32 inside a DNS name; GitHub's own
  masking matches neither.
- A floating tag (`@v0`) that points at a commit whose binary is not yet
  downloadable.

## Constraints

- **assimilate v0.10.0** exports what the binary needs:
  `jobs.Open`, `jobs.Dial`, `(*jobs.Local).TestDirs`,
  `spec.WholeTreeTest(path, platform)`, `builds.Run`, `builds.Result`
  (`State`, `K`, `Err`, `Cached`), and the `spec.Event` stream
  (`KindState`, `KindLog`, `KindInfo`, `KindSnapshot`). assimilate pins
  jobs-iroh v0.36.0 and requires Go 1.26.5.
- `TestDirs` walks the *ingested* tree, so `.amberignore` applies and
  `.git` is never seen. Paths are "/"-rooted; "/" is the root itself.
- `builds.Run` emits the terminal `KindState` event without the cached
  flag. `Result.Cached` is only available after `Run` returns. The rule
  that derives it (`cachedTest`: a `Cached` node whose name starts with the
  testvalue or testrun kind prefix in the last snapshot) is unexported.
- **K is per checkout; KT is what is shared.** The ingested tree hashes
  uid, gid and mtime of every entry, and K is the hash of the definition
  that embeds that tree key. Every fresh checkout on a hosted runner
  therefore has a new K and F. KT's sources are normalised (uid/gid 0,
  fixed mtime), so the memo carries across checkouts. Consequences: the K
  the action sees is not comparable with a developer's; in CI a memo hit
  is always the "cached testrun" shape, reached after the server has
  evaluated and pinned the recipe on a runner, so even a fully cached run
  needs a live runner for the platform.
- **assimilate's template tests are different tests.** In an assimilate
  monorepo, a `TESTS.jobs` in a template image's path is run by
  `assimilate test` with the image's source tree and args. The action
  runs it whole-tree with no params. The two share a memo only when that
  yields the same KT; with args it does not.
- **Access to the server is knowing its endpoint ID.** jobs-iroh lets
  whoever knows the ID connect to every ALPN, and assimilate's client
  dials the admin one. The `server` input must come from a secret, which
  GitHub masks in logs, and which pull requests from forks do not get.
- jobs-iroh needs no cgo: it cross-compiles for linux/amd64 and
  linux/arm64 with `CGO_ENABLED=0`.
- **GitHub Checks API.** `POST /repos/{owner}/{repo}/check-runs` and
  `PATCH …/check-runs/{id}` with the built-in token need the caller's
  workflow to grant `checks: write`. Only GitHub Apps can write check
  runs: the built-in token and an App installation token work, a personal
  access token gets a 403. `output.summary` and `output.text` are each
  limited to 65535 characters. A job summary is limited to about 1 MiB
  per step.
- **GitHub rate limits.** The built-in token gets 1,000 REST requests per
  hour per repository, shared with every other workflow. Secondary limits
  allow about 100 concurrent requests and about 180 writes per minute. A
  rate-limit rejection is a 403 or 429 carrying `retry-after` or
  `x-ratelimit-remaining: 0`, which is how it is told apart from a
  missing permission.
- On `pull_request` events `GITHUB_SHA` is the merge commit, which is also
  what `actions/checkout` checks out. A check run must be attached to the
  PR head commit (`pull_request.head.sha` in the event payload) to show on
  the PR. On `pull_request_target`, `GITHUB_SHA` and the default checkout
  are the base branch, not the PR.
- When a workflow is cancelled or times out, the runner sends SIGINT, then
  SIGTERM about 7.5 s later, then kills the process about 2.5 s after
  that. `builds.Run`'s cancel-and-drain has no such bound: it sends its
  cancels one after another, each allowed 5 s, and only then starts a 10 s
  drain timer. With a healthy server it takes about a second; with an
  unreachable one it outlasts the kill.
- A composite action's `run:` step starts a shell. The signal goes to the
  shell, and a shell waiting on a foreground child does not forward it.
- A composite action cannot read `inputs` through `INPUT_*` environment
  variables; `action.yml` must pass each one explicitly.
- A test submitted for a platform with no live runner is rejected by the
  server at submit (`unavailable: no live runners for platform …`), and an
  accepted request fails after 5 continuous minutes without one. Either
  way the action sees an ordinary failed test with that text.

## Approaches

Three ways to build the binary were considered.

1. **On assimilate's packages (chosen).** Import `assimilate/jobs`,
   `builds` and `spec`. Discovery, ingest, push, submit, follow, cancel and
   the cached flag are existing, tested code. The new code is discovery
   glue and the GitHub reporter. The keys match `assimilate test` by
   construction. Cost: the action follows assimilate's releases and its
   jobs-iroh pin.
2. **On jobs-iroh directly.** No assimilate dependency, but it
   reimplements what assimilate's `jobs` and `builds` packages do, and any
   difference in how the definition is built yields different keys and
   lost memo hits.
3. **A GitHub mode inside assimilate.** Least code, but it puts CI
   reporting into the deploy tool, and a separate binary was asked for.

Dragan picked 1.

## Decisions made with Dragan

- Tests run on a jobs-iroh server, the way `assimilate test` runs them.
- No `assimilate-templates/`, no `assimilate-tests.yaml`.
- The platform is an action input, default `linux/amd64`.
- A separate Go binary, published as assets on this repository's releases.
- Each test is reported as a check run through the Checks API with the
  workflow's built-in token (the route Dragan pointed at; the Check runs
  section below restates everything that matters from it), plus a job
  summary.

Everything below is my proposal built on those decisions, for Dragan to
correct.

## Design

### Repository

`github.com/jobs-build/jobs-action`, public, AGPL-3.0 like assimilate.

```
action.yml                    composite action
cmd/jobs-action/main.go       the binary
<packages>                    discovery glue, GitHub reporter
flake.nix, .envrc             devshell with Go, like the sibling repos
.github/workflows/ci.yml      gofmt, vet, test on push and PR
.github/workflows/release.yml tag push: build, release, move floating tag
README.md, LICENSE
docs/superpowers/specs/       this document
```

### Action inputs and outputs

| Input | Required | Default | Meaning |
|---|---|---|---|
| `server` | yes | | jobs-iroh server endpoint ID |
| `server-addr` | no | | direct `host:port` addresses, comma-separated (`JOBS_SERVER_ADDR`) |
| `platform` | no | `linux/amd64` | platform every test runs on |
| `root` | no | `.` | project root, relative to the workspace: the tree that is ingested and searched |
| `token` | no | `${{ github.token }}` | token for the Checks API |

Outputs: `passed`, `cached`, `failed`, `cancelled` (counts; `cached` is the
part of `passed` that was a memo hit).

The caller's workflow needs `permissions: contents: read, checks: write`,
a checkout step before the action, and should set `timeout-minutes` on the
job as for any job that waits on an outside service. `token` must be the
built-in token or a GitHub App installation token.

**Scale.** The design assumes up to about 100 tests per run. Two API
writes per test keeps a 100-test run at a fifth of the hourly budget, and
100 open checks can still be closed inside the cancellation window. Above
that nothing breaks, but check reporting may hit a rate limit and turn
itself off for the run (see "Degrading without checks"). *This number is
my assumption and is the one thing in this document Dragan has been asked
to confirm.*

```yaml
jobs:
  tests:
    runs-on: ubuntu-latest
    timeout-minutes: 30
    permissions:
      contents: read
      checks: write
    steps:
      - uses: actions/checkout@v4
      - uses: jobs-build/jobs-action@v0
        with:
          server: ${{ secrets.JOBS_SERVER }}
```

### action.yml

A composite action with two steps:

1. **Download.** Map `runner.os`/`runner.arch` to an asset name
   (`jobs-action-linux-amd64`, `jobs-action-linux-arm64`); anything else
   fails with a message naming the supported runners. Download that asset
   and `checksums.txt` from
   `https://github.com/jobs-build/jobs-action/releases/download/<version>/`
   with curl and retries, verify the sha256, and make it executable under
   `$RUNNER_TEMP`.
2. **Run.** `exec` the binary (so it, not the shell, receives the
   cancellation signal), passing every input as a flag and the token as
   `GITHUB_TOKEN` in the environment.

`<version>` is a literal in `action.yml`, the exact release tag
(`v0.1.0`). That is how `@v0`, `@main` or a commit SHA all resolve to a
real asset: the ref selects an `action.yml`, and the `action.yml` names
its binary.

The checksum check guards against a corrupt or truncated download. It is
not a trust anchor, because the checksums come from the same release as
the binary. Pinning the action by commit SHA pins `action.yml` and so the
version, not the binary's bytes. The README says this.

### The binary

```
jobs-action --server <id> [--server-addr a,b] [--platform os/arch]
            [--root dir] [--data-dir dir] [--list]
```

`GITHUB_TOKEN`, `GITHUB_REPOSITORY`, `GITHUB_SHA`, `GITHUB_EVENT_NAME`,
`GITHUB_EVENT_PATH`, `GITHUB_API_URL`, `GITHUB_STEP_SUMMARY` and
`GITHUB_OUTPUT` are read from the environment. When `GITHUB_ACTIONS` is
not `true`, the GitHub reporting is off and the binary is a plain
command-line runner, which is also how it is tried out locally.

Flow:

1. Open the local store at `--data-dir`. `action.yml` passes a directory
   under `$RUNNER_TEMP`; the store is thrown away with the runner.
2. Discover: `local.TestDirs(ctx, root)`. Each directory becomes
   `spec.WholeTreeTest(dir, platform)`, in the sorted order `TestDirs`
   returns. The context is the whole root tree, `Dir` is the test's
   directory, and a root `TESTS.jobs` is a root test. This is assimilate's
   project-tree test, minus the sidecar.
3. `--list`: print one line per test and exit 0. No server needed.
4. No tests: print "no tests found", write that to the job summary, set
   the outputs to 0, exit 0. No server needed, no checks created.
5. Require `--server`. Empty is an error that says so and adds that
   secrets are not available to pull requests from forks. Then dial. A
   dial failure is a plain step failure; no checks exist yet, so none are
   orphaned.
6. Create one check run per test, status `in_progress`.
7. `builds.Run(ctx, root, "", specs, client, events)`, with one consumer
   of the event stream that feeds the plain log, the check runs, and the
   per-test log tails.
8. After `Run` returns: write the job summary and the outputs, print the
   summary line, emit one `::error` workflow command per test that did not
   pass, exit 1 if any test did not end `done`, else 0.

The context is cancelled on SIGINT and SIGTERM.

### Check runs

**Name.** `jobs / <path> (<platform>)`, where `<path>` is the test's
directory without the leading slash, and the root is shown as `.`. For
example `jobs / services/api (linux/amd64)`. Names are unique within a
run because paths are.

**Head commit.** For `pull_request` events, the `pull_request.head.sha`
from the event payload. For every other event, `GITHUB_SHA`.
`pull_request_target` is deliberately not special-cased: its default
checkout is the base branch, so attaching results to the PR head would
show green checks on code that was never tested, and the event's purpose
(secrets for fork code) is the pattern this action should not encourage
with a server credential. The README says the action reports on PRs under
`pull_request` only.

**Lifecycle.** Two writes per test.

| Moment | Check run |
|---|---|
| after the dial, before the run | created, `in_progress` |
| test `done` | `completed`, conclusion `success` |
| test `failed` | `completed`, conclusion `failure` |
| test `cancelled` | `completed`, conclusion `cancelled` |

There is no `queued` state: all tests share one source push and start
together, so a separate transition would cost a third of the API budget
to say nothing. Creates and completions go through a small bounded pool
of concurrent requests.

**Output.**

- Title: `passed in 41s`, `already passed (cached)`, `failed after 1m12s`,
  or `cancelled`. The duration is measured by the binary from the test's
  first `building` state to its terminal state.
- Summary: path, platform, and the server's request ID once a snapshot
  has carried it (what `jobs-client diagnose --request` and `watch
  --request-id` take). K is not shown: the event stream does not carry
  it before `Run` returns, and it differs per checkout anyway.
- Text, on failure only: the short error from the run, then the tail of
  the test's captured output in a code block. The tail is cut from the
  front, on a line boundary, to stay under the 65535 character limit with
  margin, and says so when it was cut.

**Cached.** The check is completed when the terminal state event arrives,
not when the whole run ends, so a long run does not hold back finished
rows. The cached flag at that moment comes from the test's last snapshot,
using the same rule as assimilate's `cachedTest`. Exporting the rule from
assimilate so it is not duplicated is a follow-up for assimilate, not a
blocker.

*Since v0.1.1.* That rule alone misses most memo hits on a long-lived
server. The server flags a node cached only when the node is new and its
output is already recorded, and a finished request keeps its nodes until it
is deleted or the server restarts. A later request for the same test joins
the finished testrun node, which carries no flag. The tracker therefore
also counts a test as already passed when its testrun node is older than
the request's own testvalue node (a node's generation starts at the
server's clock at creation, and a request creates the testvalue node first)
and was never seen in a phase other than done. The job summary and the
outputs take the flag from the tracker or from `builds.Result.Cached`,
whichever says cached. The proper fix is a per-request flag from the
server; with it this rule becomes redundant, not wrong.

**Captured output.** The reporter keeps a bounded tail of log lines per
test (`KindLog` events, with the node name as a prefix where a test's
lines come from more than one node). assimilate's follow already replays
the failing node's output on failure.

### Cancellation

The binary has about 10 seconds from the first signal until it is killed,
and `builds.Run` may not return in that time. So on the first SIGINT or
SIGTERM it:

1. cancels the run context, so `builds.Run` sends its best-effort cancels
   and drains;
2. at once, without waiting for the drain, completes every check run that
   is not yet terminal as `cancelled`;
3. starts its own deadline of about 6 seconds. When `Run` returns or the
   deadline passes, whichever is first, it writes the job summary and the
   outputs from what it knows (a test with no terminal state counts as
   cancelled) and exits 1.

The signal registration stays in place for the life of the process, so
the runner's follow-up SIGTERM is absorbed rather than fatal. (assimilate's
`signalContext` drops its registration after the first signal; do not copy
that.) A check completed in step 2 is not updated again by a later
terminal event.

### Degrading without checks

If creating a check run fails with 403 or 404 and the response is not a
rate limit (the token lacks `checks: write`, or is a personal access
token), the binary prints one `::warning` naming the cause, turns check
reporting off for the rest of the run, and carries on. A rate-limit
rejection while creating does the same with its own wording. The job
summary, the log, the `::error` lines and the exit code still report
everything.

Checks already created when reporting turns off are still completed.
The request that completes a check is retried a few times with backoff,
honouring `retry-after` up to a minute, because a check stuck "in progress"
is the worst outcome. Every failure is retried, a bare 403 included: the
same token created the check, so a 403 on its update is far more likely a
secondary rate limit than a refusal. A finished run waits at most 90
seconds for these requests, after it has written its verdict; then it
warns and exits, so that a GitHub outage cannot run a passing job into its
timeout. No GitHub API failure ever changes the exit code.

A create already in flight when the run is cancelled gets two more seconds
to finish. Aborting it could leave a check that GitHub created and whose id
nobody learned.

A pull request from a fork does not reach this path: it has no server
secret, so the step fails at "Require `--server`". A caller who wants
fork PRs to skip instead of fail puts
`if: github.event.pull_request.head.repo.full_name == github.repository`
on the job; the README shows it. The action does not skip on its own,
because a green run that tested nothing is worse than a red one.

GitHub API calls are plain `net/http` against `GITHUB_API_URL`; two
endpoints do not justify a client library.

### Job summary

Appended to `GITHUB_STEP_SUMMARY`:

```
### jobs tests: 3 passed (1 already passed), 1 failed

| Test | Platform | Result | Time |
|---|---|---|---|
| services/api | linux/amd64 | passed | 41s |
| lib/common | linux/amd64 | already passed | |
| services/worker | linux/amd64 | failed | 1m12s |

<details><summary>services/worker: failed</summary>
…error and output tail…
</details>
```

Failure details are budgeted so the whole summary stays well under the
1 MiB limit.

### Log

Standard output carries the same plain prefixed lines `assimilate test
--plain` prints (state changes, progress notes, test output), one prefix
per test, so the raw step log is readable on its own.

*Since v0.1.1* the events pass through a tidying step before they reach the
log and the checks: node names are shortened to `kind:first-8-hex-digits`,
the failure recap loses its generation number, and its "(output streamed
above)" is dropped when no output of that node arrived. The log prints text
of several lines as several prefixed lines, and reports a push in progress
every 10 seconds and at its end instead of once per object.

### Releases

Version lives in one place: the literal in `action.yml`. A release is:
bump that literal (the only change in the release commit), tag `vX.Y.Z`,
push.

The tag push starts `release.yml`, which:

1. Refuses a tag that does not equal the version in `action.yml`.
2. Builds `jobs-action-linux-amd64` and `jobs-action-linux-arm64` with
   `CGO_ENABLED=0`, `-trimpath`, and the version stamped in through
   `-ldflags`, and writes `checksums.txt`.
3. Creates the GitHub release with the three assets.
4. Only then moves the floating major tag (`v0`, later `v1`) to the
   release commit.

Step order is what keeps `@v0` from ever pointing at a version with no
binary.

## Left to the builder

- Package layout inside the module and the names of the packages.
- Whether the plain log reuses assimilate's `ui.RunPlain` behind a tee or
  is a small printer of its own. Avoiding the bubbletea dependency is a
  fair reason for the latter.
- The exact wording of titles, summary and `--list` lines.
- The size of the per-test log tail and the summary's failure budget.
- Retry counts and backoff for the completing request.
- The default `--data-dir` outside GitHub Actions.
- Whether the release workflow uses `gh` or an action to create the
  release.

## Testing expectations

- **Reporter**, against an `httptest` server standing in for the Checks
  API: create and update requests carry the right name, head SHA, status,
  conclusion and output; the text is cut to the limit on a line boundary;
  403 on the first create turns checks off with one warning and the run
  still completes; a failing update is retried and never fails the run.
- **Head SHA**: `pull_request` uses the payload's head SHA;
  `pull_request_target`, `push`, `workflow_dispatch` and `merge_group`
  use `GITHUB_SHA`.
- **Rate limit versus permission**: a 403 with rate-limit headers is
  treated as a rate limit, a bare 403 as a missing permission.
- **Server ID**: it never appears in the log, the summary or a check.
- **Orchestration**, with a fake `builds.Backend` as in assimilate's own
  tests: a pass, a cached pass (both memo shapes), a failure with output,
  a cancellation. Assert the sequence of check states, the summary, the
  outputs and the exit code.
- **Cancellation**: cancelling the context completes every open check as
  `cancelled` before the drain finishes, and a late terminal event does
  not reopen or overwrite it. With a backend whose `Cancel` and `Follow`
  never return, the binary still writes the summary and exits by its own
  deadline. A second signal does not kill it.
- **Keys**: no test asserts that the action's K equals assimilate's (it
  cannot across checkouts). The definition's shape is what is asserted:
  the specs equal `spec.WholeTreeTest` with no args.
- **Discovery**: a synthetic tree with a root test, nested tests, and an
  `.amberignore`d `TESTS.jobs` that must not appear; the specs equal
  `spec.WholeTreeTest` for each directory.
- **Summary rendering**: a golden file.
- **action.yml** cannot be exercised before a release exists. The first
  release is followed by a manual run in a real repository with a
  `TESTS.jobs` and a `JOBS_SERVER` secret, covering a pass, a rerun that
  is cached, a failure, and a cancelled workflow.
