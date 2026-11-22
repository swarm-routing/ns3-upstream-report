// Copyright (c) 2026 Daniel Joppi (https://github.com/swarm-routing)
// SPDX-License-Identifier: MIT OR GPL-2.0-only

// Package send is P3 publish and P4 reply: it recomputes the plan and posts every
// ready post (a report as a new GitLab issue, a reply as a note in its thread),
// through the write client's scope guard, then links it back with an
// `upstream-sent` marker.
package send

import (
	"fmt"
	"time"

	"github.com/swarm-routing/ns3-upstream-report/internal/bridge"
	"github.com/swarm-routing/ns3-upstream-report/internal/github"
	"github.com/swarm-routing/ns3-upstream-report/internal/gitlab"
	"github.com/swarm-routing/ns3-upstream-report/internal/gitlabwrite"
	"github.com/swarm-routing/ns3-upstream-report/internal/register"
)

// Allow sets the threads of an item the writer may post to (its own issue and merge
// requests; a withdrawn merge request of ours takes notes, never a rebase).
func Allow(gw *gitlabwrite.Client, it *register.Item) {
	gw.Allowed = map[gitlabwrite.Thread]bool{}
	gw.NotesOnly = map[gitlabwrite.Thread]bool{}
	for _, r := range bridge.Refs(it, nil) {
		if r.Mirrored {
			th := gitlabwrite.Thread{Kind: r.Kind, IID: r.IID}
			gw.Allowed[th] = true
			if r.Withdrawn {
				gw.NotesOnly[th] = true
			}
		}
	}
}

// Post sends one ready note candidate (a new issue when Target is 0), unless GitLab
// already has it (a link-back lost after GitLab took the post), and links it back.
func Post(cfg *register.Config, gh *github.Client, gw *gitlabwrite.Client, r *bridge.Row, history *[]bridge.Post, now time.Time) (string, error) {
	proj, c := cfg.GitLabProject, r.C
	Allow(gw, r.Item)
	var marker, msg, url string
	if c.Target == 0 {
		iid, u, done, err := gw.R.IssueByTitle(c.Title)
		if err != nil {
			return "", err
		}
		if !done {
			if iid, u, err = gw.CreateIssue(c.Title, c.Text); err != nil {
				return "", err
			}
		}
		url = u
		marker = fmt.Sprintf("%s -> %s#%d", c.Key, proj, iid)
		msg = fmt.Sprintf("Published as [%s#%d](%s). The lifecycle workflow opens the pull request that records `issue = %d` and `state = \"published\"` for %s in tracked.toml.", proj, iid, u, iid, r.Item.ID)
		*history = append(*history, bridge.Post{Key: c.Key, Kind: "#", IID: iid, When: now})
	} else {
		nid, err := gw.R.NoteWithText(c.Kind, c.Target, c.Text)
		if err != nil {
			return "", err
		}
		if nid == "" {
			if nid, err = gw.CreateNote(c.Kind, c.Target, c.Text); err != nil {
				return "", err
			}
		}
		url = fmt.Sprintf("%s#note_%s", gitlab.WebURL(proj, c.Kind, c.Target), nid)
		marker = fmt.Sprintf("%s -> %s%s%d note %s", c.Key, proj, c.Kind, c.Target, nid)
		msg = fmt.Sprintf("Sent as [a note on %s%s%d](%s).", proj, c.Kind, c.Target, url)
		*history = append(*history, bridge.Post{Key: c.Key, Kind: c.Kind, IID: c.Target, When: now})
	}
	if _, err := gh.AddComment(bridge.Number(r.Ticket), fmt.Sprintf("<!-- upstream-sent: %s -->\n%s%s", marker, msg, bridge.Footer)); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s: sent: %s", c.Key, url), nil
}

// CmdSend posts every ready candidate, re-checked here, and links it back.
func CmdSend(cfg *register.Config, gh *github.Client, gw *gitlabwrite.Client, root string, now time.Time) ([]string, error) {
	rows, history, err := bridge.Plan(cfg, gh, root, now, nil)
	if err != nil {
		return nil, err
	}
	var lines []string
	for _, r := range rows {
		if r.Status == "rate" {
			lines = append(lines, fmt.Sprintf("%s: waits (%s)", r.C.Key, r.C.Rate))
		}
		if r.Status != "ready" {
			continue
		}
		if why := bridge.RateLimited(r.C, history, now); why != "" {
			lines = append(lines, fmt.Sprintf("%s: waits (%s)", r.C.Key, why))
			continue
		}
		l, err := Post(cfg, gh, gw, r, &history, now)
		if err != nil {
			return nil, err
		}
		lines = append(lines, l)
	}
	if len(lines) == 0 {
		lines = []string{"nothing ready"}
	}
	return lines, nil
}

// Writer is a GitLab write client over a reader (dry: every write held back after its scope check).
func Writer(r *gitlab.Client, dry bool) *gitlabwrite.Client {
	w := gitlabwrite.New(r)
	w.Dry = dry
	return w
}
