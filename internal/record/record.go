// Copyright (c) 2026 Daniel Joppi (https://github.com/swarm-routing)
// SPDX-License-Identifier: MIT OR GPL-2.0-only

// Package record closes P3's last manual step: once send has published an item's
// report (the bot's `upstream-sent: publish NS3-NNN -> <project>#N` marker on its
// mirror ticket) while tracked.toml still lists the item without its issue, it
// opens one pull request, with the org bot's token, that sets `state =
// "published"` and `issue = N` on each such item. The register is edited as text
// (comments and order kept) from main's copy, and the result is validated before
// anything is written. It reads GitHub only: no GitLab client at all.
//
// A second publish never depends on this: bridge.Candidates stops proposing a
// report once its `upstream-sent: publish` marker exists, whatever the state.
package record

import (
	"encoding/base64"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/swarm-routing/ns3-upstream-report/internal/bridge"
	"github.com/swarm-routing/ns3-upstream-report/internal/github"
	"github.com/swarm-routing/ns3-upstream-report/internal/httpx"
	"github.com/swarm-routing/ns3-upstream-report/internal/py"
	"github.com/swarm-routing/ns3-upstream-report/internal/register"
)

// Published is one item published upstream that the register does not record yet.
type Published struct {
	ID    string
	Issue int
}

var publishRe = regexp.MustCompile(`^publish (NS3-[0-9]{3}) -> ([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)#([0-9]+)$`)

// Pending is every item without an issue whose ticket carries the bot's publish marker.
func Pending(cfg *register.Config, gh *github.Client) ([]Published, error) {
	issues, err := gh.Issues()
	if err != nil {
		return nil, err
	}
	var out []Published
	for _, it := range cfg.Items {
		if it.Issue != 0 {
			continue
		}
		t, err := bridge.FindTicket(gh, cfg, it, issues)
		if err != nil {
			return nil, err
		}
		if t == nil {
			continue
		}
		comments, err := gh.Comments(bridge.Number(t))
		if err != nil {
			return nil, err
		}
		for _, s := range bridge.SentMarkers(cfg, comments) {
			m := publishRe.FindStringSubmatch(s.Value)
			if m == nil || m[1] != it.ID || m[2] != cfg.GitLabProject {
				continue
			}
			n, _ := strconv.Atoi(m[3])
			out = append(out, Published{it.ID, n})
			break
		}
	}
	return out, nil
}

var (
	idLine    = regexp.MustCompile(`^id = "(NS3-[0-9]{3})"$`)
	stateLine = regexp.MustCompile(`^state = "[a-z]+"$`)
	tableLine = regexp.MustCompile(`^\[`)
	issueLine = regexp.MustCompile(`^issue = `)
)

// Apply is tracked.toml's text with each item set to published with its issue. Only
// the item's own keys are touched (before its first sub-table); the result must
// validate and read back with exactly those values.
func Apply(text string, pub []Published) (string, error) {
	lines := strings.SplitAfter(text, "\n")
	want := map[string]int{}
	for _, p := range pub {
		want[p.ID] = p.Issue
	}
	done := map[string]bool{}
	var out []string
	cur := ""
	for _, l := range lines {
		bare := strings.TrimRight(l, "\r\n")
		switch {
		case bare == "[[item]]":
			cur = ""
		case tableLine.MatchString(bare):
			cur = "-" // a sub-table: the item's own keys are over
		case cur == "" && idLine.MatchString(bare):
			cur = idLine.FindStringSubmatch(bare)[1]
		case cur != "" && cur != "-" && want[cur] != 0 && !done[cur] && stateLine.MatchString(bare):
			out = append(out, "state = \"published\"\n", fmt.Sprintf("issue = %d\n", want[cur]))
			done[cur] = true
			continue
		case cur != "" && cur != "-" && want[cur] != 0 && issueLine.MatchString(bare):
			return "", register.Exit("record: %s already has an issue", cur)
		}
		out = append(out, l)
	}
	for _, p := range pub {
		if !done[p.ID] {
			return "", register.Exit("record: %s has no `id` followed by `state` in tracked.toml", p.ID)
		}
	}
	got := strings.Join(out, "")
	raw, err := register.Parse(got)
	if err != nil {
		return "", err
	}
	if errs := register.Validate(raw); len(errs) > 0 {
		return "", register.Exit("record: the edited register is invalid: %s", strings.Join(errs, "; "))
	}
	for _, it := range register.FromRaw(raw).Items {
		if n, ok := want[it.ID]; ok && (it.Issue != n || it.State != "published") {
			return "", register.Exit("record: %s did not read back as published with issue %d", it.ID, n)
		}
	}
	return got, nil
}

