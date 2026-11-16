# CONTEXT.md — orientation and history

Background an agent or a new contributor needs that does not belong in
`AGENTS.md`: domain notes, current state, glossary, open questions. Read the
section a task needs, not the whole file.

## Current state

2026-11-16: the maintainer decided a zero-manual design (#2, D1–D5 accepted;
ADR-0002). `read.yml` runs every 6 h without a token; `send.yml` plans and
sends merged reports and replies after the veto window, once the
maintainer has stored the GitLab tokens. P1 verify, P5 MR maintenance, P6
lifecycle and P7 rotation are being built. Tracked: NS3-003
(https://gitlab.com/nsnam/ns-3-dev/-/issues/1364,
https://gitlab.com/nsnam/ns-3-dev/-/merge_requests/2985, competing
https://gitlab.com/nsnam/ns-3-dev/-/merge_requests/3011; RCAPA ticket
swarm-routing/ns3-tools#240), and the drafts NS3-001 (swarm-routing/ns3-containers#55,
swarm-routing/ns3-tools#238) and NS3-002 (swarm-routing/ns3-tools#206,
swarm-routing/ns3-tools#239), not filed yet (swarm-routing/.github#519).

## Glossary

- **Mirror ticket**: the issue here that stands for one upstream report; its
  body holds the GitLab links and a status block, its comments the GitLab
  notes and the scheduled and sent posts.
- **RCAPA ticket**: the `[rcapa] NS3-NNN` issue in swarm-routing/ns3-tools
  that holds the root cause analysis; it and the mirror ticket link each other.
- **Veto window**: the time between a post's `upstream-scheduled` comment and
  its sending (24 h, 48 h for a new issue); the `hold` label stops it.
- **Plan / send**: the two jobs of `send.yml`; the first has no secret and
  schedules, the second holds the token and posts what is due.

## Open questions
