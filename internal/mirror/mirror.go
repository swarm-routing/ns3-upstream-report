// Copyright (c) 2026 Daniel Joppi (https://github.com/swarm-routing)
// SPDX-License-Identifier: MIT OR GPL-2.0-only

// Package mirror is P2 read: for every item it finds (or opens) the mirror ticket,
// fetches each GitLab issue and merge request and their notes, mirrors every new
// user note as a comment (deduplicated by note id), rewrites the status block and
// comments when the stall flags change: no activity for 14 days, need_rebase or
// conflicts, no reviewer, an unanswered question, a competing merge request. With
// a token it also warns, by an issue, 30 days before the token expires. It reads
// GitLab only: it imports no GitLab write client.
package mirror

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/swarm-routing/ns3-upstream-report/internal/bridge"
	"github.com/swarm-routing/ns3-upstream-report/internal/github"
	"github.com/swarm-routing/ns3-upstream-report/internal/gitlab"
	"github.com/swarm-routing/ns3-upstream-report/internal/py"
	"github.com/swarm-routing/ns3-upstream-report/internal/register"
)

type state struct {
	ref   bridge.Ref
	st    map[string]any
	notes []gitlab.Note
}

func getd(m map[string]any, k string, d any) any {
	if v, ok := m[k]; ok {
		return v
	}
	return d
}

func or(v any, d string) string {
	if py.Truthy(v) {
		return py.Str(v)
	}
	return d
}

// isQuestion is the regex `\?|\bplease\b` (case-insensitive, Unicode word boundaries).
func isQuestion(s string) bool {
	if strings.Contains(s, "?") {
		return true
	}
	rs := []rune(s)
	low := []rune(strings.ToLower(s))
	if len(low) != len(rs) {
		low = rs
		for i := range low {
			low[i] = []rune(strings.ToLower(string(rs[i])))[0]
		}
	}
	for i := 0; i+6 <= len(low); i++ {
		if string(low[i:i+6]) != "please" {
			continue
		}
		if (i == 0 || !py.IsWord(rs[i-1])) && (i+6 == len(rs) || !py.IsWord(rs[i+6])) {
			return true
		}
	}
	return false
}

// mentionedMRs is every `!N` not inside a word or a path.
func mentionedMRs(s string) []int {
	rs := []rune(s)
	var out []int
	for i := 0; i < len(rs); i++ {
		if rs[i] != '!' || (i > 0 && (py.IsWord(rs[i-1]) || rs[i-1] == '/')) {
			continue
		}
		j := i + 1
		for j < len(rs) && rs[j] >= '0' && rs[j] <= '9' {
			j++
		}
		if j > i+1 {
			n, _ := strconv.Atoi(string(rs[i+1 : j]))
			out = append(out, n)
			i = j - 1
		}
	}
	return out
}

