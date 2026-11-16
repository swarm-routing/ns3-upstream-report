# Licensing

Copyright (c) 2026 Daniel Joppi (https://github.com/swarm-routing).
Licensed per directory, as AntHocNet
([LICENSING.md](https://github.com/swarm-routing/AntHocNet/blob/main/LICENSING.md),
[ADR-0028](https://github.com/swarm-routing/AntHocNet/blob/main/docs/adr/0028-core-is-dual-licensed.md)).

| Path | Licence |
|------|---------|
| `tools/` | `MIT OR GPL-2.0-only` |
| `tools/upstream/fixtures/` | not ours: responses of gitlab.com's public API and pages for nsnam/ns-3-dev #1364, !2985 and !3011, recorded 2026-11-16, kept unchanged as test input only; the notes in them belong to their authors |
| everything else (workflows, the register, docs) | `GPL-2.0-only` |

`MIT OR GPL-2.0-only` means you may use that code under either licence, at
your option ([LICENSE-MIT](LICENSE-MIT) or [LICENSE](LICENSE)). Every source
file carries its SPDX identifier; CI checks it (`reusable-licence-check.yml`).
