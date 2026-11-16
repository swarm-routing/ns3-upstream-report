// Copyright (c) 2026 Daniel Joppi (https://github.com/swarm-routing)
// SPDX-License-Identifier: MIT OR GPL-2.0-only

// Package bridge holds what every pipeline of the bridge shares and none of them
// needs GitLab for: the markers on the mirror tickets, the text rules in both
// directions (GitLab text quoted neutralised on GitHub; text bound for GitLab
// linted), the mirror ticket's body and status block, and the plan of what may be
// sent (the guards, the veto window, `hold`, the rate limits, the P1 evidence).
// It imports no GitLab client.
package bridge

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/swarm-routing/ns3-upstream-report/internal/gitlab"
	"github.com/swarm-routing/ns3-upstream-report/internal/py"
	"github.com/swarm-routing/ns3-upstream-report/internal/register"
)

// The fixed texts and limits of ADR-0002.
const (
	Hold         = "hold"
	EvidenceDays = 7
	StallDays    = 14
	MaxNewNotes  = 50
	// Disclosure ends every automated post (swarm-routing/ns3-upstream-report#2 guard 2; wording: D2).
	Disclosure  = "Posted by swarm-routing's upstream bridge; drafted with AI assistance (Claude) and checked by its CI, not written by hand."
	StatusBegin = "<!-- upstream-status:begin -->"
	StatusEnd   = "<!-- upstream-status:end -->"
	Footer      = "\n\n---\n_Posted by the [upstream bridge](https://github.com/swarm-routing/ns3-upstream-report) read workflow._"
	// Assisted is the trailer of every commit the bridge creates (D2).
	Assisted = "Assisted-by: Claude"
)

var (
	// GLRef is `[group/project]#N` or `!N`, as a whole string.
	GLRef = regexp.MustCompile(`^([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)?([#!])([0-9]+)$`)
	glURL = regexp.MustCompile(`^https://gitlab\.com/([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)/-/(issues|work_items|merge_requests)/([0-9]+)$`)
	// AnyURL is a URL in text.
	AnyURL = regexp.MustCompile(`https?://[^` + py.SpaceClass + `)\]>]+`)
	spans  = regexp.MustCompile("``[^`]*``|`[^`\n]*`")
)

// IDMarker is the marker that ties a mirror ticket to its item.
func IDMarker(id string) string { return "<!-- upstream-id: " + id + " -->" }

// FirstLineMarker is the value of a `<!-- NAME: value -->` marker on a comment's
// first line, ok=false if there is none.
func FirstLineMarker(body, name string) (string, bool) {
	first, _, _ := strings.Cut(body, "\n")
	first = py.Strip(first)
	pre, suf := "<!-- "+name+": ", " -->"
	if !strings.HasPrefix(first, pre) || !strings.HasSuffix(first, suf) || len(first) < len(pre)+len(suf)+1 {
		return "", false
	}
	v := first[len(pre) : len(first)-len(suf)]
	if strings.Contains(v, "\n") {
		return "", false
	}
	return v, true
}

// Neutral is GitLab text made safe to quote on GitHub: no hidden markers, no
// @mentions of GitHub users, no #N links to this repository's issues.
func Neutral(text string) string {
	text = strings.ReplaceAll(text, "<!--", "&lt;!--")
	rs := []rune(text)
	var b strings.Builder
	for i, r := range rs {
		b.WriteRune(r)
		if r == '@' && i+1 < len(rs) && isAlnumASCII(rs[i+1]) {
			b.WriteRune('\u2060')
		}
	}
	rs = []rune(b.String())
	b.Reset()
	for i, r := range rs {
		b.WriteRune(r)
		if r == '#' && i+1 < len(rs) && py.IsDigit(rs[i+1]) && (i == 0 || !(py.IsWord(rs[i-1]) || rs[i-1] == '/')) {
			b.WriteRune('\u2060')
		}
	}
	return b.String()
}

func isAlnumASCII(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
}

// Quote is the text as a Markdown block quote.
func Quote(text string) string {
	lines := py.SplitLines(text)
	if len(lines) == 0 {
		return ">"
	}
	for i, l := range lines {
		if l == "" {
			lines[i] = ">"
		} else {
			lines[i] = "> " + l
		}
	}
	return strings.Join(lines, "\n")
}

