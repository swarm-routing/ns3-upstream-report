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
`#N` links before quoting, and reads its own dedupe markers only from the
first line of comments the workflow's bot account wrote. **Issue comments**
(anyone with a GitHub account can comment on a public issue) trigger the
publish and reply workflows: those run the tool from `main` only (never code
from a pull request), act only on the mirror tickets of items in
`tracked.toml` on `main`, read the comment through the API (never through an
interpolated `${{ }}` expression), and send nothing to GitLab before the
maintainer approves the `gitlab-write` deployment, after which the text is
read again and must match the sha256 shown for approval. The author check
(owner, member, collaborator) only filters noise: agents act on GitHub as the
maintainer's own account, so the deployment approval is the gate
(docs/adr/0002-the-deployment-approval-is-the-gate.md).

## Known, by-design limitations (out of scope)

- **The approval is the only gate.** Whoever can approve a `gitlab-write`
  deployment can send text to ns-3 as the maintainer. Only the maintainer is
  a reviewer, and agents never approve a deployment (org rule).
- **The read token is readable by the read job.** `gitlab-read`'s token is
  exposed to the code on `main` that the scheduled job runs; give it
  `read_api` only.
- **Text is sent as written.** The bridge does not check what a report or a
  reply says; the approver reads it in the gate job's summary first.

<!-- org-sync:begin reporting -->
## Reporting a vulnerability

Report privately through GitHub's private vulnerability reporting: the
repository's **Security** tab, then **Report a vulnerability**. If that is
not available, open an issue that only asks for a private contact, and put
no vulnerability details in it. This is a research project maintained on a
best-effort basis: expect an acknowledgement within a few days, not an SLA.
<!-- org-sync:end reporting -->

## Verifying what it ships

Nothing is released: the workflows run from `main`. A sent note or issue is
linked back on its mirror ticket, in a comment whose first line is an
`upstream-sent` marker, and the run that sent it shows the approval.
