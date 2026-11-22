# Licensing

Copyright (c) 2026 Daniel Joppi (https://github.com/swarm-routing).
Licensed per directory, as AntHocNet
([LICENSING.md](https://github.com/swarm-routing/AntHocNet/blob/main/LICENSING.md),
[ADR-0028](https://github.com/swarm-routing/AntHocNet/blob/main/docs/adr/0028-core-is-dual-licensed.md)).

| Path | Licence |
|------|---------|
| `cmd/`, `internal/` (the Go tool), `go.mod`, `go.sum` | `MIT OR GPL-2.0-only` |
| `internal/fake/testdata/` | not ours: responses of gitlab.com's public API and pages for nsnam/ns-3-dev #1364, !2985 and !3011, recorded 2026-11-16, kept unchanged as test input only; the notes in them belong to their authors |
| `patches/` | each series is a merge request's to nsnam/ns-3-dev, under ns-3's `GPL-2.0-only`, kept for resolution only |
| `.github/ISSUE_TEMPLATE/` forms copied from swarm-routing/.github (`forms = true`) | `MIT OR GPL-2.0-only`, as their source: org-sync keeps them byte-identical to the org's forms |
| everything else (workflows, the register, docs) | `GPL-2.0-only` |

<!-- org-sync:begin licence-texts -->
Texts: [`LICENSE`](LICENSE) (GPL-2.0) and [`LICENSE-MIT`](LICENSE-MIT) (the
MIT option). Each source file states its licence in its
`SPDX-License-Identifier` line; CI checks it
(`swarm-routing/.github` `cmd/check-headers`; locally `make headers`).
<!-- org-sync:end licence-texts -->
