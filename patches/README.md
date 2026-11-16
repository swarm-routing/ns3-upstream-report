# Patches

A resolved series for one of our merge requests that conflicts with its
target (P5, [ADR-0002](../docs/adr/0002-a-zero-manual-pipeline-to-ns-3s-gitlab.md)):
`NS3-NNN/<merge request>/NNNN-<subject>.patch`, the output of
`git format-patch` against ns-3-dev's target branch. Every commit message ends
with `Assisted-by: Claude` (D2); `upstream.py lint` refuses one without it.
After the merge, `send.yml` announces the push on the mirror ticket, and after
the veto window applies the series on the target branch and pushes it to the
merge request's source branch in our fork, with `--force-with-lease` on the
head GitLab reported. It never pushes to nsnam/ns-3-dev itself.
