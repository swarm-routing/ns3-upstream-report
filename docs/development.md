# Developer guide

This repository is the org's bridge to ns-3's GitLab: one stdlib Python tool
(`tools/upstream/upstream.py`), the register `tracked.toml`, and three
workflows (read, send). A contributor (usually an agent) adds an item to
`tracked.toml`, drafts a report or a reply as a file, or changes a pipeline.

## Prerequisites

Python 3.11 or newer (the tool reads TOML with `tomllib`; CI runs 3.13 for
the tests and the runner's Python 3.12 in the workflows), `make`, and `ruff`
0.17.1 for lint, and [actionlint](https://github.com/rhysd/actionlint) 1.7.12
for the workflows (`go install github.com/rhysd/actionlint/cmd/actionlint@v1.7.12`,
or its release binary, as `ci.yml` downloads it). Docker is not needed.

## Get the code

```bash
git clone https://github.com/swarm-routing/ns3-upstream-report.git
cd ns3-upstream-report
```

`make headers` and `make org-sync-check` fetch `swarm-routing/.github` into
`~/.cache/swarm-routing/dotgithub` themselves; nothing else is needed.

## Dependencies

None: the tool uses the Python standard library only. It talks to GitHub's
REST API and to gitlab.com (`nsnam/ns-3-dev`); the self-test replaces both
with fakes fed by `tools/upstream/fixtures/`.

## Build

There is nothing to compile. `make build` validates the register:

```bash
make build    # python3 -I tools/upstream/upstream.py validate
```

## Test

```bash
make test     # python3 -I tools/upstream/upstream.py --self-test
```

The self-test covers the register's validation, the reader against the
recorded GitLab pages of https://gitlab.com/nsnam/ns-3-dev/-/issues/1364 and
merge requests 2985 and 3011 (tokenless and token mode, dedupe on a second
run, a new note, a hostile note, the stall flags, the status block, dry run),
the outbound lint, and plan and send against a fake GitLab (P1 evidence, the
veto window, `hold`, the footer, the scope allowlist, the rate limits, one
send per file). CI runs it under coverage (`ci.yml`, job `python`), and
`upstream.py lint` over `reports/` and `replies/` (job `workflows`).

## Run

A read against live GitLab, writing nothing to GitHub:

```bash
env -u GITHUB_TOKEN -u GITLAB_TOKEN python3 -I tools/upstream/upstream.py read --dry-run
```

The real runs are the workflows: `read.yml` (every 6 h, or **Run workflow**
with `dry_run`) and `send.yml` (hourly and on a merge to `reports/`,
`replies/` or `tracked.toml`). Never run `read`, `plan` or `send` without
`--dry-run` locally.

## Docker and dev containers

None: the tool needs only Python, and the repository builds no image.

## Lint and format

```bash
make lint      # ruff check and ruff format --check on tools/, with the org ruff.toml
make format    # ruff format on tools/, with the org ruff.toml
make headers   # licence headers: tools/ MIT OR GPL-2.0-only, the rest GPL-2.0-only
actionlint .github/workflows/*.yml
```

## CI map

| Workflow | What it checks | Required |
| --- | --- | --- |
| `ci.yml` | `python`: ruff and the self-test under coverage; `workflows`: `tracked.toml` validates, actionlint | yes (`python / python`, `workflows`) |
| `licence.yml` | licence headers | yes (`headers / licence`) |
| `org-sync.yml` | org-managed files match the org templates | yes (`check / org-sync`) |
| `pr-title.yml` | the PR title is a lowercase Conventional Commit | yes (`title / title`) |
| `review-gate.yml` | CodeRabbit has reviewed the PR | yes (`review / coderabbit`) |
| `dco.yml` | outside contributors' sign-off | no |
| `scorecard.yml` | OpenSSF Scorecard | no |
| `sync-labels.yml` | the org's labels | no |
| `read.yml`, `send.yml` | the bridge itself (schedule, merges) | no |

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
- **`upstream.py lint` fails on a report or reply**: the line names the file
  and the fix (a bare `#N` becomes `nsnam/ns-3-dev#N` for an ns-3 item, or the
  full https://github.com/swarm-routing/... URL for ours).
- **A ticket says "Not scheduled"**: a guard failed (no fresh P1 evidence, a
  reply to a note outside the item's threads, a reference); the comment lists
  each one. Fix it in a new PR.
- **A post stays "waiting" or "held"**: its window has not ended, or the
  ticket is labelled `hold`.
- **`GITLAB_TOKEN is not set`** in a send job: the tokens are not stored in
  the environments yet (the one-time bootstrap).
