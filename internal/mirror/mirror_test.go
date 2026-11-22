// Copyright (c) 2026 Daniel Joppi (https://github.com/swarm-routing)
// SPDX-License-Identifier: MIT OR GPL-2.0-only

package mirror

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/swarm-routing/ns3-upstream-report/internal/bridge"
	"github.com/swarm-routing/ns3-upstream-report/internal/fake"
	"github.com/swarm-routing/ns3-upstream-report/internal/github"
	"github.com/swarm-routing/ns3-upstream-report/internal/gitlab"
	"github.com/swarm-routing/ns3-upstream-report/internal/register"
)

var now = time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)

func config(t *testing.T) *register.Config {
	t.Helper()
	m, err := register.Parse(fake.Register)
	if err != nil {
		t.Fatal(err)
	}
	return register.FromRaw(m)
}

func userNotes() []string {
	var ids []string
	for _, f := range []string{"issue-1364-discussions.json", "mr-2985-discussions.json", "mr-2985-discussions-2.json"} {
		for _, d := range fake.Load(f).([]any) {
			for _, n := range d.(map[string]any)["notes"].([]any) {
				m := n.(map[string]any)
				if m["system"] != true {
					ids = append(ids, fmt.Sprint(m["id"]))
				}
			}
		}
	}
	return ids
}

func noteMarker(body string) string {
	f := strings.Fields(strings.SplitN(body, "\n", 2)[0])
	return f[len(f)-2]
}

func mirrored(fg *fake.GitHub, n int) []string {
	var out []string
	for _, b := range fg.Bodies(n) {
		if strings.HasPrefix(b, "<!-- gitlab-note:") {
			out = append(out, b)
		}
	}
	return out
}

