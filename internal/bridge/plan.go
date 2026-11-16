// Copyright (c) 2026 Daniel Joppi (https://github.com/swarm-routing)
// SPDX-License-Identifier: MIT OR GPL-2.0-only

package bridge

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/swarm-routing/ns3-upstream-report/internal/github"
	"github.com/swarm-routing/ns3-upstream-report/internal/gitlab"
	"github.com/swarm-routing/ns3-upstream-report/internal/py"
	"github.com/swarm-routing/ns3-upstream-report/internal/register"
)

// Login of a comment's or an issue's author.
func Login(c map[string]any) string {
	u, _ := c["user"].(map[string]any)
	s, _ := u["login"].(string)
	return s
}

// Body of a comment or an issue ("" for none).
func Body(c map[string]any) string {
	s, _ := c["body"].(string)
	return s
}

// Number of an issue.
func Number(i map[string]any) int {
	n, _ := strconv.Atoi(py.Str(i["number"]))
	return n
}

// BotMarker is the value of a first-line marker in a comment the bot wrote.
func BotMarker(cfg *register.Config, c map[string]any, name string) (string, bool) {
	v, ok := FirstLineMarker(Body(c), name)
	if !ok || Login(c) != cfg.Bot {
		return "", false
	}
	return v, true
}

// FindTicket is the item's mirror ticket: `mirror`, else an issue the bot opened
// with its marker; nil if none.
func FindTicket(gh *github.Client, cfg *register.Config, it *register.Item, issues []map[string]any) (map[string]any, error) {
	if it.Mirror != 0 {
		for _, i := range issues {
			if Number(i) == it.Mirror {
				return i, nil
			}
		}
		return gh.Issue(it.Mirror)
	}
	sorted := append([]map[string]any(nil), issues...)
	sort.SliceStable(sorted, func(a, b int) bool { return Number(sorted[a]) < Number(sorted[b]) })
	for _, i := range sorted {
		if Login(i) == cfg.Bot && strings.Contains(Body(i), IDMarker(it.ID)) {
			return i, nil
		}
	}
	return nil, nil
}

// Sent is one `upstream-sent` marker: its value and when it was written.
type Sent struct{ Value, When string }

// SentMarkers is every `upstream-sent` marker the bot wrote.
func SentMarkers(cfg *register.Config, comments []map[string]any) []Sent {
	var out []Sent
	for _, c := range comments {
		if v, ok := BotMarker(cfg, c, "upstream-sent"); ok && v != "" {
			w, _ := c["created_at"].(string)
			out = append(out, Sent{v, w})
		}
	}
	return out
}

// SentKeys is the keys already sent (the part before ` -> `).
func SentKeys(cfg *register.Config, comments []map[string]any) map[string]bool {
	out := map[string]bool{}
	for _, s := range SentMarkers(cfg, comments) {
		k, _, _ := strings.Cut(s.Value, " -> ")
		out[k] = true
	}
	return out
}

// Evidence is an item's newest P1 evidence: `verified` in tracked.toml, or the verify
// workflow's `upstream-verified` marker on the ticket; empty if none.
func Evidence(cfg *register.Config, it *register.Item, comments []map[string]any) map[string]string {
	best := map[string]string{}
	for k, v := range it.Verified {
		best[k] = v
	}
	for _, c := range comments {
		v, ok := BotMarker(cfg, c, "upstream-verified")
		if !ok || v == "" {
			continue
		}
		got := map[string]string{}
		for _, x := range strings.FieldsFunc(v, py.IsSpace) {
			if k, val, ok := strings.Cut(x, "="); ok {
				got[k] = val
			}
		}
		if got["result"] == "reproduces" && got["date"] > best["date"] {
			best = got
		}
	}
	return best
}

