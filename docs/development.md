# Developer guide

This repository is the org's bridge to ns-3's GitLab: one Go tool,
`cmd/upstream` (its packages in `internal/`), the register `tracked.toml`,
and its workflows (read, send, verify, lifecycle, maintain, rotate). A
contributor (usually an agent) adds an item to `tracked.toml`, drafts a
report or a reply as a file, or changes a pipeline. The tool was a stdlib
Python script, `tools/upstream/upstream.py`, until the port of
[ADR-0003](adr/0003-the-bridge-is-go.md); it stays, with the port's
byte-identical proof, until the Go tool has run green, then goes.

## Prerequisites

Go at the version `go.mod`'s `toolchain` line names (go1.27.4; with an older
Go, `GOTOOLCHAIN=go1.27.4` downloads it), `make`, and
[actionlint](https://github.com/rhysd/actionlint) 1.7.12 for the workflows
(`go install github.com/rhysd/actionlint/cmd/actionlint@v1.7.12`, or its
release binary, as `ci.yml` downloads it). Python 3.11+ only for the old tool
and the proof, while they exist. Docker is not needed.

## Get the code

```bash
git clone https://github.com/swarm-routing/ns3-upstream-report.git
cd ns3-upstream-report
```

`make headers` and `make org-sync-check` fetch `swarm-routing/.github` into
`~/.cache/swarm-routing/dotgithub` themselves; nothing else is needed.

## Dependencies

The standard library, and two modules pinned in `go.sum` (the org's Go rule:
a third-party module only with a reason, scanned by govulncheck):
`github.com/BurntSushi/toml` (Go has no TOML reader; it reads
`tracked.toml`) and `golang.org/x/crypto` (`nacl/box`, the sealed box GitHub
wants for a secret, P7). The tool talks to GitHub's REST API and to
gitlab.com (`nsnam/ns-3-dev`); the tests replace both with fakes
(`internal/fake`) fed by `internal/fake/testdata/`.

## Build

```bash
make build    # go build -trimpath -o build/upstream ./cmd/upstream, then validates tracked.toml
```

The workflows build it the same way in each job, through the local action
`.github/actions/upstream` (actions/setup-go at the `toolchain` version,
`GOTOOLCHAIN=local`, `CGO_ENABLED=0 go build -trimpath`, no cache in a job
that holds a token).

## Test

```bash
make test     # go test ./...
```

The tests cover the register's validation, the reader against the recorded
GitLab pages of https://gitlab.com/nsnam/ns-3-dev/-/issues/1364 and merge
requests 2985 and 3011 (tokenless and token mode, every page of
`discussions.json`, dedupe on a second run, a new note, a hostile note, the
stall flags, the status block, dry run, the expiry warning), the outbound
lint, plan and send against a fake GitLab (P1 evidence, the veto window,
`hold`, the footer, the scope allowlist, the rate limits, one send per file,
a lost link-back), P1 verify and its record, P6 lifecycle, P5 maintenance
(rebase, conflict pull request, review request) and P7 rotation (the
permission check, the sealed box, a failed store), and that read, lifecycle
and verify are built without the GitLab write client and send GET only
(`cmd/upstream`'s `TestReadCannotWrite`, `TestReadPathsOnlyGET`). No test
writes to GitLab or GitHub for real. CI runs them through
`reusable-go-ci.yml` (job `go`), with coverage to Codecov.

The port's proof, while `tools/upstream/` exists:

```bash
tools/upstream/proof/proof.sh                       # the self-test's fixtures
python3 -I tools/upstream/proof/py_replay.py record   # a live world (GH_TOKEN for GitHub), as $TMPDIR/upstream-proof/world.json
cp "${TMPDIR:-/tmp}/upstream-proof/world.json" /tmp/live.json && tools/upstream/proof/proof.sh /tmp/live.json
```

Both tools run the commands of `tools/upstream/proof/commands.json` against
the same recorded world (every GET served from it, every write recorded and
never sent); `compare.py` diffs each exit status, stdout and write.

## Run

A read against live GitLab, writing nothing to GitHub:

```bash
go build -o upstream ./cmd/upstream
env -u GITHUB_TOKEN -u GITLAB_TOKEN ./upstream read --dry-run
```

The real runs are the workflows: `read.yml` (every 6 h, or **Run workflow**
with `dry_run`), `send.yml` (hourly and on a merge to `reports/`,
`replies/` or `tracked.toml`), `maintain.yml` (daily), `rotate.yml`
(weekly). Never run `read`, `plan`, `send`, `maintain` or `rotate` without
`--dry-run` locally.

## Docker and dev containers

None: the tool is one static binary, and the repository builds no image.

## Lint and format

```bash
make lint      # gofmt -l and go vet (CI adds staticcheck and govulncheck)
make format    # gofmt -w
make headers   # licence headers: tools/, cmd/, internal/ MIT OR GPL-2.0-only, the rest GPL-2.0-only
actionlint .github/workflows/*.yml
```

## CI map

| Workflow | What it checks | Required |
| --- | --- | --- |
| `ci.yml` | `go`: build, gofmt, vet, staticcheck, govulncheck, the tests with `-race` and coverage (`reusable-go-ci.yml`), `codecov`; `workflows`: `tracked.toml` validates, the outbound lint, the port's proof, actionlint; `python`: the old tool's ruff and self-test, until it is removed | yes (`go / go`, `workflows`) |
| `licence.yml` | licence headers | yes (`headers / licence`) |
| `org-sync.yml` | org-managed files match the org templates | yes (`check / org-sync`) |
| `pr-title.yml` | the PR title is a lowercase Conventional Commit | yes (`title / title`) |
| `review-gate.yml` | CodeRabbit has reviewed the PR | yes (`review / coderabbit`) |
| `dco.yml` | outside contributors' sign-off | no |
| `scorecard.yml` | OpenSSF Scorecard | no |
| `sync-labels.yml` | the org's labels | no |
| `read.yml`, `send.yml`, `verify.yml`, `lifecycle.yml`, `maintain.yml`, `rotate.yml` | the bridge itself (schedules, merges) | no |

The "yes" rows are required once swarm-routing/.github's `repo-settings`
applies this repository's `[settings]` (dispatched right after the first
merge); until then nothing gates a merge. The required checks are the ones the `main` ruleset lists (`gh api
repos/swarm-routing/ns3-upstream-report/rules/branches/main`). `main` merges
through a merge queue: enable auto-merge on a PR.

## Release

Nothing is released: the workflows run from `main`, so a merged change is
live at the next run. See [CONTRIBUTING "Cutting a release"](https://github.com/swarm-routing/.github/blob/main/CONTRIBUTING.md#cutting-a-release)
if that ever changes.

## Troubleshooting

- **`tracked.toml: …` on any command**: the register does not validate; the
  message names the item and the key.
- **`upstream lint` fails on a report or reply**: the line names the file
  and the fix (a bare `#N` becomes `nsnam/ns-3-dev#N` for an ns-3 item, or the
  full https://github.com/swarm-routing/... URL for ours).
- **A ticket says "Not scheduled"**: a guard failed (no fresh P1 evidence, a
  reply to a note outside the item's threads, a reference); the comment lists
  each one. Fix it in a new PR.
- **A post stays "waiting" or "held"**: its window has not ended, or the
  ticket is labelled `hold`.
- **`GITLAB_TOKEN is not set`** in a send job: the token is not stored in
  the environment yet (the one-time bootstrap).
- **A `[P7]` issue**: rotate could not check, rotate or store the token; the
  issue names the missing permission or the failure. read opens one 30 days
  before the token expires.
- **`TestReadCannotWrite` fails**: a package of read, lifecycle or verify
  now imports `internal/gitlabwrite`; move the write to send, maintain or
  rotate.
