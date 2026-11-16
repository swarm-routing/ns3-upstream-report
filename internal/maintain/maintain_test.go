// Copyright (c) 2026 Daniel Joppi (https://github.com/swarm-routing)
// SPDX-License-Identifier: MIT OR GPL-2.0-only

package maintain

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/swarm-routing/ns3-upstream-report/internal/bridge"
	"github.com/swarm-routing/ns3-upstream-report/internal/fake"
	"github.com/swarm-routing/ns3-upstream-report/internal/github"
	"github.com/swarm-routing/ns3-upstream-report/internal/gitlab"
	"github.com/swarm-routing/ns3-upstream-report/internal/httpx"
	"github.com/swarm-routing/ns3-upstream-report/internal/mirror"
	"github.com/swarm-routing/ns3-upstream-report/internal/register"
	"github.com/swarm-routing/ns3-upstream-report/internal/send"
)

var now = time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)

type bot struct{ calls []fake.Call }

func (b *bot) transport(method, url string, _ map[string]string, body any) (httpx.Response, error) {
	b.calls = append(b.calls, fake.Call{Method: method, URL: url, Body: body})
	switch {
	case method == "GET" && strings.HasSuffix(url, "/git/ref/heads/main"):
		return httpx.Response{Status: 200, Raw: []byte(`{"object": {"sha": "abc123"}}`)}, nil
	case method == "POST" && strings.HasSuffix(url, "/git/refs"), method == "PUT" && strings.Contains(url, "/contents/"):
		return httpx.Response{Status: 201, Raw: []byte(`{}`)}, nil
	case method == "POST" && strings.HasSuffix(url, "/pulls"):
		return httpx.Response{Status: 201, Raw: []byte(`{"number": 77}`)}, nil
	}
	return httpx.Response{}, fmt.Errorf("unexpected bot call %s %s", method, url)
}

type world struct {
	cfg *register.Config
	fl  *fake.GitLab
	fg  *fake.GitHub
	gh  *github.Client
	t3  int
}

func setup(t *testing.T) *world {
	t.Helper()
	m, _ := register.Parse(fake.Register)
	cfg := register.FromRaw(m)
	fl, fg := fake.NewGitLab(), fake.NewGitHub()
	gh := github.New("o/r", "t", fg.Transport, false)
	if _, err := mirror.CmdRead(cfg, gitlab.New("nsnam/ns-3-dev", "", fl.Transport), gh, now); err != nil {
		t.Fatal(err)
	}
	return &world{cfg, fl, fg, gh, fg.Ticket("NS3-003")}
}

