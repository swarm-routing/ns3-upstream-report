# 0002. A zero-manual pipeline to ns-3's GitLab

- **Status:** accepted; its token scopes, D3 (the push of a resolved branch) and the tool's language superseded by [0003](0003-the-bridge-is-go.md); amended 2026-11-22: no merge requests of ours to ns-3 (below)
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

## Amendment (2026-11-22): no merge requests of ours to ns-3

Maintainer decision, 2026-11-22: "We are not planning to work in a MR on
ns3 and we should close our MR and let another one to do the fix."

The org does not author or maintain merge requests to `nsnam/ns-3-dev`. It
reports bugs, tests fixes against its reproducers, and follows other
people's fixes to a release. The maintainer closed our
https://gitlab.com/nsnam/ns-3-dev/-/merge_requests/2985 (NS3-003,
https://gitlab.com/nsnam/ns-3-dev/-/issues/1364) on GitLab at
2026-11-22T19:30:43Z. The bridge itself still never closes anything
upstream (guard 3). `tracked.toml` lists NS3-003 with `issue = 1364` and
`competing = [3011]` only.

What this changes, and what it does not:

- **The record.** The four commits of !2985 remain readable on GitLab:
  `e19b518f` (do not send queued packets when the next-hop lookup fails),
  `13397fca` (the advertisement root-cause fix: advertise the address of
  the interface a message is sent from), `70399d9e` (invoke the error
  callback when dropping queued packets) and `aead3416` (a regression test
  for a multi-interface chain). The competing
  https://gitlab.com/nsnam/ns-3-dev/-/merge_requests/3011 has only the
  guard. If the other three matter to ns-3, our part is to say so in a
  reply on the issue, never to reopen a merge request of ours.
- **P5 MR maintenance** (`maintain.yml`, `internal/maintain`) stays in the
  code. It works on an item's `merge_requests` only, and no item lists any,
  so it has nothing to do: each run is a no-op. It is not disabled. A
  disabled workflow is a state outside the repository that someone has to
  remember, and a register entry with `merge_requests` would be the visible
  change that brings it back, through review. Guard 3's "merge requests
  from the fork, and rebasing our merge requests" stays code with nothing
  to act on.
- **P6 lifecycle** follows `competing` merge requests and the issue's
  closing merge request to a release (#55), so a fix by someone else still
  sets "fixed in ns-3.X", opens the workaround issues and closes the RCAPA
  ticket.
- **Replies** (P4) post only on the item's own issue and merge requests;
  with no merge request of ours, that is the issue. A competing merge
  request is never a thread we write to.
