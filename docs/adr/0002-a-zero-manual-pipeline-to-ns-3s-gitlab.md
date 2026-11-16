# 0002. A zero-manual pipeline to ns-3's GitLab

- **Status:** accepted; its token scopes, D3 (the push of a resolved branch) and the tool's language superseded by [0003](0003-the-bridge-is-go.md)
- **Date:** 2026-11-16
- **Issue:** #2 (decisions D1 to D5: https://github.com/swarm-routing/ns3-upstream-report/issues/2#issuecomment-6917989105)

## Context

The org reports bugs it finds in upstream ns-3 to ns-3's GitLab
(https://gitlab.com/nsnam/ns-3-dev). On 2026-11-16 every step of that was
done by hand: reading the review thread of
https://gitlab.com/nsnam/ns-3-dev/-/merge_requests/2985 to find the stall,
building ns-3-dev `master` to rerun the reproducers, writing and correcting
the texts, pasting them into GitLab, rebasing the merge request, answering
reviewers, and spotting a fix in a release. The maintainer's requirement:
"I will not do anything manually, we need to structure as a pipeline." The
maintainer's remaining actions are decisions, never mechanics.

**Identity, the real risk.** Agents act on GitHub as the maintainer's own
user. Nothing on GitHub can tell the maintainer's command from an agent's:
not the comment author, not the author association, and not an environment's
required reviewer, which an agent could approve. A design that relies on
"only the maintainer can trigger or approve this" is not a gate. (A separate
identity for agents is the proper fix, outside this repository:
swarm-routing/.github#523, decision D4.)

ns-3's reviewers asked for AI assistance to be attributed (`Assisted-by:`)
and discourage AI-written messages, on
https://gitlab.com/nsnam/ns-3-dev/-/merge_requests/2985.

## Decision

One mirror ticket per upstream item, and eight pipelines (#2):

| Pipeline | Runs | Token |
|----------|------|-------|
| P0 draft | an agent opens a PR with `reports/NS3-NNN.md` (or a reply file) | none |
| P1 verify | CI builds ns-3-dev `master` in the `ns3-dev` toolchain and runs the item's reproducer; a green run records `verified = { date, master, run }` | none |
| P2 read | every 6 h: notes, state and stall flags into the mirror ticket | `read_api` (optional) |
| P3 publish | `reports/NS3-NNN.md` as a new GitLab issue, after the window | `api` |
| P4 reply | `replies/NS3-NNN/<note-id>.md` as a note in that note's thread, after the window | `api` |
| P5 MR maintenance | the rebase API on our merge request; a conflict becomes a PR here; a review request at most once per 30 days | `api`, `write_repository` |
| P6 lifecycle | daily: a merged MR's commit in a release tag sets "fixed in ns-3.X", opens the workaround issues, closes the RCAPA ticket once they are closed | none |
| P7 credentials | rotates the token every 60 days (`POST /personal_access_tokens/self/rotate`) and stores it with the org App | the App |

**The text comes only from merged files**, never from a comment, so every
post was reviewed as a PR (CodeRabbit and the org's review gate) and passed
CI's references lint.

**Guards**, checked by `tools/upstream/upstream.py` before every API call; a
failure stops the post and comments why on the mirror ticket:

1. *References*: no bare `#N` / `!N` / `owner/repo#N` that GitLab would link;
   our items as full GitHub URLs; ns-3 items as `nsnam/ns-3-dev#N` or a full
   GitLab URL (maintainer rule 2026-11-16). The other way round, our side
   names GitLab items by full URL.
2. *Disclosure* (D2): every automated post ends with "Posted by
   swarm-routing's upstream bridge; drafted with AI assistance (Claude) and
   checked by its CI, not written by hand."; commits pushed to a merge
   request carry `Assisted-by: Claude`.
3. *Scope*: `nsnam/ns-3-dev` only; only creating issues, notes on the
   item's own issue and merge requests, merge requests from the fork, and
   rebasing our merge requests; never closing, labelling, approving or
   merging anything upstream.
4. *Rate limits*: one new issue per day, one note per thread per 24 h, one
   review request per merge request per 30 days.
5. *Evidence*: a new report needs P1 verify green on `master` from the last 7
   days.
6. *Content source*: merged files only (above).
7. *Veto window* (D1 (a)): a post is scheduled with its exact text on the
   mirror ticket and goes out 24 h later (48 h for a new issue) unless the
   ticket is labelled `hold`. Nothing needs doing for it to go out. The
   policy is code (`gate` in `tracked.toml`: `veto-window`, `auto`,
   `approve`).

**Environments**: `gitlab-read` (deployment branch `main`) and `gitlab-write`
(deployment branch `main`, no required reviewer: the veto window is the
gate, and a reviewer an agent can impersonate would add a click, not a
control). Both are declared in `.github/org-sync.toml` and applied by
`repo-settings`.

**D3**: the write token has `write_repository`, so P5 pushes resolved
branches, only to the source branches of tracked merge requests. **D5**: P7
rotates the token with org-bot's App, which needs `secrets: write` on this
repository.

### The flow

The diagrams are the README's ("How it works").

## Consequences

- The maintainer's only actions are the optional `hold` label and the
  one-time bootstrap (the two GitLab tokens, the App permission for P7).
- Everything sent is reviewed text that passed fixed guards, and every post
  is announced on its ticket a day or two ahead.
- Because every actor may be an agent, the guards assume one wrote
  everything; a mistake that passes the PR review and the guards still goes
  out unless someone holds it within the window.
- A merged file is sent once; to send something else, merge a new file.