func (w *world) run(t *testing.T, at time.Time, b *bot) []string {
	t.Helper()
	w.fg.Clock = at.Format(bridge.ISO)
	m := &Maintainer{Cfg: w.cfg, GH: w.gh, GW: send.Writer(gitlab.New("nsnam/ns-3-dev", "tok", w.fl.Transport), false), Root: t.TempDir(), Now: at}
	if b != nil {
		m.Bot = github.New("o/r", "bot", b.transport, false)
	}
	out, err := m.Run()
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func has(lines []string, s string) bool {
	for _, l := range lines {
		if strings.Contains(l, s) {
			return true
		}
	}
	return false
}

func gitlabWrites(fl *fake.GitLab) []string {
	var out []string
	for _, c := range fl.Writes() {
		out = append(out, c.Method+" "+strings.TrimPrefix(c.URL, fake.API))
	}
	return out
}

func TestRebase(t *testing.T) {
	w := setup(t)
	out := w.run(t, now, nil)
	if !has(out, "rebase NS3-003/!2985/83a80c744c10: scheduled for 2026-11-17T12:00:00Z") {
		t.Errorf("P5: need_rebase on our MR is scheduled as a rebase, 24 h out: %v", out)
	}
	if !has(w.fg.Bodies(w.t3), "goes out as a rebase of [nsnam/ns-3-dev!2985]") {
		t.Error("the rebase is announced on the ticket")
	}
	if out := w.run(t, now.Add(time.Hour), nil); !has(out, "rebase NS3-003/!2985/83a80c744c10: waiting") || len(w.fl.Writes()) != 0 {
		t.Errorf("nothing inside the window: %v", out)
	}
	w.fg.Issues[w.t3]["labels"] = []any{map[string]any{"name": bridge.Hold}}
	if out := w.run(t, now.Add(25*time.Hour), nil); !has(out, ": held") || len(w.fl.Writes()) != 0 {
		t.Errorf("hold stops the rebase: %v", out)
	}
	w.fg.Issues[w.t3]["labels"] = []any{}
	out = w.run(t, now.Add(25*time.Hour), nil)
	if got := gitlabWrites(w.fl); len(got) != 1 || got[0] != "PUT /merge_requests/2985/rebase" || !has(out, "rebase NS3-003/!2985/83a80c744c10: rebase requested") {
		t.Errorf("P5: after the window GitLab's rebase API is called, once: %v %v", got, out)
	}
	b := w.fg.Bodies(w.t3)
	if !strings.HasPrefix(b[len(b)-1], "<!-- upstream-sent: rebase NS3-003/!2985/83a80c744c10 -> nsnam/ns-3-dev!2985 rebase -->") {
		t.Error("the rebase is linked back")
	}
	if out := w.run(t, now.Add(26*time.Hour), nil); len(gitlabWrites(w.fl)) != 1 || has(out, "rebase requested") {
		t.Errorf("once per head: %v", out)
	}
	// a rebase is not a note: a reply to the same thread the same day is not held by it
	rows := bridge.History(w.cfg, [][]map[string]any{w.fg.Comments[w.t3]})
	if len(rows) != 0 {
		t.Errorf("a rebase is not in the post history: %v", rows)
	}
}

func TestNotOursNorMerged(t *testing.T) {
	w := setup(t)
	mr := w.fl.MR(2985)
	mr["author"] = map[string]any{"username": "someone"}
	w.fl.SetMR(2985, mr)
	if out := w.run(t, now, nil); len(out) != 1 || out[0] != "nothing to maintain" {
		t.Errorf("P5 scope: nothing on a merge request not ours: %v", out)
	}
	mr = w.fl.MR(2985)
	mr["author"] = map[string]any{"username": "danieljoppi"}
	mr["state"] = "merged"
	w.fl.SetMR(2985, mr)
	if out := w.run(t, now, nil); out[0] != "nothing to maintain" {
		t.Errorf("P5: nothing on a merged merge request: %v", out)
	}
}

func TestConflict(t *testing.T) {
	w := setup(t)
	mr := w.fl.MR(2985)
	mr["has_conflicts"] = true
	w.fl.SetMR(2985, mr)
	patch := "From 0123 Mon Sep 17 00:00:00 2001\nSubject: [PATCH] dsdv: guard\n\n---\n src/dsdv/a.cc | 1 +\n"
	w.fl.Patches[fake.GitLabBase+"/nsnam/ns-3-dev/-/merge_requests/2985.patch"] = patch
	if out := w.run(t, now, nil); !has(out, "conflict NS3-003/!2985/83a80c744c10: the pull request waits for the org bot's token") {
		t.Errorf("without the bot's token the conflict waits: %v", out)
	}
	b := &bot{}
	out := w.run(t, now, b)
	if !has(out, "conflict NS3-003/!2985/83a80c744c10: opened pull request #77") {
		t.Fatalf("P5: a conflict opens a pull request here: %v", out)
	}
	var put, pull map[string]any
	for _, c := range b.calls {
		if c.Method == "PUT" {
			put = c.Body.(map[string]any)
			if !strings.HasSuffix(c.URL, "/contents/patches/NS3-003/2985/83a80c744c10.patch") {
				t.Errorf("the patch's path: %s", c.URL)
			}
		}
		if strings.HasSuffix(c.URL, "/pulls") {
			pull = c.Body.(map[string]any)
		}
	}
	content, _ := base64.StdEncoding.DecodeString(put["content"].(string))
	if string(content) != patch || put["branch"] != "bot/p5-ns3-003-2985-83a80c744c10" ||
		!strings.HasSuffix(put["message"].(string), "\n\n"+bridge.Assisted) {
		t.Errorf("the conflicting patch, committed with Assisted-by: Claude: %v", put)
	}
	if pull["base"] != "main" || pull["head"] != "bot/p5-ns3-003-2985-83a80c744c10" ||
		pull["title"] != "chore(p5): resolve the conflict of merge request 2985 (ns3-003)" ||
		!strings.Contains(pull["body"].(string), "stops at this pull request") {
		t.Errorf("the pull request: %v", pull)
	}
	if len(w.fl.Writes()) != 0 {
		t.Errorf("a conflict writes nothing to GitLab (no push, no rebase): %v", gitlabWrites(w.fl))
	}
	n := len(b.calls)
	if out := w.run(t, now.Add(time.Hour), b); !has(out, "pull request already opened") || len(b.calls) != n {
		t.Errorf("one pull request per head: %v", out)
	}
	if has(out, "rebase") {
		t.Error("a conflicting merge request is not rebased")
	}
}

func TestReviewRequest(t *testing.T) {
	w := setup(t)
	mr := w.fl.MR(2985)
	mr["detailed_merge_status"] = "mergeable"
	w.fl.SetMR(2985, mr)
	if out := w.run(t, now, nil); has(out, "review") {
		t.Errorf("the last note is not ours a week ago... yet: %v", out)
	}
	w.fl.AddNote("!", 2985, "3999999990", "danieljoppi", "Attribution added.", "2026-11-01T10:00:00.000Z")
	if out := w.run(t, time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC), nil); has(out, "review") {
		t.Errorf("not before 7 days: %v", out)
	}
	out := w.run(t, now, nil)
	var sch []string
	for _, b := range w.fg.Bodies(w.t3) {
		if strings.Contains(b, "upstream-scheduled: review NS3-003/!2985/3999999990") {
			sch = append(sch, b)
		}
	}
	if len(sch) != 1 || !strings.Contains(sch[0], ReviewRequest) || !strings.Contains(sch[0], bridge.Disclosure) || !has(out, "scheduled for 2026-11-17T12:00:00Z") {
		t.Errorf("P5: no reviewer a week after our answer: one fixed review request, footer included: %v", out)
	}
	out = w.run(t, now.Add(25*time.Hour), nil)
	got := gitlabWrites(w.fl)
	if len(got) != 1 || got[0] != "POST /merge_requests/2985/notes" || !has(out, "review NS3-003/!2985/3999999990: sent: ") {
		t.Errorf("after the window the request is a note on the merge request: %v %v", got, out)
	}
	body := w.fl.Writes()[0].Body.(map[string]any)["body"]
	if body != ReviewRequest+"\n\n---\n"+bridge.Disclosure {
		t.Errorf("the review request's text: %q", body)
	}
	// our own note is now the last, a week passes: still at most once per 30 days
	w.fl.AddNote("!", 2985, "3999999991", "danieljoppi", "Ping.", "2026-11-18T10:00:00.000Z")
	if out := w.run(t, now.Add(10*24*time.Hour), nil); has(out, "review NS3-003/!2985/3999999991") {
		t.Errorf("at most one review request per 30 days: %v", out)
	}
	if out := w.run(t, now.Add(32*24*time.Hour), nil); !has(out, ": scheduled for 2026-12-19T12:00:00Z") || !has(out, "review NS3-003/!2985/") {
		t.Errorf("after 30 days, again: %v", out)
	}
}

func TestReviewRequestRateLimit(t *testing.T) {
	w := setup(t)
	mr := w.fl.MR(2985)
	mr["detailed_merge_status"] = "mergeable"
	w.fl.SetMR(2985, mr)
	w.fl.AddNote("!", 2985, "3999999990", "danieljoppi", "Attribution added.", "2026-11-01T10:00:00.000Z")
	w.run(t, now, nil)
	// a reply went to the same thread an hour before the request is due
	w.fg.Clock = now.Add(24 * time.Hour).Format(bridge.ISO)
	w.fg.Add(w.t3, "<!-- upstream-sent: reply NS3-003/1 -> nsnam/ns-3-dev!2985 note 5 -->\nx", "github-actions[bot]")
	out := w.run(t, now.Add(25*time.Hour), nil)
	if !has(out, "rate (one note per thread per 24 h)") || len(w.fl.Writes()) != 0 {
		t.Errorf("a review request keeps the thread's rate limit: %v", out)
	}
}