// ReadFile is a file of the repository at base, refused outside it; ok=false if missing.
func ReadFile(base, rel string) (string, bool, error) {
	p, err := register.Contained(base, rel)
	if err != nil {
		return "", false, err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return string(data), true, nil
}

var reportTitle = regexp.MustCompile(`^# ([^` + py.SpaceClass + `].*)$`)

// ParseReport is (title, body) of reports/NS3-NNN.md: `# <GitLab title>`, then the body.
func ParseReport(text string) (string, string, error) {
	first, body, _ := strings.Cut(strings.TrimLeft(text, "\ufeff"), "\n")
	m := reportTitle.FindStringSubmatch(py.Strip(first))
	if m == nil || py.Strip(body) == "" {
		return "", "", errors.New("a report starts with `# <GitLab title>`, then its body")
	}
	return m[1], py.Strip(body), nil
}

var replyName = regexp.MustCompile(`^([0-9]+)\.md$`)
var reportName = regexp.MustCompile(`^(NS3-[0-9]{3})\.md$`)

// ReplyFile is a reply: the note id and the path relative to the root.
type ReplyFile struct{ Note, Rel string }

// ReplyFiles is replies/NS3-NNN/<note-id>.md, in name order.
func ReplyFiles(root string, it *register.Item) []ReplyFile {
	ents, err := os.ReadDir(filepath.Join(root, "replies", it.ID))
	if err != nil {
		return nil
	}
	var out []ReplyFile
	for _, e := range ents {
		if m := replyName.FindStringSubmatch(e.Name()); m != nil {
			out = append(out, ReplyFile{m[1], filepath.Join("replies", it.ID, e.Name())})
		}
	}
	return out
}

// LintFiles is every problem of reports/ and replies/ (CI runs it on each PR).
func LintFiles(cfg *register.Config, root string) ([]string, error) {
	ids := map[string]bool{}
	for _, it := range cfg.Items {
		ids[it.ID] = true
	}
	var problems []string
	for _, sub := range []string{"reports", "replies"} {
		base := filepath.Join(root, sub)
		var dirs []string
		_ = filepath.WalkDir(base, func(p string, d os.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				dirs = append(dirs, p)
			}
			return nil
		})
		for _, dir := range dirs {
			ents, _ := os.ReadDir(dir)
			for _, e := range ents {
				if e.IsDir() {
					continue
				}
				name := e.Name()
				if name == "README.md" && dir == base {
					continue
				}
				rel, _ := filepath.Rel(root, filepath.Join(dir, name))
				text, _, err := ReadFile(root, rel)
				if err != nil {
					return nil, err
				}
				if sub == "reports" {
					m := reportName.FindStringSubmatch(name)
					if m == nil || !ids[m[1]] || dir != base {
						problems = append(problems, rel+": not reports/<an id of tracked.toml>.md")
						continue
					}
					title, body, err := ParseReport(text)
					if err != nil {
						problems = append(problems, rel+": "+err.Error())
						continue
					}
					text = title + "\n" + body
				} else {
					if !ids[filepath.Base(dir)] || !replyName.MatchString(name) {
						problems = append(problems, rel+": not replies/<an id of tracked.toml>/<note id>.md")
						continue
					}
					if py.Strip(text) == "" {
						problems = append(problems, rel+": empty")
						continue
					}
				}
				for _, p := range LintOutbound(text, cfg.GitLabProject) {
					problems = append(problems, rel+": "+p)
				}
				if d, _, _ := strings.Cut(Disclosure, ";"); strings.Contains(text, d) {
					problems = append(problems, rel+": the disclosure footer is added on sending; leave it out")
				}
			}
		}
	}
	return problems, nil
}

// Policy is (gate, window for a new issue, window for a note) of an item.
func Policy(cfg *register.Config, it *register.Item) (string, time.Duration, time.Duration) {
	gate := it.Gate
	if gate == "" {
		gate = cfg.Gate
	}
	if gate != "veto-window" {
		return gate, 0, 0
	}
	return gate, time.Duration(cfg.WindowHoursNewIssue) * time.Hour, time.Duration(cfg.WindowHours) * time.Hour
}

// Candidate is one thing the bridge would post (or do) for an item.
type Candidate struct {
	Key, Kind, Title, Text, SHA, Gate, Rate string
	Action                                  string // "note" (an issue or a note), "rebase" (P5)
	Target                                  int    // 0: a new issue
	Problems                                []string
	Due                                     time.Time
}