// Flags is the stall flags of one item.
func Flags(cfg *register.Config, it *register.Item, states []state, now time.Time) []string {
	proj := cfg.GitLabProject
	known := map[[2]any]bool{}
	for _, r := range bridge.Refs(it, nil) {
		known[[2]any{r.Kind, r.IID}] = true
	}
	var flags []string
	var last time.Time
	for _, s := range states {
		stv := s.st["state"]
		if (stv != nil && py.Str(stv) != "opened") || !s.ref.Mirrored {
			continue
		}
		label := fmt.Sprintf("[%s%d](%s)", s.ref.Kind, s.ref.IID, or(s.st["web_url"], gitlab.WebURL(proj, s.ref.Kind, s.ref.IID)))
		var user []gitlab.Note
		for _, n := range s.notes {
			if !n.System {
				user = append(user, n)
			}
		}
		var times []time.Time
		if py.Truthy(s.st["updated_at"]) {
			times = append(times, bridge.MustTime(py.Str(s.st["updated_at"])))
		}
		for _, n := range s.notes {
			if n.CreatedAt != "" {
				times = append(times, bridge.MustTime(n.CreatedAt))
			}
		}
		for _, t := range times {
			if last.IsZero() || t.After(last) {
				last = t
			}
		}
		if s.ref.Kind == "!" {
			dms := s.st["detailed_merge_status"]
			if (dms != nil && py.Str(dms) == "need_rebase") || py.Truthy(s.st["has_conflicts"]) {
				w := "conflicts"
				if dms != nil && py.Str(dms) == "need_rebase" {
					w = "need_rebase"
				}
				flags = append(flags, fmt.Sprintf("%s (%s)", w, label))
			}
			if !py.Truthy(s.st["reviewers"]) {
				flags = append(flags, fmt.Sprintf("no reviewer (%s)", label))
			}
		}
		if len(user) > 0 && len(cfg.OurAccounts) > 0 {
			n := user[len(user)-1]
			if !cfg.Ours(n.Author) && isQuestion(n.Body) {
				flags = append(flags, fmt.Sprintf("unanswered question (%s, %s %s)", label, n.Author, py.Head(n.CreatedAt, 10)))
			}
		}
	}
	if !last.IsZero() && now.Sub(last) > bridge.StallDays*24*time.Hour {
		flags = append([]string{fmt.Sprintf("no activity for %d days", py.Days(now.Sub(last)))}, flags...)
	}
	mentioned := map[int]bool{}
	seen := map[[2]any]bool{}
	for _, s := range states {
		k := [2]any{s.ref.Kind, s.ref.IID}
		if seen[k] {
			continue
		}
		seen[k] = true
		for _, n := range s.notes {
			for _, m := range mentionedMRs(n.Body) {
				if !known[[2]any{"!", m}] || it.IsCompeting(m) {
					mentioned[m] = true
				}
			}
		}
	}
	var ms []int
	for m := range mentioned {
		if !it.HasMR(m) {
			ms = append(ms, m)
		}
	}
	sortInts(ms)
	for _, m := range ms {
		f := fmt.Sprintf("competing merge request [!%d](%s)", m, gitlab.WebURL(proj, "!", m))
		if !it.IsCompeting(m) {
			f += " (not tracked)"
		}
		flags = append(flags, f)
	}
	return flags
}

func sortInts(a []int) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j] < a[j-1]; j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}

// StatusBlock is the ticket's status block.
func StatusBlock(cfg *register.Config, it *register.Item, states []state, flags []string) string {
	proj := cfg.GitLabProject
	var rows []string
	for _, s := range states {
		st, k := s.st, s.ref.Kind
		url := or(st["web_url"], gitlab.WebURL(proj, k, s.ref.IID))
		role := ""
		if !s.ref.Mirrored {
			role = " (competing)"
		}
		var rv []string
		if l, ok := st["reviewers"].([]any); ok {
			for _, r := range l {
				m, _ := r.(map[string]any)
				rv = append(rv, py.Str(getd(m, "username", "?")))
			}
		}
		reviewers := strings.Join(rv, ",")
		if reviewers == "" {
			reviewers = "none"
		}
		ap, _ := st["_approvals"].(map[string]any)
		approvals := "–"
		if k == "!" && len(ap) > 0 {
			by, _ := ap["approved_by"].([]any)
			approvals = fmt.Sprintf("%d given, %s left", len(by), py.Str(getd(ap, "approvals_left", "?")))
		}
		pipe := "–"
		if k == "!" {
			hp, _ := st["head_pipeline"].(map[string]any)
			pipe = py.Str(getd(hp, "status", "none"))
		}
		dms, rvw := "–", "–"
		if k == "!" {
			dms = py.Str(getd(st, "detailed_merge_status", "–"))
			rvw = reviewers
		}
		var labels []string
		if l, ok := st["labels"].([]any); ok {
			for _, x := range l {
				labels = append(labels, py.Str(x))
			}
		}
		lb := strings.Join(labels, ", ")
		if lb == "" {
			lb = "none"
		}
		rows = append(rows, fmt.Sprintf("| [%s%d](%s)%s | %s | %s | %s | %s | %s | %s | %s |",
			k, s.ref.IID, url, role, py.Str(getd(st, "state", "?")), dms, rvw, approvals, pipe, lb, py.Head(or(st["updated_at"], "?"), 10)))
	}
	out := []string{"### Upstream status", "", "_Kept by the read workflow; edits here are overwritten._", ""}
	if len(states) == 0 {
		out = append(out, fmt.Sprintf("**%s**: nothing on GitLab yet.", it.State))
	} else {
		out = append(out, "| GitLab | State | Merge status | Reviewers | Approvals | Pipeline | Labels | Updated |",
			"|---|---|---|---|---|---|---|---|")
		out = append(out, rows...)
	}
	f := "none"
	if len(flags) > 0 {
		f = strings.Join(flags, "; ")
	}
	out = append(out, "", "**Stalls:** "+f)
	return strings.Join(out, "\n")
}

