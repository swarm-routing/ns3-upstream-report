# Developer guide

This repository is the org's bridge to ns-3's GitLab: one stdlib Python tool
(`tools/upstream/upstream.py`), the register `tracked.toml`, and three
workflows (read, publish, reply). A contributor usually adds an item to
`tracked.toml`, or changes what the reader mirrors or flags.

## Prerequisites

Python 3.11 or newer (the tool reads TOML with `tomllib`; CI runs 3.13 for
the tests and the runner's Python 3.12 in the workflows), `make`, and `ruff`
0.17.1 for lint. Docker is not needed.

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
recorded GitLab pages of nsnam/ns-3-dev#1364, !2985 and !3011 (tokenless and
token mode, dedupe on a second run, a new note, a hostile note, the stall
flags, the status block, dry run), and the gate, publish and reply paths
against a fake GitLab (refusals, the sha256 check, the disclosure line, one
send per comment). CI runs it under coverage (`ci.yml`, job `python`).

## Run

A read against live GitLab, writing nothing to GitHub:

```bash
env -u GITHUB_TOKEN -u GITLAB_TOKEN python3 -I tools/upstream/upstream.py read --dry-run
```

The real runs are the workflows: `read.yml` (every 6 h, or **Run workflow**
with `dry_run`), `publish.yml` and `reply.yml` (a `/publish` or
`/upstream own|assisted` comment on a mirror ticket, then the maintainer's
approval of the `gitlab-write` deployment). Never run `read` without
`--dry-run`, `publish` or `reply` locally.

## Docker and dev containers

None: the tool needs only Python, and the repository builds no image.

## Lint and format

```bash
make lint      # ruff check tools && ruff format --check tools
make format    # ruff format tools
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
| `read.yml`, `publish.yml`, `reply.yml` | the bridge itself (schedule, issue comments) | no |

The required checks are the ones the `main` ruleset lists (`gh api
repos/swarm-routing/ns3-upstream-report/rules/branches/main`). `main` merges
through a merge queue: enable auto-merge on a PR.

## Release

Nothing is released: the workflows run from `main`, so a merged change is
live at the next run. See [CONTRIBUTING "Cutting a release"](https://github.com/swarm-routing/.github/blob/main/CONTRIBUTING.md#cutting-a-release)
if that ever changes.

## Troubleshooting

- **`tracked.toml: …` on any command**: the register does not validate; the
  message names the item and the key.
- **The gate job says "Not sent" in its summary**: the reason follows (not a
  tracked mirror ticket, the draft placeholder still in Report text, a bare
  `/upstream`, already sent). Fix it and comment again.
- **The send job fails with "changed since the gate"**: the ticket or the
  comment was edited after the approval was requested. Comment again; the new
  run shows the current text.
- **The send job waits**: it needs the maintainer's approval of the
  `gitlab-write` deployment (Actions run page, **Review deployments**).
- **`GITLAB_TOKEN is not set`** in a send job: the maintainer has not stored
  the token in the `gitlab-write` environment yet.