// ParseTarget is (kind, iid) of a GitLab reference of project proj: its full URL or
// `group/project#N`; ok=false otherwise.
func ParseTarget(text, proj string) (string, int, bool) {
	text = strings.TrimRight(py.Strip(text), "/")
	if m := glURL.FindStringSubmatch(text); m != nil {
		if m[1] != proj {
			return "", 0, false
		}
		n, _ := strconv.Atoi(m[3])
		if m[2] == "merge_requests" {
			return "!", n, true
		}
		return "#", n, true
	}
	if m := GLRef.FindStringSubmatch(text); m != nil && m[1] == proj {
		n, _ := strconv.Atoi(m[3])
		return m[2], n, true
	}
	return "", 0, false
}

// stripFences blanks every fenced code block (``` or ~~~, the closing fence the
// same as the opening, each indented at most three spaces).
func stripFences(text string) string {
	var b strings.Builder
	pos := 0
	for {
		start, end := findFence(text, pos)
		if start < 0 {
			b.WriteString(text[pos:])
			return b.String()
		}
		b.WriteString(text[pos:start])
		b.WriteString(" ")
		pos = end
	}
}

// fenceAt is the fence that opens at line start i (after at most three spaces), or "".
func fenceAt(text string, i int) (string, int) {
	j := i
	for j < len(text) && j-i < 3 && text[j] == ' ' {
		j++
	}
	for _, f := range []string{"```", "~~~"} {
		if strings.HasPrefix(text[j:], f) {
			return f, j + 3
		}
	}
	return "", 0
}

func findFence(text string, from int) (int, int) {
	for s := from; s < len(text); s++ {
		if s > 0 && text[s-1] != '\n' {
			continue
		}
		f, after := fenceAt(text, s)
		if f == "" {
			continue
		}
		for c := after; c < len(text); c++ {
			if text[c-1] != '\n' {
				continue
			}
			if g, _ := fenceAt(text, c); g == f {
				if e := strings.IndexByte(text[c:], '\n'); e >= 0 {
					return s, c + e
				}
				return s, len(text)
			}
		}
	}
	return -1, -1
}

// outboundRefs is every reference GitLab would link in t: (ref, kind, n).
func outboundRefs(t string) [][3]string {
	rs := []rune(t)
	var out [][3]string
	setc := func(r rune) bool { return isAlnumASCII(r) || r == '_' || r == '.' || r == '-' }
	for p := 0; p < len(rs); p++ {
		if p > 0 {
			c := rs[p-1]
			if py.IsWord(c) || strings.ContainsRune("/.-#!", c) {
				continue
			}
		}
		// the optional ref: segments of the set, at least one `/segment`
		ref, q := "", p
		i := p
		for i < len(rs) && setc(rs[i]) {
			i++
		}
		segs := 0
		if i > p {
			for i < len(rs) && rs[i] == '/' {
				j := i + 1
				for j < len(rs) && setc(rs[j]) {
					j++
				}
				if j == i+1 {
					break
				}
				i = j
				segs++
			}
		}
		if segs > 0 && i < len(rs) && (rs[i] == '#' || rs[i] == '!') {
			if k, n, e, ok := kindNum(rs, i); ok {
				out = append(out, [3]string{string(rs[p:i]), k, n})
				p = e - 1
				continue
			}
		}
		if k, n, e, ok := kindNum(rs, q); ok {
			out = append(out, [3]string{ref, k, n})
			p = e - 1
		}
	}
	return out
}

// kindNum matches [#!]\d+(?!\w) at i.
func kindNum(rs []rune, i int) (string, string, int, bool) {
	if i >= len(rs) || (rs[i] != '#' && rs[i] != '!') {
		return "", "", 0, false
	}
	j := i + 1
	for j < len(rs) && py.IsDigit(rs[j]) {
		j++
	}
	if j == i+1 || (j < len(rs) && py.IsWord(rs[j])) {
		return "", "", 0, false
	}
	return string(rs[i]), string(rs[i+1 : j]), j, true
}