// NoteComment is a mirrored note as a ticket comment.
func NoteComment(proj, kind string, iid int, n gitlab.Note) string {
	return fmt.Sprintf("<!-- gitlab-note: %s%s%d %s -->\n**%s** (%s) on [%s%s%d](%s), %s UTC:\n\n%s",
		proj, kind, iid, n.ID, n.Author, bridge.Neutral(n.Name), proj, kind, iid, n.URL,
		strings.Replace(py.Head(n.CreatedAt, 16), "T", " ", -1), bridge.Quote(bridge.Neutral(n.Body))) + bridge.Footer
}

var sentRe = regexp.MustCompile(`-> ([^` + py.SpaceClass + `]+?)([#!])([0-9]+)(?: note ([^` + py.SpaceClass + `]+))?$`)

// MirroredIDs is the note ids already on the ticket, the threads sent to from here,
// and the last flags comment's value.
func MirroredIDs(cfg *register.Config, comments []map[string]any) (map[string]bool, [][2]any, *string) {
	ids := map[string]bool{}
	var extra [][2]any
	var last *string
	for _, c := range comments {
		if bridge.Login(c) != cfg.Bot {
			continue
		}
		body := bridge.Body(c)
		if v, ok := bridge.FirstLineMarker(body, "gitlab-note"); ok && v != "" {
			f := strings.FieldsFunc(v, py.IsSpace)
			ids[f[len(f)-1]] = true
		}
		if v, ok := bridge.FirstLineMarker(body, "upstream-sent"); ok && v != "" {
			if m := sentRe.FindStringSubmatch(v); m != nil {
				if m[4] != "" {
					ids[m[4]] = true
				}
				n, _ := strconv.Atoi(m[3])
				extra = append(extra, [2]any{m[2], n})
			}
		}
		if v, ok := bridge.FirstLineMarker(body, "upstream-flags"); ok {
			vv := v
			last = &vv
		}
	}
	return ids, extra, last
}

func readItem(gl *gitlab.Client, gh *github.Client, cfg *register.Config, it *register.Item, issues []map[string]any, now time.Time, out *[]string) error {
	proj := cfg.GitLabProject
	ticket, err := bridge.FindTicket(gh, cfg, it, issues)
	if err != nil {
		return err
	}
	var comments []map[string]any
	if ticket == nil {
		body := bridge.TicketBody(cfg, it)
		got, err := gh.CreateIssue(fmt.Sprintf("[upstream] %s %s", it.ID, it.Title), body, []string{"rcapa", "ns3"})
		if err != nil {
			return err
		}
		ticket = map[string]any{"number": getd(got, "number", 0), "body": body}
		*out = append(*out, fmt.Sprintf("%s: opened mirror ticket #%d", it.ID, bridge.Number(ticket)))
	} else if bridge.Number(ticket) != 0 {
		if comments, err = gh.Comments(bridge.Number(ticket)); err != nil {
			return err
		}
	}
	n := bridge.Number(ticket)
	done, extra, lastFlags := MirroredIDs(cfg, comments)
	var states []state
	if it.State != "draft" || len(extra) > 0 {
		for _, r := range bridge.Refs(it, extra) {
			st, err := gl.Item(r.Kind, r.IID)
			if err != nil {
				return err
			}
			notes, err := gl.Notes(r.Kind, r.IID, or(st["web_url"], gitlab.WebURL(proj, r.Kind, r.IID)))
			if err != nil {
				return err
			}
			states = append(states, state{r, st, notes})
		}
	}
	added := 0
	notesOf := map[[2]any][]gitlab.Note{}
	for _, s := range states {
		notesOf[[2]any{s.ref.Kind, s.ref.IID}] = s.notes
	}
outer:
	for _, s := range states {
		if !s.ref.Mirrored {
			continue
		}
		for _, note := range notesOf[[2]any{s.ref.Kind, s.ref.IID}] {
			if note.System || done[note.ID] {
				continue
			}
			if added >= bridge.MaxNewNotes {
				*out = append(*out, it.ID+": more notes left for the next run")
				continue outer
			}
			if _, err := gh.AddComment(n, NoteComment(proj, s.ref.Kind, s.ref.IID, note)); err != nil {
				return err
			}
			done[note.ID] = true
			added++
		}
	}
	if added > 0 {
		*out = append(*out, fmt.Sprintf("%s: mirrored %d note(s) on #%d", it.ID, added, n))
	}
	// the states as Python's dict keeps them: the notes of a repeated thread are its last fetch
	for i := range states {
		states[i].notes = notesOf[[2]any{states[i].ref.Kind, states[i].ref.IID}]
	}
	var flags []string
	if len(states) > 0 {
		flags = Flags(cfg, it, states, now)
	}
	if n != 0 {
		got, err := gh.Issue(n)
		if err != nil {
			return err
		}
		if len(got) > 0 {
			ticket = got
		}
	}
	body := bridge.Body(ticket)
	nb := bridge.ReplaceStatus(body, StatusBlock(cfg, it, states, flags))
	if nb != body {
		if _, err := gh.UpdateBody(n, nb); err != nil {
			return err
		}
		*out = append(*out, fmt.Sprintf("%s: status block updated on #%d", it.ID, n))
	}
	key := strings.Join(flags, ",")
	lf := ""
	if lastFlags != nil {
		lf = *lastFlags
	}
	if len(states) > 0 && key != lf {
		text := "**Stalls cleared.**"
		if len(flags) > 0 {
			var b []string
			for _, f := range flags {
				b = append(b, "- "+f)
			}
			text = "**Stalls:**\n\n" + strings.Join(b, "\n")
		}
		if _, err := gh.AddComment(n, fmt.Sprintf("<!-- upstream-flags: %s -->\n%s%s", key, text, bridge.Footer)); err != nil {
			return err
		}
		if len(flags) > 0 {
			*out = append(*out, fmt.Sprintf("%s: stalls flagged: %s", it.ID, key))
		} else {
			*out = append(*out, it.ID+": stalls cleared")
		}
	}
	if len(*out) == 0 || !strings.HasPrefix((*out)[len(*out)-1], it.ID) {
		*out = append(*out, fmt.Sprintf("%s: #%d up to date", it.ID, n))
	}
	return nil
}