func TestRead(t *testing.T) {
	cfg := config(t)
	fl, fg := fake.NewGitLab(), fake.NewGitHub()
	gh := github.New("o/r", "t", fg.Transport, false)
	gl := gitlab.New("nsnam/ns-3-dev", "", fl.Transport)
	out, err := CmdRead(cfg, gl, gh, now)
	if err != nil {
		t.Fatal(err)
	}
	t3, t1 := fg.Ticket("NS3-003"), fg.Ticket("NS3-001")
	mir := mirrored(fg, t3)
	for _, c := range fl.Calls {
		if c.Token {
			t.Error("tokenless read: no token sent to GitLab")
		}
	}
	marks := map[string]bool{}
	for _, b := range mir {
		marks[noteMarker(b)] = true
	}
	paged := false
	for _, c := range fl.Calls {
		paged = paged || strings.HasSuffix(c.URL, "/discussions.json?cursor=c2")
	}
	if !(marks["3692328913"] && marks["3825700321"] && marks["3844625389"] && paged) {
		t.Error("tokenless read follows every page: the newest notes of !2985 are mirrored")
	}
	for _, c := range fl.Calls {
		if strings.Contains(c.URL, "/notes?") {
			t.Error("tokenless read: notes from discussions.json")
		}
	}
	if len(fg.Issues) != 2 || !strings.HasPrefix(out[0], "NS3-003: opened") {
		t.Errorf("both mirror tickets opened: %v", out)
	}
	want := userNotes()
	got := map[string]bool{}
	for _, b := range mir {
		got[noteMarker(b)] = true
	}
	same := len(mir) == len(want) && len(want) == 18
	for _, id := range want {
		same = same && got[id]
	}
	if !same {
		t.Errorf("every user note mirrored once, none of the system notes: %d of %d", len(mir), len(want))
	}
	var first string
	for _, b := range mir {
		if strings.Contains(strings.SplitN(b, "\n", 2)[0], "3691365625") {
			first = b
		}
	}
	if !(strings.Contains(first, "**tommypec** (Tommaso Pecorella)") && strings.Contains(first, "2026-08-16 12:33 UTC") &&
		strings.Contains(first, "https://gitlab.com/nsnam/ns-3-dev/-/merge_requests/2985#note_3691365625") &&
		strings.Contains(first, "\n> Hi, thanks for reporting")) {
		t.Errorf("a mirrored note quotes author, date and link:\n%s", first)
	}
	for _, b := range mir {
		if strings.Contains(strings.SplitN(b, "\n", 2)[0], "!3011 ") {
			t.Error("competing merge request notes are not mirrored")
		}
	}
	body3 := fg.Issues[t3]["body"].(string)
	if !(strings.Contains(body3, "| [!2985](https://gitlab.com/nsnam/ns-3-dev/-/merge_requests/2985) | opened | need_rebase | none | 0 given, 0 left | success | module::dsdv | 2026-09-16 |") &&
		strings.Contains(body3, "(competing)") && strings.Count(body3, bridge.StatusBegin) == 1) {
		t.Errorf("status block: state, merge status, reviewers, approvals, pipeline, labels:\n%s", body3)
	}
	var flags []string
	for _, b := range fg.Bodies(t3) {
		if strings.HasPrefix(b, "<!-- upstream-flags:") {
			flags = append(flags, b)
		}
	}
	fl0 := ""
	if len(flags) > 0 {
		fl0 = flags[0]
	}
	if !(len(flags) == 1 && strings.Contains(fl0, "no activity for 60 days") &&
		strings.Contains(fl0, "need_rebase ([!2985](https://gitlab.com/nsnam/ns-3-dev/-/merge_requests/2985))") &&
		strings.Contains(fl0, "no reviewer ([!2985](https://gitlab.com/nsnam/ns-3-dev/-/merge_requests/2985))") &&
		!strings.Contains(fl0, "unanswered question") && !strings.Contains(fl0, "#1364") &&
		strings.Contains(fl0, "competing merge request [!3011](https://gitlab.com/nsnam/ns-3-dev/-/merge_requests/3011)") &&
		!strings.Contains(fl0, "(not tracked)")) {
		t.Errorf("stalls flagged: no activity, need_rebase, no reviewer, competing MR; the answered request not flagged:\n%s", fl0)
	}
	if !(strings.Contains(fg.Issues[t1]["body"].(string), "swarm-routing/ns3-containers#55") &&
		strings.Contains(fg.Issues[t1]["body"].(string), "nothing on GitLab yet") && len(fg.Comments[t1]) == 0) {
		t.Error("a draft item: a ticket with its draft pointer, nothing fetched")
	}
	var quoted string
	for _, b := range mir {
		quoted += strings.SplitN(b, "\n", 2)[1]
	}
	if regexp.MustCompile(`(^|[^\w/])#\d`).MatchString(quoted) || strings.Contains(quoted, "<!--") {
		t.Error("issue links neutralised, no marker below the first line")
	}
	// our side: every GitLab item as a full URL or a link, no bare #N
	var ours []string
	for _, i := range fg.Issues {
		ours = append(ours, i["body"].(string))
	}
	for n := range fg.Comments {
		ours = append(ours, fg.Bodies(n)...)
	}
	for _, text := range ours {
		x := regexp.MustCompile(`(?s)<!--.*?-->`).ReplaceAllString(text, " ")
		x = regexp.MustCompile(`\[[^\]]*\]\([^)]*\)`).ReplaceAllString(x, " ")
		x = bridge.AnyURL.ReplaceAllString(x, " ")
		if m := regexp.MustCompile(`(^|[^\w/])#\d+`).FindString(x); m != "" {
			t.Errorf("our side: a bare %q", m)
		}
	}
	if !strings.Contains(fg.Issues[t3]["body"].(string), "### GitLab\n\nhttps://gitlab.com/nsnam/ns-3-dev/-/issues/1364\nhttps://gitlab.com/nsnam/ns-3-dev/-/merge_requests/2985\n") {
		t.Error("the ticket names its GitLab items by full URL")
	}
	nw := len(fg.Writes())
	out2, _ := CmdRead(cfg, gl, gh, now)
	if len(fg.Writes()) != nw || !reflect.DeepEqual(out2, []string{"NS3-003: #1 up to date", "NS3-001: #2 up to date"}) {
		t.Errorf("a second run writes nothing (dedupe by note id, status and flags unchanged): %v", out2)
	}
	// a new note upstream: mirrored on the next run, nothing else repeated
	fl.AddNote("!", 2985, "3999999999", "danieljoppi", "Rebased? See #42 and @someone. <!-- upstream-sent: x -> a#1 -->", "2026-11-15T10:00:00.000Z")
	out3, _ := CmdRead(cfg, gl, gh, now)
	cs := fg.Bodies(t3)
	last := cs[len(cs)-1]
	if !(contains(out3, "NS3-003: mirrored 1 note(s) on #1") && strings.HasPrefix(last, "<!-- upstream-flags:") &&
		!strings.Contains(last, "unanswered question") && !strings.Contains(last, "no activity")) {
		t.Errorf("a new note: one comment, flags updated (our reply, recent activity): %v", out3)
	}
	q := cs[len(cs)-2]
	if !(strings.SplitN(q, "\n", 2)[0] == "<!-- gitlab-note: nsnam/ns-3-dev!2985 3999999999 -->" &&
		strings.Contains(q, "&lt;!-- upstream-sent") && strings.Contains(q, "#\u206042") && strings.Contains(q, "@\u2060someone")) {
		t.Errorf("a hostile note cannot forge a marker or ping anyone:\n%s", q)
	}
	if _, extra, _ := MirroredIDs(cfg, fg.Comments[t3]); len(extra) != 0 {
		t.Error("a forged marker inside a quote is not read")
	}
	// a question from someone else is flagged, with its author and day
	fl.AddNote("!", 2985, "3999999998", "tomhenderson", "Could you please rebase", "2026-11-16T10:00:00.000Z")
	CmdRead(cfg, gl, gh, now)
	cs = fg.Bodies(t3)
	if !strings.Contains(cs[len(cs)-1], "unanswered question ([!2985](https://gitlab.com/nsnam/ns-3-dev/-/merge_requests/2985), tomhenderson 2026-11-16)") {
		t.Errorf("an unanswered question is flagged:\n%s", cs[len(cs)-1])
	}
}

