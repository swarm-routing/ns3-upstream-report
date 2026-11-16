# 0002. The deployment approval is the only gate to GitLab

- **Status:** accepted
- **Date:** 2026-11-16
- **Issue:** maintainer decision 2026-11-16 (swarm-routing/ns3-tools#246, moved here)

## Context

This repository holds a GitLab token that can write to ns-3's tracker
(`nsnam/ns-3-dev`) as the maintainer. Text sent with it speaks for the
project in an upstream community that has its own rules on AI-written
messages (nsnam/ns-3-dev!2985, where reviewers asked for `Assisted-by:`).

The org's agents act on GitHub through the maintainer's own account. A
comment an agent writes and a comment the maintainer writes have the same
author and the same `author_association` (`OWNER`). So no check inside a
workflow (the actor, the association, a label, a username allowlist) can
tell the maintainer from an agent: any such check is a noise filter, not a
security boundary.

GitHub environments give the one control an agent cannot exercise for the
maintainer: a required reviewer approving a deployment, in the GitHub UI,
for that run. The org's rule is that agents never approve a deployment.

## Decision

- The GitLab token is stored only as an **environment secret**, never as a
  repository or organization secret: `gitlab-read` (no reviewer, deployment
  branch `main` only; token scope `read_api`, optional) and `gitlab-write`
  (required reviewer: the maintainer; deployment branch `main` only; token
  scope `api`). Both are declared in `.github/org-sync.toml`
  `[settings.environments]` and applied by `repo-settings`
  (swarm-routing/.github#521).
- Every write to GitLab (`publish.yml`, `reply.yml`) runs in a `send` job in
  `gitlab-write`. Before it, a `gate` job with no secret checks the request
  and writes the exact text and its sha256 to the run summary, so the
  approver reads what will be sent. After the approval, `send` reads the
  ticket or comment again and sends only if the sha256 is unchanged.
- `prevent_self_review` stays off: the maintainer approves runs that his own
  account (or an agent acting as it) started.
- What the workflows act on is fixed by `main`: the `issue_comment` trigger
  runs the workflow file and the tool from the default branch, never from a
  pull request; only mirror tickets of items in `tracked.toml` on `main` are
  accepted; the comment is read through the API, never interpolated into a
  `run:` line; no fork-PR trigger exists. Jobs holding the token use no
  third-party action, and every action is pinned by commit SHA.
- Agents draft text as ordinary comments or in the ticket's Report text, and
  never comment `/publish` or `/upstream`, and never approve a deployment.

## Consequences

- Nothing reaches GitLab without the maintainer looking at the run and
  approving it; a stolen or misused GitHub session alone cannot send.
- The approval is the whole gate: anyone added as a reviewer of
  `gitlab-write` could send as the maintainer. The reviewer list is code, in
  `[settings]`, so a change to it is a reviewed pull request.
- The read job's token is exposed to the code on `main` it runs, unattended;
  hence `read_api` only, and the reader works without it.
- An approval request that sits too long expires (GitHub: 30 days); the
  request is made again with a new comment.
