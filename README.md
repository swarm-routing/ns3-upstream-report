# ns3-upstream-report

<!-- org-sync:begin badges -->
| Group | Badges |
| --- | --- |
| Build | [![CI](https://github.com/swarm-routing/ns3-upstream-report/actions/workflows/ci.yml/badge.svg)](https://github.com/swarm-routing/ns3-upstream-report/actions/workflows/ci.yml) |
| Release | [![Licence: GPL-2.0-only, tools/ MIT OR GPL-2.0-only](https://img.shields.io/badge/licence-GPL--2.0--only%2C%20tools%2F%20MIT%20OR%20GPL--2.0--only-blue)](LICENSING.md) |
| Trust | [![OpenSSF Scorecard](https://api.securityscorecards.dev/projects/github.com/swarm-routing/ns3-upstream-report/badge)](https://scorecard.dev/viewer/?uri=github.com/swarm-routing/ns3-upstream-report) |
| Stack | [![Python 3.11+](https://img.shields.io/badge/python-3.11%2B-blue?logo=python)](AGENTS.md) |
| Community & docs | [![Conventional Commits](https://img.shields.io/badge/Conventional%20Commits-1.0.0-yellow)](https://www.conventionalcommits.org) |
<!-- org-sync:end badges -->

The bridge between the swarm-routing org and ns-3's GitLab
([`nsnam/ns-3-dev`](https://gitlab.com/nsnam/ns-3-dev)). The org finds bugs in
upstream ns-3 while benchmarking; each gets an `NS3-NNN` id, an RCAPA (root
cause analysis) and an RCAPA ticket in
[swarm-routing/ns3-tools](https://github.com/swarm-routing/ns3-tools/blob/main/docs/ns3-bugs.md).
When one is reported upstream, its **mirror ticket** here follows the GitLab
side, and the reports and replies go out from here, each only after the
maintainer approves it.

**Status: phase 1 (2026-11-16).** The read workflow runs; publish and reply
are written and tested against a fake GitLab, and wait for the maintainer's
token in the `gitlab-write` environment.

## How it works

| Workflow | Trigger | Environment | What it does |
|----------|---------|-------------|--------------|
| [`read.yml`](.github/workflows/read.yml) | every 6 h, and on demand | `gitlab-read` (no reviewer, `main` only) | For every item of [`tracked.toml`](tracked.toml): opens its mirror ticket if it has none; for a published item fetches each GitLab issue and merge request (state, `detailed_merge_status`, reviewers, approvals, head pipeline, labels) into the ticket's status block, adds each new GitLab note as a comment (author, date, link; deduplicated by note id), and comments when the stall flags change: no activity for 14 days, `need_rebase` or conflicts, no reviewer, an unanswered question, a competing merge request. Runs without a token (GitLab's public `discussions.json` pages) |
| [`publish.yml`](.github/workflows/publish.yml) | a `/publish` comment on a mirror ticket | `gitlab-write` (the maintainer approves, `main` only) | Files the ticket's **Report text**: a new GitLab issue with its **GitLab title**, or a note on its **GitLab target**; posts the link back |
| [`reply.yml`](.github/workflows/reply.yml) | a comment whose first line is `/upstream own` or `/upstream assisted` (optionally `#N` or `!N`) | `gitlab-write` | Sends the lines after the first, verbatim, as a GitLab note on the item's `reply_to` (or the reference named); posts the link back |

`tracked.toml` is the register: one `[[item]]` per report (its `NS3-NNN`
id, title, state `draft` / `published` / `closed`, RCAPA ticket, GitLab
issue, merge requests, competing merge requests, `reply_to`, and the mirror
ticket once known). An item enters only by a reviewed pull request, and the
publish and reply workflows act only on mirror tickets of items listed on
`main`. Each mirror ticket links its RCAPA ticket in swarm-routing/ns3-tools,
and the RCAPA ticket links back.

To report a new bug: open the RCAPA ticket in swarm-routing/ns3-tools first,
then a ticket here from the **Upstream report** form, add the item (with
`mirror = <ticket>`) to `tracked.toml` in a pull request, put the final text
in the ticket, and comment `/publish`.

## Security

- **The deployment approval is the only gate**
  ([ADR-0002](docs/adr/0002-the-deployment-approval-is-the-gate.md)). The
  org's agents act on GitHub as the maintainer's own account, so a check on
  who commented cannot tell the maintainer from an agent; the workflows'
  author check (owner, member, collaborator) only keeps strangers' comments
  from starting runs. Nothing reaches GitLab until the maintainer approves
  the `gitlab-write` deployment, after reading the exact text and its sha256
  in the gate job's summary; the send job reads the text again and refuses if
  it changed. Agents never approve a deployment.
- **Agents never send.** An agent may draft a report in a ticket or a reply
  as an ordinary comment; it never comments `/publish` or `/upstream`. Only
  the maintainer turns a draft into a send.
- **Disclosure** ([ADR-0003](docs/adr/0003-disclosure-on-relayed-text.md)).
  ns-3's reviewers asked for AI assistance to be attributed
  ([nsnam/ns-3-dev!2985](https://gitlab.com/nsnam/ns-3-dev/-/merge_requests/2985)).
  Every relayed text states who wrote it (the form's *Who wrote the report
  text*; `/upstream own` or `/upstream assisted`, a bare `/upstream` is
  refused), and the assisted ones end with an `Assisted-by:` line.
- **The token lives in the environments**, never in a repository secret:
  `gitlab-read` (scope `read_api`, optional) and `gitlab-write` (scope
  `api`). Both environments only deploy from `main`; both are code, in
  `.github/org-sync.toml` `[settings.environments]`.
- **No untrusted code runs with the token.** The comment-triggered workflows
  run from `main` (never from a pull request; there is no fork-PR trigger),
  read the comment through the API instead of interpolating it into a
  script, and give each job the least permissions (`issues: write` at most).
  Jobs holding the token use no third-party action; every action is pinned
  by commit SHA. GitLab notes quoted here are neutralised (no hidden markers,
  no `@mentions`, no `#N` links).

See [SECURITY.md](SECURITY.md) for the trust boundary and how to report a
vulnerability.

<!-- org-sync:begin develop -->
## Develop

New here? [docs/development.md](docs/development.md) is the developer guide:
the tools to install, how to get the code and its dependencies, how to
build, test, run and lint it, the Docker images, what CI checks and how to
read a red run. How changes are proposed, reviewed and released is the org's
[CONTRIBUTING.md](https://github.com/swarm-routing/.github/blob/main/CONTRIBUTING.md).
<!-- org-sync:end develop -->

## Contributing

Contributions from forks are welcome: see
[Outside contributions](https://github.com/swarm-routing/.github/blob/main/CONTRIBUTING.md#outside-contributions)
(sign off every commit with `git commit -s`).

## Licence

See [LICENSING.md](LICENSING.md).
