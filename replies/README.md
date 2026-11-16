# Replies

One file per answer to a GitLab note, `NS3-NNN/<note-id>.md`, where
`<note-id>` is the id of the note answered, as the read workflow mirrored it
on the item's ticket (its first-line marker `gitlab-note: … <note-id>`). The
file's text is posted verbatim, plus the disclosure footer, as a note in that
note's thread, 24 h after `send.yml` schedules it, at most one note per
thread per 24 h
([ADR-0002](../docs/adr/0002-a-zero-manual-pipeline-to-ns-3s-gitlab.md)).
The same reference rules as `reports/` apply (`upstream.py lint`).
