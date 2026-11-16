// Copyright (c) 2026 Daniel Joppi (https://github.com/swarm-routing)
// SPDX-License-Identifier: MIT OR GPL-2.0-only

// Package lifecycle is P6: a merged merge request of ours whose commit an ns-3
// release tag contains sets "fixed in ns-3.X" on the ticket and opens one issue per
// workaround (with the org bot's token); once every one of them is closed, the RCAPA
// ticket is closed. It reads GitLab only.
package lifecycle

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/swarm-routing/ns3-upstream-report/internal/bridge"
	"github.com/swarm-routing/ns3-upstream-report/internal/github"
	"github.com/swarm-routing/ns3-upstream-report/internal/gitlab"
	"github.com/swarm-routing/ns3-upstream-report/internal/py"
	"github.com/swarm-routing/ns3-upstream-report/internal/register"
)

var releaseTag = regexp.MustCompile(`^ns-3\.[0-9]+(?:\.[0-9]+)?$`)

func tagKey(n string) []int {
	var out []int
	for _, p := range strings.Split(n[5:], ".") {
		i, _ := strconv.Atoi(p)
		out = append(out, i)
	}
	return out
}

func less(a, b []int) bool {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return len(a) < len(b)
}

// CmdLifecycle runs P6; org is a client for other repositories (nil: it says what waits for the token).
func CmdLifecycle(cfg *register.Config, gl *gitlab.Client, gh, org *github.Client, now time.Time) ([]string, error) {
	issues, err := gh.Issues()
	if err != nil {
		return nil, err
	}
	var lines []string
	for _, it := range cfg.Items {
		t, err := bridge.FindTicket(gh, cfg, it, issues)
		if err != nil {
			return nil, err
		}
		if t == nil {
			continue
		}
		tn := bridge.Number(t)
		comments, err := gh.Comments(tn)
		if err != nil {
			return nil, err
		}
		released := ""
		for _, c := range comments {
			if v, ok := bridge.BotMarker(cfg, c, "upstream-released"); ok {
				released = v
			}
		}
		if released == "" {
			for _, mr := range it.MergeRequests {
				st, err := gl.Item("!", mr)
				if err != nil {
					return nil, err
				}
				sha := ""
				if py.Truthy(st["merge_commit_sha"]) {
					sha = py.Str(st["merge_commit_sha"])
				} else if py.Truthy(st["squash_commit_sha"]) {
					sha = py.Str(st["squash_commit_sha"])
				}
				if py.Str(st["state"]) != "merged" || sha == "" {
					continue
				}
				names, err := gl.CommitTags(sha)
				if err != nil {
					return nil, err
				}
				var tags []string
				for _, n := range names {
					if releaseTag.MatchString(n) {
						tags = append(tags, n)
					}
				}
				sort.SliceStable(tags, func(a, b int) bool { return less(tagKey(tags[a]), tagKey(tags[b])) })
				if len(tags) > 0 {
					released = fmt.Sprintf("%s !%d %s", tags[0], mr, sha)
					if _, err := gh.AddComment(tn, fmt.Sprintf("<!-- upstream-released: %s -->\n**Fixed in %s**: [!%d](%s) merged as `%s`, which the release tag contains.%s",
						released, tags[0], mr, gitlab.WebURL(cfg.GitLabProject, "!", mr), py.Head(sha, 12), bridge.Footer)); err != nil {
						return nil, err
					}
					lines = append(lines, fmt.Sprintf("%s: fixed in %s", it.ID, tags[0]))
					break
				}
			}
		}
		if released == "" {
			continue
		}
		var opened []string
		for _, c := range comments {
			if v, ok := bridge.FirstLineMarker(bridge.Body(c), "upstream-workaround"); ok && v != "" {
				opened = append(opened, v)
			}
		}
		var todo []string
		for _, w := range it.Workarounds {
			found := false
			for _, o := range opened {
				if strings.HasPrefix(o, w+" ") {
					found = true
				}
			}
			if !found {
				todo = append(todo, w)
			}
		}
		if len(todo) > 0 && org == nil {
			lines = append(lines, fmt.Sprintf("%s: workaround issues wait for the org bot's token: %s", it.ID, strings.Join(todo, ", ")))
			continue
		}
		tag, _, _ := strings.Cut(released, " ")
		for _, w := range todo {
			repo, _, _ := strings.Cut(w, "#")
			got, err := org.CreateIssueIn(repo, fmt.Sprintf("Remove the %s workaround: fixed in %s", it.ID, tag),
				fmt.Sprintf("%s (%s) is fixed in %s. Remove the workaround %s names, then close this issue; the RCAPA ticket closes when every workaround issue is closed (PROC-001).\n\nMirror ticket: https://github.com/%s/issues/%d%s",
					it.ID, it.RCAPA, tag, w, gh.Repo, tn, bridge.Footer))
			if err != nil {
				return nil, err
			}
			num := bridge.Number(got)
			if _, err := gh.AddComment(tn, fmt.Sprintf("<!-- upstream-workaround: %s %s#%d -->\nOpened %s#%d to remove the workaround of %s.%s", w, repo, num, repo, num, w, bridge.Footer)); err != nil {
				return nil, err
			}
			opened = append(opened, fmt.Sprintf("%s %s#%d", w, repo, num))
			lines = append(lines, fmt.Sprintf("%s: opened %s#%d", it.ID, repo, num))
		}
		if org == nil || it.RCAPA == "" {
			continue
		}
		var refsOpen []string
		all := true
		for _, o := range opened {
			f := strings.Fields(o)
			if len(f) < 2 {
				continue
			}
			refsOpen = append(refsOpen, f[1])
		}
		for _, r := range refsOpen {
			s, err := org.IssueState(r)
			if err != nil {
				return nil, err
			}
			if s != "closed" {
				all = false
			}
		}
		repo, num, _ := strings.Cut(it.RCAPA, "#")
		n, _ := strconv.Atoi(num)
		if all {
			s, err := org.IssueState(it.RCAPA)
			if err != nil {
				return nil, err
			}
			if s == "open" {
				names := strings.Join(refsOpen, ", ")
				if names == "" {
					names = "none listed"
				}
				if err := org.CommentIn(repo, n, fmt.Sprintf("Fixed in %s, and every workaround issue is closed (%s): closing (PROC-001).%s", tag, names, bridge.Footer)); err != nil {
					return nil, err
				}
				if err := org.Close(repo, n); err != nil {
					return nil, err
				}
				lines = append(lines, fmt.Sprintf("%s: closed %s", it.ID, it.RCAPA))
			}
		}
	}
	if len(lines) == 0 {
		lines = []string{"nothing released"}
	}
	return lines, nil
}
