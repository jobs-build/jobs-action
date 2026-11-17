# jobs-action

A GitHub Action that runs every `TESTS.jobs` in a repository on a
[jobs-iroh](https://github.com/jobs-build/jobs-iroh) server and reports each
test on the commit as its own check.

```
✓ jobs / services/api (linux/amd64)      passed in 41s
✓ jobs / lib/common (linux/amd64)        already passed (cached)
✗ jobs / services/worker (linux/amd64)   failed after 1m12s
```

A failed check holds the error and the end of the test's output. The workflow
run page gets a summary table with the same rows.

## Usage

```yaml
on:
  push:
    branches: [main]
  pull_request:

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

The action needs the repository checked out before it runs, and the job needs
`checks: write` for the per-test checks.

### Inputs

| Input | Required | Default | |
|---|---|---|---|
| `server` | yes | | jobs-iroh server endpoint ID. Pass it from a secret (see [The server ID is a credential](#the-server-id-is-a-credential)). |
| `server-addr` | no | | Direct `host:port` addresses of the server, comma-separated. |
| `platform` | no | `linux/amd64` | Platform every test runs on. |
| `root` | no | `.` | Project root, relative to the workspace: the tree that is ingested and searched. |
| `token` | no | the workflow's token | Token for the Checks API. Must be the workflow's token or a GitHub App installation token; GitHub does not let a personal access token write checks. |

To test on two platforms, use a matrix. The platform is part of each check's
name, so the rows stay apart:

```yaml
    strategy:
      matrix:
        platform: [linux/amd64, linux/arm64]
    steps:
      - uses: actions/checkout@v4
      - uses: jobs-build/jobs-action@v0
        with:
          server: ${{ secrets.JOBS_SERVER }}
          platform: ${{ matrix.platform }}
```

### Outputs

`passed`, `cached`, `failed` and `cancelled`: counts of tests. `cached` is the
part of `passed` that the server had already passed and did not run.

The step fails when any test did not pass. It succeeds when none was found.

## What runs

Every directory in the checkout that holds a file named `TESTS.jobs` is one
test, run on the input platform with no params. The whole checkout is the
test's context and the directory is where it runs, so references to sibling
directories resolve. This is what `jobs-client remote-test` does from that
directory with the repository root as context, and what `assimilate test` does
for a project-tree test.

Discovery walks the tree jobs-iroh would ingest: `.amberignore` applies and
`.git` is never seen. A `TESTS.jobs` under an ignored directory is not a test.

`assimilate-templates/` and `assimilate-tests.yaml` are not read.

## What does not run again

jobs-iroh records a pass under a key derived from what the test's sandbox
saw: the script, the environment, the sources the test covers, the platform
and the content of every input. A test with a recorded pass is not run again;
its check says `already passed (cached)`.

So a commit that touches nothing a test covers re-runs nothing, and the push
to `main` after a merge finds the tests of the merge result already passed.
The record is shared with developers: a pass from `jobs-client remote-test`
or `assimilate test` on the same sources is a memo hit here, and the other way
round.

Two things to know:

- Even a fully cached run needs a live runner for the platform. The server
  evaluates and pins each recipe before it can tell that the test has already
  passed.
- In an assimilate monorepo, `assimilate test` runs a `TESTS.jobs` that sits
  in a deployed image's directory with that image's sources and args. The
  action runs it with the whole tree and no params. With args those are two
  different tests and do not share a record.

## Pull requests

On `pull_request` events the checks are attached to the pull request's head
commit, and the tests run on what `actions/checkout` checked out, which by
default is the merge of the pull request into its base.

**Forks.** A pull request from a fork gets neither the repository's secrets
nor a token that can write checks. The step fails, saying that no server was
given. To skip such pull requests instead:

```yaml
jobs:
  tests:
    if: github.event.pull_request.head.repo.full_name == github.repository || github.event_name != 'pull_request'
```

The action does not skip on its own: a green run that tested nothing would be
worse than a red one.

**`pull_request_target`** is not supported for reporting on pull requests.
Its default checkout is the base branch, so results would describe code the
pull request did not change, and the event exists to hand secrets to fork
code.

## The server ID is a credential

jobs-iroh lets anyone who knows a server's endpoint ID connect to it,
including to its admin interface. Store the ID as a secret. Anyone who can
push a workflow to the repository can read its secrets.

The action never prints the ID: it replaces it with `<server>` in its log,
the job summary and the checks, and GitHub masks secrets in logs as well.

## Without `checks: write`

If the token cannot write checks, the action warns once and carries on. The
job summary, the step log, an error annotation per failed test and the step's
exit code still report everything. The same happens if GitHub's rate limit is
reached while the checks are being created.

No GitHub API failure ever changes the step's result.

## Limits

- **Scale.** Built for up to about 100 tests per run. Each test costs two API
  writes, against a budget of 1,000 requests an hour per repository. Above
  that the action still runs every test, but check reporting may hit the rate
  limit and fall back to the job summary.
- **Runners.** Linux x64 and arm64. The runner's architecture has nothing to
  do with `platform`: tests run on the jobs-iroh server's runners.
- **Cancellation.** When a workflow is cancelled, the action cancels its
  requests on the server and marks its open checks cancelled. GitHub allows
  about ten seconds for this.
- **One platform per step**, no test params, no checks on fork pull requests.

## How the binary gets there

The action downloads one static binary from this repository's release and
runs it. `action.yml` names the exact release, so `@v0`, a version tag and a
commit SHA all resolve to a specific binary.

The download is checked against the release's `checksums.txt`. That catches a
corrupt download; it is not a trust anchor, because the checksums come from
the same release. Pinning the action to a commit SHA pins the version it
downloads, not the binary's bytes.

## Running it by hand

```sh
go run ./cmd/jobs-action --list --root ~/src/myrepo        # what would run; no server needed
JOBS_SERVER=<endpoint-id> go run ./cmd/jobs-action --root ~/src/myrepo
```

Outside GitHub Actions the binary prints the same log and exits with the same
code, and writes no checks, summary or outputs.

## Development

```sh
direnv allow            # or: nix develop
go test -race ./...
gofmt -l .              # must print nothing
```

The binary is built on [assimilate](https://github.com/jobs-build/assimilate)'s
`jobs`, `builds` and `spec` packages, which do the ingest, push, submit,
follow and cancel. What lives here is the reporting: `checks` (the Checks
API), `report` (per-test state, check runs, summary, log) and `action` (the
command).

### Releasing

1. Set `JOBS_ACTION_VERSION` in `action.yml` to the new version. That is the
   only change in the release commit.
2. Tag the commit `vX.Y.Z` and push the commit and the tag.

The tag starts the release workflow. It refuses a tag that does not match
`action.yml`, builds the two binaries, publishes the GitHub release, and only
then moves the floating major tag (`v0`), so that `@v0` never points at a
version whose binary is not there yet.

## Licence

AGPL-3.0. See [LICENSE](LICENSE).
