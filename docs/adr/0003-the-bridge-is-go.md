# 0003. The bridge is one Go tool, and one token reads and writes

- **Status:** accepted
- **Date:** 2026-11-16
- **Issue:** #2 (the single-token decision: https://github.com/swarm-routing/ns3-upstream-report/issues/2#issuecomment-6921062335); handoff #11

## Context

The bridge's pipelines (P1–P4, P6) were one stdlib Python script,
`tools/upstream/upstream.py` (2 279 lines with its self-test), and P5 and P7
were drafted in it (closed pull request #12, branch `claude/p5-p7`). On
2026-11-16 the maintainer decided that the org's pipelines are written in Go
(swarm-routing/.github `docs/adr/0002-pipelines-are-go.md`): one command per
`cmd/<tool>`, the logic in `internal/`, `go test` as the self-test, built in
the job with `actions/setup-go` at the `toolchain` version, a port proving
byte-identical output before the workflow switches.

The same day the maintainer decided **one GitLab token, scope `api`, for
read and write**, stored as `GITLAB_TOKEN` in both `gitlab-read` and
`gitlab-write`. That supersedes ADR-0002's two tokens (`read_api` for read;
`api` + `write_repository` for write), and so D3: without
`write_repository` the bridge cannot push a resolved branch to the fork.
The decision named its compensations: read cannot write, enforced in code;
writes only through the guarded commands; a warning before the token
expires.

## Decision

**One Go tool**, `cmd/upstream` (module
`github.com/swarm-routing/ns3-upstream-report`, `toolchain go1.27.4`), with
the subcommands of the Python tool (`validate`, `lint`, `read`, `plan`,
`send`, `verify-matrix`, `verify`, `verify-record`, `lifecycle`) and two new
ones, `maintain` (P5) and `rotate` (P7). Its output, the writes it makes and
the markers it reads are the Python tool's, byte for byte: the digests of the
scheduled posts, the comments, the status block, the flags and every line
printed (`internal/py` keeps the Python semantics they depend on). The proof
(`tools/upstream/proof/`) runs both tools against the same recorded worlds
(the self-test's fixtures and the live tickets) and diffs every exit status,
stdout and write; it runs in CI until the Python tool is removed, after the
Go tool's first green scheduled run.

**Read cannot write.** `internal/gitlab` is the reader and sends GET only;
`internal/gitlabwrite` is the only GitLab writer, with the scope guard
(`nsnam/ns-3-dev` only; a new issue, a note on the item's own threads, the
rebase of the item's own merge request, the token's self-rotation; never a
close, label, approval, merge or push), and only `internal/send`,
`internal/maintain` and `internal/rotate` import it. `cmd/upstream`'s
`TestReadCannotWrite` fails if read, lifecycle or verify (or what they import)
ever does; `TestReadPathsOnlyGET` runs them with a token and asserts that
GitLab saw only GET.

**P5, `maintain` (`maintain.yml`, daily, `gitlab-write`)**, on the open merge
requests of `tracked.toml` whose author is in `our_accounts`:

- `need_rebase` → GitLab's rebase API (`PUT
  /projects/:id/merge_requests/:iid/rebase`, which needs push access to the
  source branch, checked by GitLab), announced on the mirror ticket and sent
  after the 24 h veto window, `hold` stopping it, once per head. A rebase is
  not a post: it counts in no rate limit.
- A conflict → a pull request in this repository, opened with the org bot's
  token, adding the merge request's series as GitLab serves it
  (`patches/NS3-NNN/<mr>/<head>.patch`), its commit carrying
  `Assisted-by: Claude` (D2). **The conflict stops there**: the bridge has no
  code that pushes to GitLab, and the token could not.
- No reviewer 7 days after our last note → the fixed review request (the
  disclosure footer added), a note behind the window, `hold` and the rate
  limits, at most once per merge request per 30 days.

**P7, `rotate` (`rotate.yml`, weekly, `gitlab-write`)**: first the org App's
access, read of each environment's secrets public key, then (only when a
rotation is due) a write of the current token back into both environments;
a 401/403/404 or a failed write opens an issue naming the missing permission
(**Secrets: Read and write**, granted by the maintainer on 2026-11-16) and
nothing is rotated. Once the token is 60 days old, or expires within 14 days:
`POST /personal_access_tokens/self/rotate` with a 90-day expiry, and the new
token stored, sealed with `golang.org/x/crypto/nacl/box` for each
environment's key, in both environments. Any failure opens an issue; no
token text reaches an output or an issue. **The expiry warning**: `read`,
with the token, reads `expires_at` from `GET /personal_access_tokens/self`
and opens one issue 30 days before it.

**Dependencies**: `github.com/BurntSushi/toml` (Go has no TOML reader) and
`golang.org/x/crypto` (the sealed box), both pinned in `go.sum` and scanned
by govulncheck in `reusable-go-ci.yml`.

## Consequences

- The workflows build the tool in the job (`.github/actions/upstream`), with
  no module or build cache in a job that holds a token; `verify.yml`'s jobs in
  the `ns3-dev` image, which has no Go, take the static binary as an artifact.
- The ungated scheduled read holds a token that could write; the code it
  runs cannot, and a test keeps it so. A change that needs a GitLab write
  belongs in send, maintain or rotate, behind the guards.
- D3 is unmet: a conflicting rebase ends in a pull request here for an agent
  or the maintainer; pushing the resolution to the fork needs a token with
  `write_repository` and a new decision.
- The token is short-lived from the first rotation on, and its expiry is
  watched.
- `tools/upstream/` (the Python tool, its fixtures and the proof) is removed
  in a follow-up pull request once the Go tool has run green.