// Candidates is what an item would post: its report and its replies.
func Candidates(cfg *register.Config, root string, it *register.Item, comments []map[string]any, now time.Time) ([]*Candidate, error) {
	proj := cfg.GitLabProject
	var out []*Candidate
	published := false
	for _, s := range SentMarkers(cfg, comments) {
		if strings.HasPrefix(s.Value, "publish ") {
			published = true
		}
	}
	text, ok, err := ReadFile(root, filepath.Join("reports", it.ID+".md"))
	if err != nil {
		return nil, err
	}
	if ok && !(it.Issue != 0 || published) {
		var problems []string
		title, body, err := ParseReport(text)
		if err != nil {
			problems = append(problems, err.Error())
		}
		if it.State != "draft" {
			problems = append(problems, fmt.Sprintf("%s is %s, not a draft: a report is filed once", it.ID, it.State))
		}
		when := Evidence(cfg, it, comments)["date"]
		if t, err := ParseTime(when + "T00:00:00Z"); when == "" || err != nil || now.Sub(t) > EvidenceDays*24*time.Hour {
			problems = append(problems, fmt.Sprintf("no P1 verify evidence from the last %d days (`verified = { date, master, run }` in tracked.toml)", EvidenceDays))
		}
		body = Outbound(body)
		problems = append(problems, LintOutbound(title+"\n"+body, proj)...)
		out = append(out, &Candidate{Key: "publish " + it.ID, Kind: "#", Title: title, Text: body, Action: "note",
			SHA: Digest("publish", it.ID, title, body), Problems: problems})
	}
	type th struct {
		kind string
		iid  int
	}
	threads := map[string]th{}
	for _, c := range comments {
		if v, ok := BotMarker(cfg, c, "gitlab-note"); ok {
			f := strings.FieldsFunc(v, py.IsSpace)
			if len(f) != 2 {
				continue
			}
			if m := GLRef.FindStringSubmatch(f[0]); m != nil && m[1] == proj {
				n, _ := strconv.Atoi(m[3])
				threads[f[1]] = th{m[2], n}
			}
		}
	}
	sent := SentKeys(cfg, comments)
	for _, rf := range ReplyFiles(root, it) {
		key := fmt.Sprintf("reply %s/%s", it.ID, rf.Note)
		if sent[key] {
			continue
		}
		t, _, err := ReadFile(root, rf.Rel)
		if err != nil {
			return nil, err
		}
		body := Outbound(t)
		var problems []string
		target, ok := threads[rf.Note]
		if !ok || !RefInItem(it, target.kind, target.iid) {
			problems = append(problems, fmt.Sprintf("note %s is not a mirrored note of %s's own issue or MRs", rf.Note, it.ID))
			if !ok {
				target = th{"#", 0}
			}
		}
		problems = append(problems, LintOutbound(body, proj)...)
		out = append(out, &Candidate{Key: key, Kind: target.kind, Target: target.iid, Text: body, Action: "note",
			SHA: Digest("reply", it.ID, rf.Note, []any{target.kind, target.iid}, body), Problems: problems})
	}
	return out, nil
}

// RefInItem is whether (kind, iid) is the item's issue or one of its merge requests.
func RefInItem(it *register.Item, kind string, iid int) bool {
	if kind == "#" {
		return it.Issue == iid
	}
	return it.HasMR(iid)
}

// Ref is a GitLab reference of an item: ours (mirrored) or a competing merge request.
type Ref struct {
	Kind     string
	IID      int
	Mirrored bool
}

// Refs is the item's GitLab references, ours first, then extra ones (sent from here).
func Refs(it *register.Item, extra [][2]any) []Ref {
	var out []Ref
	if it.Issue != 0 {
		out = append(out, Ref{"#", it.Issue, true})
	}
	for _, i := range it.MergeRequests {
		out = append(out, Ref{"!", i, true})
	}
	for _, i := range it.Competing {
		out = append(out, Ref{"!", i, false})
	}
	for _, e := range extra {
		k, i := e[0].(string), e[1].(int)
		dup := false
		for _, r := range out {
			if r.Kind == k && r.IID == i {
				dup = true
			}
		}
		if !dup {
			out = append(out, Ref{k, i, true})
		}
	}
	return out
}

var schedRe = regexp.MustCompile(`^(.+) sha=([0-9a-f]{64}) due=([^` + py.SpaceClass + `]+)$`)

type sched struct {
	sha string
	due time.Time
}

