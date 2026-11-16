# Reports

One file per upstream report, `NS3-NNN.md`, named after its item in
[`tracked.toml`](../tracked.toml). The first line is `# <GitLab title>`; the
rest is the body of the GitLab issue, in ns-3's issue template. An agent
writes it from the RCAPA document in swarm-routing/ns3-tools (P0 draft); a
merged file is the only text `send.yml` publishes, after P1 verify evidence
from the last 7 days and the 48 h veto window
([ADR-0002](../docs/adr/0002-a-zero-manual-pipeline-to-ns-3s-gitlab.md)).

References, checked by `upstream.py lint` on every PR: ns-3 items as
`nsnam/ns-3-dev#N` / `nsnam/ns-3-dev!N` or their full GitLab URL; ours as
full `https://github.com/swarm-routing/<repo>/issues/N` (or `/pull/N`) URLs;
never a bare `#N` or `!N`. Do not write the disclosure footer: it is added
when the text is sent.