// LintOutbound is what in a text bound for GitLab would link the wrong thing there:
// a bare `#N` or `!N` (GitLab links ns-3's issue or merge request N),
// `swarm-routing/<repo>#N` or any other `owner/repo#N`. Ours must be full GitHub
// URLs; an ns-3 item is `nsnam/ns-3-dev#N` or its full GitLab URL. Code spans and
// blocks are not linked.
func LintOutbound(text, proj string) []string {
	t := stripFences(text)
	t = spans.ReplaceAllString(t, " ")
	t = AnyURL.ReplaceAllString(t, " ")
	var problems []string
	for _, m := range outboundRefs(t) {
		ref, kind, n := m[0], m[1], m[2]
		what := ref + kind + n
		switch {
		case ref == proj:
		case ref != "" && strings.HasPrefix(strings.ToLower(ref), "swarm-routing/"):
			kp := "issues"
			if kind != "#" {
				kp = "pull"
			}
			problems = append(problems, fmt.Sprintf("`%s` links a GitLab project on GitLab: write https://github.com/%s/%s/%s (or /pull/%s for a pull request)", what, ref, kp, n, n))
		case ref != "":
			problems = append(problems, fmt.Sprintf("`%s` links a GitLab project on GitLab: write the item's full URL", what))
		default:
			w := "merge request"
			if kind == "#" {
				w = "issue"
			}
			problems = append(problems, fmt.Sprintf("`%s` links ns-3's %s %s on GitLab: write `%s%s%s` for an ns-3 item, or the full https://github.com/swarm-routing/<repo>/issues/%s (or /pull/%s) for ours", what, w, n, proj, kind, n, n, n))
		}
	}
	return problems
}

// Outbound is the text as it goes out: the file's text and the fixed disclosure footer.
func Outbound(text string) string { return py.Strip(text) + "\n\n---\n" + Disclosure }

// Digest is the sha256 of json.dumps(parts, ensure_ascii=False).
func Digest(parts ...any) string {
	s := sha256.Sum256([]byte(py.Dumps(parts, false)))
	return hex.EncodeToString(s[:])
}

// TicketBody is a mirror ticket's body: the item's references and where its text comes from.
func TicketBody(cfg *register.Config, it *register.Item) string {
	proj := cfg.GitLabProject
	var gl []string
	if it.Issue != 0 {
		gl = append(gl, gitlab.WebURL(proj, "#", it.Issue))
	}
	for _, m := range it.MergeRequests {
		gl = append(gl, gitlab.WebURL(proj, "!", m))
	}
	var report string
	if it.State == "draft" {
		d := it.Draft
		if len(d) == 0 {
			d = []string{"the RCAPA ticket"}
		}
		report = "Draft, not published. Drafted in " + strings.Join(d, ", ") +
			fmt.Sprintf("; it is sent from `reports/%s.md` once a pull request merges it and P1 verify evidence is recorded.", it.ID)
	} else {
		report = "Published before this bridge existed."
	}
	rcapa := it.RCAPA
	if rcapa == "" {
		rcapa = "none yet"
	}
	g := strings.Join(gl, "\n")
	if g == "" {
		g = "nothing filed yet"
	}
	return IDMarker(it.ID) + "\n" +
		"### NS3 id\n\n" + it.ID + "\n\n" +
		"### RCAPA ticket\n\n" + rcapa + "\n\n" +
		"### GitLab\n\n" + g + "\n\n" +
		"### Report\n\n" + report + " Replies are sent from `replies/" + it.ID + "/<note id>.md`." +
		" Label this ticket `" + Hold + "` to stop every post of it.\n\n" +
		StatusBegin + "\n" + StatusEnd + "\n"
}

// ReplaceStatus puts block between the status markers (appending them if missing).
func ReplaceStatus(body, block string) string {
	block = StatusBegin + "\n" + py.Strip(block) + "\n" + StatusEnd
	if strings.Contains(body, StatusBegin) && strings.Contains(body, StatusEnd) {
		head, rest, _ := strings.Cut(body, StatusBegin)
		_, tail, _ := strings.Cut(rest, StatusEnd)
		return head + block + tail
	}
	return strings.TrimRight(body, "\n") + "\n\n" + block + "\n"
}

// ParseTime is an ISO 8601 time (a trailing Z allowed).
func ParseTime(s string) (time.Time, error) {
	s = strings.Replace(s, "Z", "+00:00", -1)
	for _, l := range []string{"2006-01-02T15:04:05.999999999-07:00", "2006-01-02 15:04:05.999999999-07:00"} {
		if t, err := time.Parse(l, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid isoformat string: %q", s)
}

// MustTime is ParseTime for a time the bridge wrote itself; the zero time if invalid.
func MustTime(s string) time.Time {
	t, _ := ParseTime(s)
	return t
}

// ISO is the format of a due time.
const ISO = "2006-01-02T15:04:05Z"