// Scheduled is key -> (sha, due) of the latest `upstream-scheduled` marker per key.
func Scheduled(cfg *register.Config, comments []map[string]any) map[string]sched {
	out := map[string]sched{}
	for _, c := range comments {
		if v, ok := BotMarker(cfg, c, "upstream-scheduled"); ok {
			if m := schedRe.FindStringSubmatch(v); m != nil {
				if t, err := ParseTime(m[3]); err == nil {
					out[m[1]] = sched{m[2], t}
				}
			}
		}
	}
	return out
}

// Post is one post the bridge made: its key, its thread and when.
type Post struct {
	Key, Kind string
	IID       int
	When      time.Time
}

// RateLimited is why a post must wait for the rate limits, or "".
func RateLimited(c *Candidate, history []Post, now time.Time) string {
	if c.Action == "rebase" {
		return "" // not a post; its key (the head) makes it once
	}
	day := now.Add(-24 * time.Hour)
	if strings.HasPrefix(c.Key, "publish ") {
		for _, h := range history {
			if strings.HasPrefix(h.Key, "publish ") && !h.When.Before(day) {
				return "one new issue per day"
			}
		}
	}
	for _, h := range history {
		if h.Kind == c.Kind && h.IID == c.Target && !h.When.Before(day) {
			return "one note per thread per 24 h"
		}
	}
	return ""
}

var histRe = regexp.MustCompile(`^[^` + py.SpaceClass + `]+?([#!])([0-9]+)`)
var notPost = regexp.MustCompile(` rebase$`)

// History is every post the bridge made, from the sent markers (a P5 rebase is not a post).
func History(cfg *register.Config, all [][]map[string]any) []Post {
	var out []Post
	for _, comments := range all {
		for _, s := range SentMarkers(cfg, comments) {
			key, rest, _ := strings.Cut(s.Value, " -> ")
			if notPost.MatchString(rest) {
				continue
			}
			m := histRe.FindStringSubmatch(rest)
			if m == nil || s.When == "" {
				continue
			}
			t, err := ParseTime(s.When)
			if err != nil {
				continue
			}
			n, _ := strconv.Atoi(m[2])
			out = append(out, Post{key, m[1], n, t})
		}
	}
	return out
}

// Row is one candidate of the plan with its ticket and status:
// blocked, held, schedule, waiting, rate, ready.
type Row struct {
	Item     *register.Item
	Ticket   map[string]any
	Comments []map[string]any
	C        *Candidate
	Status   string
}

// Held is whether a ticket carries the `hold` label.
func Held(t map[string]any) bool {
	ls, _ := t["labels"].([]any)
	for _, lb := range ls {
		if m, ok := lb.(map[string]any); ok {
			if py.Str(m["name"]) == Hold {
				return true
			}
		} else if py.Str(lb) == Hold {
			return true
		}
	}
	return false
}

// Status of a candidate: blocked, held, schedule (Due set), waiting (Due set) or ready.
func Status(c *Candidate, held bool, sch map[string]sched, wIssue, wNote time.Duration, now time.Time) string {
	s, have := sch[c.Key]
	switch {
	case len(c.Problems) > 0:
		return "blocked"
	case held:
		return "held"
	case !have || s.sha != c.SHA:
		if strings.HasPrefix(c.Key, "publish ") {
			c.Due = now.Add(wIssue)
		} else {
			c.Due = now.Add(wNote)
		}
		return "schedule"
	case s.due.After(now):
		c.Due = s.due
		return "waiting"
	}
	return "ready"
}

// Extra is a source of more candidates for an item (P5's maintenance).
type Extra func(it *register.Item, ticket map[string]any, comments []map[string]any) ([]*Candidate, error)