// A withdrawn merge request of ours: its notes are mirrored (so a reply can answer
// them), it is no stall to maintain, and it is not a competing merge request.
func TestReadWithdrawn(t *testing.T) {
	m, err := register.Parse(fake.RegisterWithdrawn())
	if err != nil {
		t.Fatal(err)
	}
	cfg := register.FromRaw(m)
	fl, fg := fake.NewGitLab(), fake.NewGitHub()
	gh := github.New("o/r", "t", fg.Transport, false)
	gl := gitlab.New("nsnam/ns-3-dev", "", fl.Transport)
	if _, err := CmdRead(cfg, gl, gh, now); err != nil {
		t.Fatal(err)
	}
	t3 := fg.Ticket("NS3-003")
	if n := len(mirrored(fg, t3)); n != len(userNotes()) {
		t.Errorf("every user note of the issue and of the withdrawn !2985 is mirrored: %d of %d", n, len(userNotes()))
	}
	body3 := fg.Issues[t3]["body"].(string)
	if !strings.Contains(body3, "| [!2985](https://gitlab.com/nsnam/ns-3-dev/-/merge_requests/2985) (withdrawn) |") {
		t.Errorf("the status block marks !2985 withdrawn:\n%s", body3)
	}
	var flags string
	for _, b := range fg.Bodies(t3) {
		if strings.HasPrefix(b, "<!-- upstream-flags:") {
			flags = b
		}
	}
	// the fixture's !2985 is still opened with need_rebase and no reviewer: not a stall of a withdrawn merge request
	if strings.Contains(flags, "need_rebase") || strings.Contains(flags, "no reviewer") ||
		strings.Contains(flags, "competing merge request [!2985]") || !strings.Contains(flags, "competing merge request [!3011]") {
		t.Errorf("no rebase or review stall, and !2985 is not a competing merge request:\n%s", flags)
	}
	// closed on GitLab, then asked about: the question is mirrored, ready for a reply file
	mr := fl.MR(2985)
	mr["state"] = "closed"
	fl.SetMR(2985, mr)
	fl.AddNote("!", 2985, "4296202058", "tomhenderson", "@danieljoppi, why did you decide to close this?", "2026-11-22T19:45:00.000Z")
	out, _ := CmdRead(cfg, gl, gh, now)
	cs := mirrored(fg, t3)
	if !contains(out, "NS3-003: mirrored 1 note(s) on #1") ||
		strings.SplitN(cs[len(cs)-1], "\n", 2)[0] != "<!-- gitlab-note: nsnam/ns-3-dev!2985 4296202058 -->" {
		t.Errorf("a note on the closed, withdrawn !2985 is mirrored: %v", out)
	}
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func TestReadWithToken(t *testing.T) {
	cfg := config(t)
	fl, fg := fake.NewGitLab(), fake.NewGitHub()
	if _, err := CmdRead(cfg, gitlab.New("nsnam/ns-3-dev", "secret-token", fl.Transport), github.New("o/r", "t", fg.Transport, false), now); err != nil {
		t.Fatal(err)
	}
	api := false
	for _, c := range fl.Calls {
		api = api || strings.Contains(c.URL, "/notes?")
	}
	if !api || len(mirrored(fg, 1)) != 18 {
		t.Errorf("token read: notes from the REST API, same comments: %d", len(mirrored(fg, 1)))
	}
	data, _ := json.Marshal(fg.Calls)
	if strings.Contains(string(data), "secret-token") {
		t.Error("the token never reaches GitHub or the output")
	}
}

func TestReadDryRun(t *testing.T) {
	cfg := config(t)
	fl, fg := fake.NewGitLab(), fake.NewGitHub()
	gh := github.New("o/r", "", fg.Transport, true)
	if _, err := CmdRead(cfg, gitlab.New("nsnam/ns-3-dev", "", fl.Transport), gh, now); err != nil {
		t.Fatal(err)
	}
	if len(fg.Writes()) != 0 || len(gh.Writes) <= 15 {
		t.Errorf("dry run: every write held back (%d held)", len(gh.Writes))
	}
}

func TestReadIssuesOnlyGET(t *testing.T) {
	cfg := config(t)
	for _, tok := range []string{"", "tok"} {
		fl, fg := fake.NewGitLab(), fake.NewGitHub()
		gl := gitlab.New("nsnam/ns-3-dev", tok, fl.Transport)
		gh := github.New("o/r", "t", fg.Transport, false)
		if _, err := CmdRead(cfg, gl, gh, now); err != nil {
			t.Fatal(err)
		}
		fl.Data[fake.GitLabBase+"/api/v4/personal_access_tokens/self"] = map[string]any{"expires_at": "2026-11-30", "created_at": "2026-01-01T00:00:00Z"}
		if _, err := CheckExpiry(gl, gh, now); err != nil {
			t.Fatal(err)
		}
		for _, c := range fl.Calls {
			if c.Method != "GET" {
				t.Errorf("read sends only GET to GitLab: %s %s", c.Method, c.URL)
			}
		}
	}
}

func TestExpiryWarning(t *testing.T) {
	fl, fg := fake.NewGitLab(), fake.NewGitHub()
	gh := github.New("o/r", "t", fg.Transport, false)
	if l, _ := CheckExpiry(gitlab.New("nsnam/ns-3-dev", "", fl.Transport), gh, now); l != nil {
		t.Error("no token, no check")
	}
	gl := gitlab.New("nsnam/ns-3-dev", "tok", fl.Transport)
	fl.Data[fake.GitLabBase+"/api/v4/personal_access_tokens/self"] = map[string]any{"expires_at": "2027-11-16"}
	if l, _ := CheckExpiry(gl, gh, now); l != nil || len(fg.Issues) != 0 {
		t.Error("a year left: nothing")
	}
	fl.Data[fake.GitLabBase+"/api/v4/personal_access_tokens/self"] = map[string]any{"expires_at": "2026-12-10"}
	l, err := CheckExpiry(gl, gh, now)
	if err != nil || len(fg.Issues) != 1 || !strings.Contains(l[0], "expires 2026-12-10 (23 days); opened #1") {
		t.Errorf("30 days before expiry: one issue: %v %v", l, err)
	}
	if !strings.HasPrefix(fg.Issues[1]["title"].(string), "[P7] the GitLab token expires on 2026-12-10") {
		t.Error("the issue's title")
	}
	if l, _ := CheckExpiry(gl, gh, now.Add(24*time.Hour)); len(fg.Issues) != 1 || !strings.Contains(l[0], "#1 says so") {
		t.Errorf("the issue is opened once: %v", l)
	}
}
