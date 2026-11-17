# Roadmap

Which release each piece of work serves, and what each release must show to
ship. Releases are cut on the maintainer's request only (swarm-routing/.github
CONTRIBUTING "Cutting a release"); the org-wide view is the
[org ROADMAP](https://github.com/swarm-routing/.github/blob/main/ROADMAP.md).
A version's scope is the set of issues labelled `release:vX.Y.Z`; the release
workflow's ladder gate refuses a bump whose version has no row here.

The bridge's workflows run `main`'s code (golden rule 12), so a merged change
is live at the next run; a release tags one such commit, so the bridge has a
citable, verifiable version (maintainer decision on
[swarm-routing/.github#636](https://github.com/swarm-routing/.github/issues/636),
2026-11-17). The phases P0–P7 are tracked in #2, not by release.

## Release ladder

| Release | Theme | Exit criteria |
|---|---|---|
| v0.1.0 | first tagged release | `release.yml` on `reusable-release.yml`, `.cz.toml` and `CITATION.cff` in place; `make check` and the `go / go` and `workflows` checks green on `main`; a dry run green; no open issue labelled `release:v0.1.0`; the maintainer asks for it |

## Non-goals

- No release per change: the workflows follow `main`, and a release is the
  citable snapshot, never what the workflows run.