// Plan is every candidate of every item with its status, and the bridge's post history.
func Plan(cfg *register.Config, gh *github.Client, root string, now time.Time, more Extra) ([]*Row, []Post, error) {
	issues, err := gh.Issues()
	if err != nil {
		return nil, nil, err
	}
	var rows []*Row
	var all [][]map[string]any
	for _, it := range cfg.Items {
		t, err := FindTicket(gh, cfg, it, issues)
		if err != nil {
			return nil, nil, err
		}
		if t == nil {
			continue
		}
		comments, err := gh.Comments(Number(t))
		if err != nil {
			return nil, nil, err
		}
		all = append(all, comments)
		held := Held(t)
		sch := Scheduled(cfg, comments)
		gate, wi, wn := Policy(cfg, it)
		var cands []*Candidate
		if more == nil {
			cands, err = Candidates(cfg, root, it, comments, now)
		} else {
			cands, err = more(it, t, comments)
		}
		if err != nil {
			return nil, nil, err
		}
		for _, c := range cands {
			st := Status(c, held, sch, wi, wn, now)
			c.Gate = gate
			rows = append(rows, &Row{it, t, comments, c, st})
		}
	}
	history := History(cfg, all)
	for _, r := range rows {
		if r.Status == "ready" {
			if why := RateLimited(r.C, history, now); why != "" {
				r.Status, r.C.Rate = "rate", why
			}
		}
	}
	return rows, history, nil
}

// Announce comments the schedule and the blocks of the rows on their tickets; returns
// (lines, ready?).
func Announce(cfg *register.Config, gh *github.Client, rows []*Row, now time.Time) ([]string, bool, error) {
	var lines []string
	ready := false
	proj := cfg.GitLabProject
	for _, r := range rows {
		c, n := r.C, Number(r.Ticket)
		switch r.Status {
		case "blocked":
			mark := fmt.Sprintf("<!-- upstream-blocked: %s sha=%s -->", c.Key, c.SHA)
			seen := false
			for _, x := range r.Comments {
				if strings.HasPrefix(Body(x), mark) {
					seen = true
				}
			}
			if !seen {
				var b strings.Builder
				for i, p := range c.Problems {
					if i > 0 {
						b.WriteString("\n")
					}
					b.WriteString("- " + p)
				}
				if _, err := gh.AddComment(n, fmt.Sprintf("%s\n**Not scheduled:** `%s` fails its guards:\n\n%s%s", mark, c.Key, b.String(), Footer)); err != nil {
					return nil, false, err
				}
			}
			lines = append(lines, fmt.Sprintf("%s: blocked: %s", c.Key, strings.Join(c.Problems, "; ")))
		case "schedule":
			due := c.Due.Format(ISO)
			var where string
			switch {
			case c.Action == "rebase":
				where = fmt.Sprintf("a rebase of [%s!%d](%s)", proj, c.Target, gitlab.WebURL(proj, "!", c.Target))
			case c.Target == 0:
				where = "a new issue"
			default:
				where = fmt.Sprintf("a note on [%s%s%d](%s)", proj, c.Kind, c.Target, gitlab.WebURL(proj, c.Kind, c.Target))
			}
			title := ""
			if c.Title != "" {
				title = "**Title:** " + c.Title + "\n\n"
			}
			body := fmt.Sprintf("<!-- upstream-scheduled: %s sha=%s due=%s -->\n**Scheduled:** `%s` goes out as %s after %s (gate `%s`), unless this ticket is labelled `%s`.\n\n%s````markdown\n%s\n````%s",
				c.Key, c.SHA, due, c.Key, where, due, c.Gate, Hold, title, c.Text, Footer)
			if _, err := gh.AddComment(n, body); err != nil {
				return nil, false, err
			}
			lines = append(lines, fmt.Sprintf("%s: scheduled for %s", c.Key, due))
			ready = ready || !c.Due.After(now)
		default:
			l := c.Key + ": " + r.Status
			if r.Status == "rate" {
				l += " (" + c.Rate + ")"
			}
			lines = append(lines, l)
			ready = ready || r.Status == "ready"
		}
	}
	return lines, ready, nil
}

// CmdPlan comments the schedule and the blocks on the tickets; returns (lines, ready?).
func CmdPlan(cfg *register.Config, gh *github.Client, root string, now time.Time) ([]string, bool, error) {
	rows, _, err := Plan(cfg, gh, root, now, nil)
	if err != nil {
		return nil, false, err
	}
	lines, ready, err := Announce(cfg, gh, rows, now)
	if err != nil {
		return nil, false, err
	}
	if len(rows) == 0 {
		lines = append(lines, "nothing to send")
	}
	return lines, ready, nil
}
