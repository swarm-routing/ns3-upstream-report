// Copyright (c) 2026 Daniel Joppi (https://github.com/swarm-routing)
// SPDX-License-Identifier: MIT OR GPL-2.0-only

// Package lifecycle is P6: a merged merge request (ours, a competing one, or one
// that closed the item's GitLab issue) whose commit an ns-3 release tag contains sets "fixed in ns-3.X" on the ticket, once a P1 verify record
// (tracked.toml's `verified`, or the verify workflow's marker) names a `master` that contains
// that commit (until then it waits, and says so on the ticket once; #79), and opens one issue per
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

// candidate is a merge request that may be the item's fix.
type candidate struct {
	iid  int
	role string // "ours", "competing" or "closed_by"
	st   map[string]any
}

// candidates is every merge request that may be the item's fix: ours, then the
// competing ones, then any other that closes the item's GitLab issue on merge
// (GitLab's closed_by list), each once. They are only read: P5 and the replies
// follow ours alone.
func candidates(gl *gitlab.Client, it *register.Item) ([]candidate, error) {
	var out []candidate
	seen := map[int]bool{}
	for _, l := range []struct {
		role string
		iids []int
	}{{"ours", it.MergeRequests}, {"competing", it.Competing}} {
		for _, mr := range l.iids {
			if seen[mr] {
				continue
			}
			seen[mr] = true
			st, err := gl.Item("!", mr)
			if err != nil {
				return nil, err
			}
			out = append(out, candidate{mr, l.role, st})
		}
	}
	if it.Issue == 0 {
		return out, nil
	}
	closing, err := gl.ClosedBy(it.Issue)
	if err != nil {
		return nil, err
	}
	for _, st := range closing {
		mr, _ := strconv.Atoi(py.Str(st["iid"]))
		if mr == 0 || seen[mr] {
			continue
		}
		seen[mr] = true
		out = append(out, candidate{mr, "closed_by", st})
	}
	return out, nil
}

// verifiedWith is the newest P1 verify record whose `master` contains commit sha:
// tracked.toml's `verified = { date, master, run }`, or a verify workflow's
// `upstream-verified` marker on the ticket, whatever its result; nil if none (#79).
func verifiedWith(gl *gitlab.Client, cfg *register.Config, it *register.Item, comments []map[string]any, sha string) (map[string]string, error) {
	var recs []map[string]string
	if it.Verified["master"] != "" {
		recs = append(recs, it.Verified)
	}
	for _, c := range comments {
		v, ok := bridge.BotMarker(cfg, c, "upstream-verified")
		if !ok || v == "" {
			continue
		}
		got := map[string]string{}
		for _, x := range strings.FieldsFunc(v, py.IsSpace) {
			if k, val, ok := strings.Cut(x, "="); ok {
				got[k] = val
			}
		}
		if got["master"] != "" {
			recs = append(recs, got)
		}
	}
	sort.SliceStable(recs, func(a, b int) bool { return recs[a]["date"] > recs[b]["date"] })
	for _, r := range recs {
		ok, err := gl.Contains(r["master"], sha)
		if err != nil {
			return nil, err
		}
		if ok {
			return r, nil
		}
	}
	return nil, nil
}

// verifyResult is a verify record's result in words (tracked.toml's carries none).
func verifyResult(r map[string]string) string {
	switch r["result"] {
	case "":
		return "recorded in tracked.toml"
	case "not-reproduced":
		return "the bug does not reproduce"
	case "reproduces":
		return "the bug **still reproduces**"
	}
	return r["result"]
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
			cands, err := candidates(gl, it)
			if err != nil {
				return nil, err
			}
			for _, c := range cands {
				st := c.st
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
					mr := c.iid
					role := ""
					switch c.role {
					case "competing":
						role = " (competing)"
					case "closed_by":
						role = fmt.Sprintf(", which closed [%s#%d](%s),", cfg.GitLabProject, it.Issue, gitlab.WebURL(cfg.GitLabProject, "#", it.Issue))
					}
					fix := fmt.Sprintf("%s !%d %s", tags[0], mr, sha)
					rec, err := verifiedWith(gl, cfg, it, comments, sha)
					if err != nil {
						return nil, err
					}
					if rec == nil {
						// a release tag can lag the merge; P6 acts only once P1 has run
						// on a master that holds the fix (#55, #79)
						said := false
						for _, cm := range comments {
							if v, ok := bridge.BotMarker(cfg, cm, "upstream-release-waits"); ok && v == fix {
								said = true
							}
						}
						if !said {
							if _, err := gh.AddComment(tn, fmt.Sprintf("<!-- upstream-release-waits: %s -->\n**Released in %s, waiting for P1 verify**: [!%d](%s)%s merged as `%s`, which the release tag contains. It is marked fixed once a P1 verify has run on an ns-3-dev `master` that contains that commit (the weekly `verify.yml`, or its dispatch).%s",
								fix, tags[0], mr, gitlab.WebURL(cfg.GitLabProject, "!", mr), role, py.Head(sha, 12), bridge.Footer)); err != nil {
								return nil, err
							}
						}
						lines = append(lines, fmt.Sprintf("%s: in %s (!%d), waits for a P1 verify on a master containing %s", it.ID, tags[0], mr, py.Head(sha, 12)))
						break
					}
					released = fix
					if _, err := gh.AddComment(tn, fmt.Sprintf("<!-- upstream-released: %s -->\n**Fixed in %s**: [!%d](%s)%s merged as `%s`, which the release tag contains. P1 verify on `master` `%s` (%s): %s.%s",
						released, tags[0], mr, gitlab.WebURL(cfg.GitLabProject, "!", mr), role, py.Head(sha, 12),
						py.Head(rec["master"], 12), rec["date"], verifyResult(rec), bridge.Footer)); err != nil {
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
