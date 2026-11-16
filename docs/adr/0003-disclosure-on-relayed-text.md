# 0003. Relayed text says whether an AI assistant drafted it

- **Status:** accepted
- **Date:** 2026-11-16
- **Issue:** maintainer decision 2026-11-16 (swarm-routing/ns3-tools#246, moved here)

## Context

ns-3's reviewers asked, on nsnam/ns-3-dev!2985, for AI assistance to be
attributed (`Assisted-by:` on commits) and discourage AI-written messages.
Reports and replies relayed from here are often drafted by an agent and then
read by the maintainer. A disclosure that depends on remembering to add it
by hand will be forgotten.

## Decision

The author is a required choice on every relayed text, never a default:

- **Report** (`/publish`): the issue form's required dropdown *Who wrote the
  report text*: `I wrote it` or `An AI assistant drafted it; I reviewed it`.
  Tickets the read workflow opens default to the second.
- **Reply** (`/upstream`): the first line is `/upstream own` or
  `/upstream assisted` (optionally followed by the target, `#N` or `!N`); a
  bare `/upstream` is refused with the reason.

For the assisted choice, `tools/upstream/upstream.py` appends, after a
`---` rule, the line `DISCLOSURE`

> Assisted-by: an AI assistant (Claude Code) drafted this text; I reviewed it before sending.

The appended line is part of the text whose sha256 the approver sees in the
gate job's summary.

## Consequences

- Every relayed text carries the maintainer's explicit statement of who wrote
  it, and the assisted ones say so upstream in the form ns-3 asked for.
- Changing the wording is a change to `DISCLOSURE` and this ADR.
- The bridge cannot tell whether the statement is true; the choice is the
  maintainer's, at the moment of sending.