// Branch is the pull request's branch: one per set of items and issues.
func Branch(pub []Published) string {
	var parts []string
	for _, p := range pub {
		parts = append(parts, fmt.Sprintf("%s-%d", strings.ToLower(p.ID), p.Issue))
	}
	return "bot/register-" + strings.Join(parts, "-")
}

// CmdRecord opens the register pull request with bot (nil: it says what waits for
// the org bot's token); gh reads the tickets.
func CmdRecord(cfg *register.Config, gh, bot *github.Client) ([]string, error) {
	pub, err := Pending(cfg, gh)
	if err != nil {
		return nil, err
	}
	if len(pub) == 0 {
		return []string{"register: up to date"}, nil
	}
	var names []string
	for _, p := range pub {
		names = append(names, fmt.Sprintf("%s (%s#%d)", p.ID, cfg.GitLabProject, p.Issue))
	}
	what := strings.Join(names, ", ")
	if bot == nil {
		return []string{"register: the pull request for " + what + " waits for the org bot's token"}, nil
	}
	repo := bot.Repo
	branch := Branch(pub)
	owner, _, _ := strings.Cut(repo, "/")
	open, err := bot.Pages(fmt.Sprintf("repos/%s/pulls?state=open&head=%s:%s", repo, owner, branch))
	if err != nil {
		return nil, err
	}
	if len(open) > 0 {
		return []string{fmt.Sprintf("register: pull request #%d already open for %s", bridge.Number(open[0]), what)}, nil
	}
	cur, err := bot.Call("GET", "repos/"+repo+"/contents/tracked.toml?ref=main", nil)
	if err != nil {
		return nil, err
	}
	file, _ := cur.(map[string]any)
	data, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(py.Str(file["content"]), "\n", ""))
	if err != nil {
		return nil, err
	}
	text, err := Apply(string(data), pub)
	if err != nil {
		return nil, err
	}
	ref, err := bot.Call("GET", "repos/"+repo+"/git/ref/heads/main", nil)
	if err != nil {
		return nil, err
	}
	obj, _ := ref.(map[string]any)["object"].(map[string]any)
	sha, write := py.Str(file["sha"]), true
	if _, err := bot.Call("POST", "repos/"+repo+"/git/refs", map[string]any{"ref": "refs/heads/" + branch, "sha": py.Str(obj["sha"])}); err != nil {
		// a run that stopped after creating the branch: commit on top of it (never a force push)
		if httpx.Status(err) != 422 {
			return nil, err
		}
		there, err := bot.Call("GET", "repos/"+repo+"/contents/tracked.toml?ref="+branch, nil)
		if err != nil {
			return nil, err
		}
		f, _ := there.(map[string]any)
		old, _ := base64.StdEncoding.DecodeString(strings.ReplaceAll(py.Str(f["content"]), "\n", ""))
		sha, write = py.Str(f["sha"]), string(old) != text
	}
	var ids []string
	for _, p := range pub {
		ids = append(ids, strings.ToLower(p.ID))
	}
	title := fmt.Sprintf("chore(register): record %s as published", strings.Join(ids, ", "))
	msg := fmt.Sprintf("%s\n\nThe send workflow published %s (its `upstream-sent` marker on the mirror\nticket); this sets `state = \"published\"` and `issue` in tracked.toml.\n\n%s",
		title, what, bridge.Assisted)
	if write {
		if _, err := bot.Call("PUT", "repos/"+repo+"/contents/tracked.toml", map[string]any{
			"message": msg, "branch": branch, "sha": sha,
			"content": base64.StdEncoding.EncodeToString([]byte(text)),
		}); err != nil {
			return nil, err
		}
	}
	var rows []string
	for _, p := range pub {
		rows = append(rows, fmt.Sprintf("- %s: `state = \"published\"`, `issue = %d` ([%s#%d](https://gitlab.com/%s/-/work_items/%d))",
			p.ID, p.Issue, cfg.GitLabProject, p.Issue, cfg.GitLabProject, p.Issue))
	}
	body := fmt.Sprintf("## Summary\n\nP3 publish (ADR-0002) sent these reports; the register now records them, read from the "+
		"bot's `upstream-sent: publish` markers on the mirror tickets:\n\n%s\n\nUntil this merges nothing is published twice: "+
		"the marker alone stops a second `publish` candidate. Merging it lets replies and P5 act on the issue (the item's "+
		"own threads are the send allowlist).%s", strings.Join(rows, "\n"), bridge.Footer)
	got, err := bot.Call("POST", "repos/"+repo+"/pulls", map[string]any{"title": title, "head": branch, "base": "main", "body": body})
	if err != nil {
		return nil, err
	}
	return []string{fmt.Sprintf("register: opened pull request #%d for %s", bridge.Number(got.(map[string]any)), what)}, nil
}