// CmdRead is P2 read over every item (a closed one only while it has a mirror ticket).
func CmdRead(cfg *register.Config, gl *gitlab.Client, gh *github.Client, now time.Time) ([]string, error) {
	issues, err := gh.Issues()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, it := range cfg.Items {
		if it.State == "closed" && it.Mirror == 0 {
			continue
		}
		if err := readItem(gl, gh, cfg, it, issues, now, &out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ExpiryWarnDays is how long before the GitLab token expires read opens an issue.
const ExpiryWarnDays = 30

// ExpiryTitle is the warning issue's title.
func ExpiryTitle(date string) string { return "[P7] the GitLab token expires on " + date }

// CheckExpiry opens one issue when the GitLab token expires within 30 days (the
// single-token decision's compensation); nothing without a token.
func CheckExpiry(gl *gitlab.Client, gh *github.Client, now time.Time) ([]string, error) {
	if gl.Token == "" {
		return nil, nil
	}
	me, err := gl.TokenSelf()
	if err != nil {
		return []string{"GitLab token: expiry not readable (" + err.Error() + ")"}, nil
	}
	exp, _ := me["expires_at"].(string)
	if exp == "" {
		return nil, nil
	}
	t, err := time.Parse("2006-01-02", exp)
	if err != nil {
		return []string{"GitLab token: expires_at " + exp + " not a date"}, nil
	}
	left := t.Sub(now)
	if left > ExpiryWarnDays*24*time.Hour {
		return nil, nil
	}
	issues, err := gh.Issues()
	if err != nil {
		return nil, err
	}
	title := ExpiryTitle(exp)
	for _, i := range issues {
		if py.Str(i["title"]) == title {
			return []string{fmt.Sprintf("GitLab token: expires %s (%d days); #%d says so", exp, py.Days(left), bridge.Number(i))}, nil
		}
	}
	body := fmt.Sprintf("The GitLab token in the environments `gitlab-read` and `gitlab-write` expires on %s (%d days from %s).\n\n"+
		"P7 (`rotate.yml`) rotates it once it is 60 days old; a rotation that has not happened by now failed or could not run: "+
		"see its runs. If it cannot rotate, the maintainer creates a new token (`api`) and stores it as `GITLAB_TOKEN` in both "+
		"environments before that date, or every pipeline that reads or writes GitLab with it stops.%s",
		exp, py.Days(left), now.Format("2006-01-02"), bridge.Footer)
	got, err := gh.CreateIssue(title, body, []string{"bug"})
	if err != nil {
		return nil, err
	}
	return []string{fmt.Sprintf("GitLab token: expires %s (%d days); opened #%d", exp, py.Days(left), bridge.Number(got))}, nil
}
