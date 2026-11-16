# Security Policy

`swarm-routing/ns3-upstream-report` is the org's bridge to ns-3's GitLab: a stdlib Python tool and three workflows, no simulation code. This document records its
security posture honestly; it promises nothing the code does not do. The
org-wide policy is [swarm-routing/.github SECURITY.md](https://github.com/swarm-routing/.github/blob/main/SECURITY.md).

## Trust boundary

<!-- Which input is untrusted, the only code that parses it, and what that
code guarantees. A protocol: its wire-format decoder (org invariant 3). A
tool: the parsers of files it reads. If nothing parses untrusted input, say
so in one line. -->
Two inputs are untrusted. **GitLab notes** (anyone on gitlab.com can write
one) are quoted into mirror-ticket comments by `tools/upstream/upstream.py`:
it never executes them, neutralises hidden-comment markers, `@mentions` and
`#N` links before quoting, and reads its own markers only from the first line
of comments the workflow's bot wrote. **Text bound for GitLab** comes only
from files merged to `main` (`reports/`, `replies/`), never from a comment,
and passes the guards before every API call: references, the disclosure
footer, the scope allowlist (`nsnam/ns-3-dev`, the item's own threads, no
close, label, approval or merge), rate limits and fresh P1 evidence. Agents
act on GitHub as the maintainer's own user, so no actor check or approval
click is a control; the guards assume an agent wrote everything
(docs/adr/0002-a-zero-manual-pipeline-to-ns-3s-gitlab.md).

## Known, by-design limitations (out of scope)

- **A reviewed mistake can still go out.** A text that passes its pull
  request and the guards is sent when its veto window ends, unless someone
  labels the ticket `hold`.
- **The read token is readable by the read job.** `gitlab-read`'s token is
  exposed to the code on `main` that the scheduled job runs; give it
  `read_api` only.
- **The bridge does not judge content.** It checks references, scope, rate
  and evidence, not whether a report is right; P1 verify and the review of
  the pull request do that.

<!-- org-sync:begin reporting -->
## Reporting a vulnerability

Report privately through GitHub's private vulnerability reporting: the
repository's **Security** tab, then **Report a vulnerability**. If that is
not available, open an issue that only asks for a private contact, and put
no vulnerability details in it. This is a research project maintained on a
best-effort basis: expect an acknowledgement within a few days, not an SLA.
<!-- org-sync:end reporting -->

## Verifying what it ships

Nothing is released: the workflows run from `main`. Every post is announced
on its mirror ticket (an `upstream-scheduled` comment with its exact text)
and linked back after it is sent (an `upstream-sent` comment).
