// Copyright (c) 2026 Daniel Joppi (https://github.com/swarm-routing)
// SPDX-License-Identifier: MIT OR GPL-2.0-only

// Package maintain is P5, MR maintenance, on the open merge requests of tracked.toml
// whose author is one of our accounts:
//
//   - need_rebase: GitLab's rebase API (`PUT .../merge_requests/:iid/rebase`; GitLab
//     checks push access to the source branch), announced on the mirror ticket and
//     sent after the veto window like a note (`hold` stops it), once per head;
//   - a conflict: a pull request here (org bot's token) with the merge request's
//     series as `patches/NS3-NNN/<mr>/<head>.patch`, its commit carrying
//     `Assisted-by: Claude`, for an agent to resolve. The GitLab token has scope
//     `api` only (no `write_repository`, the maintainer's decision of 2026-11-16), so
//     the bridge never pushes to GitLab: a conflict stops at that pull request;
//   - no reviewer 7 days after our last note: the fixed ReviewRequest note (with the
//     disclosure footer), at most once per merge request per 30 days, through the
//     window, the rate limits and `hold`.
package maintain

import (
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/swarm-routing/ns3-upstream-report/internal/bridge"
	"github.com/swarm-routing/ns3-upstream-report/internal/github"
	"github.com/swarm-routing/ns3-upstream-report/internal/gitlab"
	"github.com/swarm-routing/ns3-upstream-report/internal/gitlabwrite"
	"github.com/swarm-routing/ns3-upstream-report/internal/py"
	"github.com/swarm-routing/ns3-upstream-report/internal/register"
	"github.com/swarm-routing/ns3-upstream-report/internal/send"
)

// ReviewRequest is the fixed text of a review request (the footer is added on sending).
const ReviewRequest = "Friendly ping: this merge request has no reviewer yet. Our last answer above is a week " +
	"old; could someone from the maintainers of this module take a look when there is time?"

// Limits of ADR-0002 guard 4.
const (
	ReviewAfter = 7 * 24 * time.Hour
	ReviewEvery = 30 * 24 * time.Hour
)

type conflict struct {
	it     *register.Item
	ticket map[string]any
	mr     int
	sha    string
	held   bool
	done   bool
}

// Maintainer runs P5. Bot opens pull requests here (nil: a conflict waits for the token).
type Maintainer struct {
	Cfg  *register.Config
	GH   *github.Client
	GW   *gitlabwrite.Client
	Bot  *github.Client
	Root string
	Now  time.Time

	conflicts []conflict
}

func (m *Maintainer) candidates(it *register.Item, ticket map[string]any, comments []map[string]any) ([]*bridge.Candidate, error) {
	var out []*bridge.Candidate
	sent := bridge.SentMarkers(m.Cfg, comments)
	maintained := map[string]bool{}
	for _, c := range comments {
		if v, ok := bridge.BotMarker(m.Cfg, c, "upstream-maintained"); ok {
			k, _, _ := strings.Cut(v, " -> ")
			maintained[k] = true
		}
	}
	for _, mr := range it.MergeRequests { // never it.Withdrawn: a withdrawn merge request is not maintained
		st, err := m.GW.R.Item("!", mr)
		if err != nil {
			return nil, err
		}
		author, _ := st["author"].(map[string]any)
		if py.Str(st["state"]) != "opened" || !m.Cfg.Ours(py.Str(author["username"])) {
			continue
		}
		sha := py.Head(py.Str(st["sha"]), 12)
		if py.Truthy(st["has_conflicts"]) {
			key := fmt.Sprintf("conflict %s/!%d/%s", it.ID, mr, sha)
			m.conflicts = append(m.conflicts, conflict{it, ticket, mr, sha, bridge.Held(ticket), maintained[key]})
		} else if py.Str(st["detailed_merge_status"]) == "need_rebase" {
			out = append(out, &bridge.Candidate{
				Key: fmt.Sprintf("rebase %s/!%d/%s", it.ID, mr, sha), Kind: "!", Target: mr, Action: "rebase",
				Text: fmt.Sprintf("GitLab's rebase of !%d on its target branch (head `%s`).", mr, sha),
				SHA:  bridge.Digest("rebase", mr, sha),
			})
		}
		if py.Truthy(st["reviewers"]) {
			continue
		}
		web := py.Str(st["web_url"])
		if web == "" || web == "None" {
			web = gitlab.WebURL(m.Cfg.GitLabProject, "!", mr)
		}
		notes, err := m.GW.R.Notes("!", mr, web)
		if err != nil {
			return nil, err
		}
		var last *gitlab.Note
		for i := range notes {
			if !notes[i].System {
				last = &notes[i]
			}
		}
		if last == nil || !m.Cfg.Ours(last.Author) {
			continue
		}
		at, err := bridge.ParseTime(last.CreatedAt)
		if err != nil || m.Now.Sub(at) <= ReviewAfter {
			continue
		}
		recent := false
		for _, s := range sent {
			if strings.HasPrefix(s.Value, fmt.Sprintf("review %s/!%d/", it.ID, mr)) && s.When != "" {
				if w, err := bridge.ParseTime(s.When); err == nil && m.Now.Sub(w) < ReviewEvery {
					recent = true
				}
			}
		}
		if recent {
			continue
		}
		body := bridge.Outbound(ReviewRequest)
		out = append(out, &bridge.Candidate{
			Key: fmt.Sprintf("review %s/!%d/%s", it.ID, mr, last.ID), Kind: "!", Target: mr, Action: "note",
			Text: body, SHA: bridge.Digest("review", mr, body), Problems: bridge.LintOutbound(body, m.Cfg.GitLabProject),
		})
	}
	done := bridge.SentKeys(m.Cfg, comments)
	var keep []*bridge.Candidate
	for _, c := range out {
		if !done[c.Key] {
			keep = append(keep, c)
		}
	}
	return keep, nil
}

