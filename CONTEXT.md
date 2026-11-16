# CONTEXT.md — orientation and history

Background an agent or a new contributor needs that does not belong in
`AGENTS.md`: domain notes, current state, glossary, open questions. Read the
section a task needs, not the whole file.

## Current state

Phase 1 (2026-11-16): `read.yml` runs every 6 h without a token; the
publish and reply workflows are written and tested against a fake GitLab and
wait for the maintainer's `GITLAB_TOKEN` in `gitlab-write`. Tracked: NS3-003
(nsnam/ns-3-dev#1364, MR !2985, competing !3011; RCAPA ticket
swarm-routing/ns3-tools#240), and the drafts NS3-001
(swarm-routing/ns3-containers#55, swarm-routing/ns3-tools#238) and NS3-002
(swarm-routing/ns3-tools#206, swarm-routing/ns3-tools#239), not filed yet
(swarm-routing/.github#519).

## Glossary

- **Mirror ticket**: the issue here that stands for one upstream report; its
  body holds the report text and a status block, its comments the GitLab
  notes.
- **RCAPA ticket**: the `[rcapa] NS3-NNN` issue in swarm-routing/ns3-tools
  that holds the root cause analysis; it and the mirror ticket link each other.
- **Gate / send**: the two jobs of `publish.yml` and `reply.yml`; the first
  has no secret and shows the text, the second holds the token and needs the
  approval.

## Open questions
