# Patches

A merge request of ours that conflicts with its target (P5,
[ADR-0003](../docs/adr/0003-the-bridge-is-go.md)): `maintain.yml` opens a
pull request adding `NS3-NNN/<merge request>/<head>.patch`, the merge
request's series as GitLab serves it, for an agent to resolve here. Every
commit keeps `Assisted-by: Claude` (D2).

The bridge pushes nothing to GitLab: the token has scope `api` only, without
`write_repository` (the maintainer's decision of 2026-11-16), so a conflict
stops at that pull request by design.