// Run is one P5 pass: schedule, send what is due, open the conflict pull requests.
func (m *Maintainer) Run() ([]string, error) {
	rows, history, err := bridge.Plan(m.Cfg, m.GH, m.Root, m.Now, m.candidates)
	if err != nil {
		return nil, err
	}
	lines, _, err := bridge.Announce(m.Cfg, m.GH, rows, m.Now)
	if err != nil {
		return nil, err
	}
	proj := m.Cfg.GitLabProject
	for _, r := range rows {
		if r.Status != "ready" {
			continue
		}
		if why := bridge.RateLimited(r.C, history, m.Now); why != "" {
			lines = append(lines, fmt.Sprintf("%s: waits (%s)", r.C.Key, why))
			continue
		}
		if r.C.Action == "rebase" {
			send.Allow(m.GW, r.Item)
			if err := m.GW.Rebase(r.C.Target); err != nil {
				return nil, err
			}
			url := gitlab.WebURL(proj, "!", r.C.Target)
			if _, err := m.GH.AddComment(bridge.Number(r.Ticket), fmt.Sprintf("<!-- upstream-sent: %s -> %s!%d rebase -->\nAsked GitLab to rebase [%s!%d](%s); P1 verify re-runs on the new head.%s",
				r.C.Key, proj, r.C.Target, proj, r.C.Target, url, bridge.Footer)); err != nil {
				return nil, err
			}
			lines = append(lines, r.C.Key+": rebase requested")
			continue
		}
		l, err := send.Post(m.Cfg, m.GH, m.GW, r, &history, m.Now)
		if err != nil {
			return nil, err
		}
		lines = append(lines, l)
	}
	for _, c := range m.conflicts {
		l, err := m.conflictPR(c)
		if err != nil {
			return nil, err
		}
		lines = append(lines, l)
	}
	if len(lines) == 0 {
		lines = []string{"nothing to maintain"}
	}
	return lines, nil
}

func (m *Maintainer) conflictPR(c conflict) (string, error) {
	key := fmt.Sprintf("conflict %s/!%d/%s", c.it.ID, c.mr, c.sha)
	switch {
	case c.done:
		return key + ": pull request already opened", nil
	case c.held:
		return key + ": held", nil
	case m.Bot == nil:
		return key + ": the pull request waits for the org bot's token", nil
	}
	proj := m.Cfg.GitLabProject
	patch, err := m.GW.R.MRPatch(c.mr)
	if err != nil {
		return "", err
	}
	repo := m.Bot.Repo
	ref, err := m.Bot.Call("GET", "repos/"+repo+"/git/ref/heads/main", nil)
	if err != nil {
		return "", err
	}
	obj, _ := ref.(map[string]any)["object"].(map[string]any)
	branch := fmt.Sprintf("bot/p5-%s-%d-%s", strings.ToLower(c.it.ID), c.mr, c.sha)
	if _, err := m.Bot.Call("POST", "repos/"+repo+"/git/refs", map[string]any{"ref": "refs/heads/" + branch, "sha": py.Str(obj["sha"])}); err != nil {
		return "", err
	}
	path := fmt.Sprintf("patches/%s/%d/%s.patch", c.it.ID, c.mr, c.sha)
	url := gitlab.WebURL(proj, "!", c.mr)
	msg := fmt.Sprintf("chore(p5): the conflicting series of %s!%d at %s\n\n"+
		"GitLab reports a conflict between %s and its target branch; this is the\n"+
		"merge request's series as GitLab serves it (.patch), for an agent to resolve.\n\n%s",
		proj, c.mr, c.sha, url, bridge.Assisted)
	if _, err := m.Bot.Call("PUT", "repos/"+repo+"/contents/"+path, map[string]any{
		"message": msg, "branch": branch, "content": base64.StdEncoding.EncodeToString([]byte(patch)),
	}); err != nil {
		return "", err
	}
	tn := bridge.Number(c.ticket)
	body := fmt.Sprintf("## Summary\n\nP5 MR maintenance (ADR-0002): %s conflicts with its target branch at head `%s`. "+
		"This pull request adds the merge request's series as GitLab serves it, `%s`, for an agent to resolve here "+
		"(each commit keeps `%s`, D2).\n\n"+
		"The bridge does not push the resolved series to the fork: the GitLab token has scope `api` only, without "+
		"`write_repository` (the maintainer's decision of 2026-11-16; D3 unmet), so a conflict stops at this pull request "+
		"by design.\n\nMirror ticket: https://github.com/%s/issues/%d\n\nPart of https://github.com/%s/issues/2%s",
		url, c.sha, path, bridge.Assisted, m.GH.Repo, tn, m.GH.Repo, bridge.Footer)
	got, err := m.Bot.Call("POST", "repos/"+repo+"/pulls", map[string]any{
		"title": fmt.Sprintf("chore(p5): resolve the conflict of merge request %d (%s)", c.mr, strings.ToLower(c.it.ID)),
		"head":  branch, "base": "main", "body": body,
	})
	if err != nil {
		return "", err
	}
	pr := bridge.Number(got.(map[string]any))
	if _, err := m.GH.AddComment(tn, fmt.Sprintf("<!-- upstream-maintained: %s -> pull %d -->\n[%s!%d](%s) conflicts with its target at `%s`: "+
		"opened https://github.com/%s/pull/%d with its series for an agent to resolve. The bridge pushes nothing to GitLab "+
		"(the token has no `write_repository`), so the conflict stops there.%s",
		key, pr, proj, c.mr, url, c.sha, repo, pr, bridge.Footer)); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s: opened pull request #%d", key, pr), nil
}
