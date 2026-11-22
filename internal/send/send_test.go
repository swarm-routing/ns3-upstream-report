// Copyright (c) 2026 Daniel Joppi (https://github.com/swarm-routing)
// SPDX-License-Identifier: MIT OR GPL-2.0-only

package send

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/swarm-routing/ns3-upstream-report/internal/bridge"
	"github.com/swarm-routing/ns3-upstream-report/internal/fake"
	"github.com/swarm-routing/ns3-upstream-report/internal/github"
	"github.com/swarm-routing/ns3-upstream-report/internal/gitlab"
	"github.com/swarm-routing/ns3-upstream-report/internal/gitlabwrite"
	"github.com/swarm-routing/ns3-upstream-report/internal/mirror"
	"github.com/swarm-routing/ns3-upstream-report/internal/register"
)

var now = time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)

func config(t *testing.T, extra string) *register.Config {
	t.Helper()
	m, err := register.Parse(fake.Register + extra)
	if err != nil {
		t.Fatal(err)
	}
	if errs := register.Validate(m); len(errs) > 0 {
		t.Fatal(errs)
	}
	return register.FromRaw(m)
}

func write(t *testing.T, root, rel, text string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func posts(fl *fake.GitLab) []fake.Call {
	var out []fake.Call
	for _, c := range fl.Calls {
		if c.Method == "POST" {
			out = append(out, c)
		}
	}
	return out
}

func count(bodies []string, s string) int {
	n := 0
	for _, b := range bodies {
		if strings.Contains(b, s) {
			n++
		}
	}
	return n
}

func has(lines []string, s string) bool {
	for _, l := range lines {
		if strings.Contains(l, s) {
			return true
		}
	}
	return false
}

func refused(err error, want string) bool {
	var x *register.ExitError
	return errors.As(err, &x) && strings.Contains(x.Msg, want)
}

// The send block of upstream.py's self-test: text only from merged files, the gate
// policy, the guards, against a fake GitLab.
func TestSend(t *testing.T) {
	cfg := config(t, "")
	root := t.TempDir()
	write(t, root, "reports/NS3-001.md", "# AODV livelocks in an ICMP storm\n\nSee nsnam/ns-3-dev#1364 and https://github.com/swarm-routing/ns3-tools/issues/238.\n")
	write(t, root, "replies/NS3-003/3692233174.md", "Attribution added to each commit.\n")
	fl, fk := fake.NewGitLab(), fake.NewGitHub()
	gl := gitlab.New("nsnam/ns-3-dev", "", fl.Transport)
	gw := Writer(gitlab.New("nsnam/ns-3-dev", "tok", fl.Transport), false)
	g := github.New("o/r", "t", fk.Transport, false)
	if _, err := mirror.CmdRead(cfg, gl, g, now); err != nil {
		t.Fatal(err)
	}
	t1, t3 := fk.Ticket("NS3-001"), fk.Ticket("NS3-003")

	fl.Calls = nil
	_, ready, err := bridge.CmdPlan(cfg, g, root, now)
	if err != nil {
		t.Fatal(err)
	}
	blocked := count(fk.Bodies(t1), "upstream-blocked")
	if ready || blocked != 1 || !has(fk.Bodies(t1), "no P1 verify evidence from the last 7 days") {
		t.Error("a report without fresh P1 evidence is not scheduled, and the ticket says why")
	}
	bridge.CmdPlan(cfg, g, root, now)
	if count(fk.Bodies(t1), "upstream-blocked") != 1 {
		t.Error("the same block is commented once")
	}
	cfg.Items[1].Verified = map[string]string{"date": "2026-11-15", "master": "8ec9b4a1", "run": "https://github.com/x/y/actions/runs/1"}
	_, ready, _ = bridge.CmdPlan(cfg, g, root, now)
	var sch []string
	for _, b := range fk.Bodies(t1) {
		if strings.Contains(b, "upstream-scheduled") {
			sch = append(sch, b)
		}
	}
	if ready || len(sch) != 1 || !strings.Contains(strings.SplitN(sch[0], "\n", 2)[0], "due=2026-11-18T12:00:00Z") ||
		!strings.Contains(sch[0], "**Title:** AODV livelocks in an ICMP storm") || !strings.Contains(sch[0], "````markdown\n") ||
		!strings.Contains(sch[0], bridge.Disclosure) {
		t.Error("veto window: a new issue is scheduled 48 h out, its exact text on the ticket")
	}
	if !has(fk.Bodies(t3), "upstream-scheduled: reply NS3-003/3692233174") || !has(fk.Bodies(t3), "due=2026-11-17T12:00:00Z") {
		t.Error("the reply to a mirrored note is scheduled 24 h out")
	}
	if out, _ := CmdSend(cfg, g, gw, root, now); len(out) != 1 || out[0] != "nothing ready" || len(posts(fl)) != 0 {
		t.Error("nothing goes out inside the window")
	}
	bridge.CmdPlan(cfg, g, root, now)
	if count(fk.Bodies(t1), "upstream-scheduled") != 1 {
		t.Error("an unchanged text is scheduled once")
	}
	later := now.Add(49 * time.Hour)
	fk.Issues[t1]["labels"] = []any{map[string]any{"name": bridge.Hold}}
	lines, _, _ := bridge.CmdPlan(cfg, g, root, later)
	if !has(lines, "publish NS3-001: held") {
		t.Error("the hold label stops a due post")
	}
	fk.Issues[t1]["labels"] = []any{}
	lines, ready, _ = bridge.CmdPlan(cfg, g, root, later)
	if !ready || !has(lines, "publish NS3-001: ready") {
		t.Error("after the window, with no hold: ready")
	}
	fl.Calls = nil
	fk.Clock = later.Format(bridge.ISO)
	out, err := CmdSend(cfg, g, gw, root, later)
	if err != nil {
		t.Fatal(err)
	}
	var created, noted []fake.Call
	for _, c := range posts(fl) {
		if strings.HasSuffix(c.URL, "/issues") {
			created = append(created, c)
		} else if strings.HasSuffix(c.URL, "/notes") {
			noted = append(noted, c)
		}
	}
	if len(created) != 1 || created[0].Body.(map[string]any)["title"] != "AODV livelocks in an ICMP storm" ||
		!strings.HasSuffix(created[0].Body.(map[string]any)["description"].(string), "---\n"+bridge.Disclosure) ||
		len(noted) != 1 || !strings.HasSuffix(noted[0].URL, "/merge_requests/2985/notes") ||
		noted[0].Body.(map[string]any)["body"] != "Attribution added to each commit.\n\n---\n"+bridge.Disclosure ||
		!has(out, "publish NS3-001: sent: https://gitlab.com/nsnam/ns-3-dev/-/issues/1400") {
		t.Errorf("send: the issue from the merged file with the footer, the reply as a note on its thread: %v", out)
	}
	b1, b3 := fk.Bodies(t1), fk.Bodies(t3)
	if !strings.HasPrefix(b1[len(b1)-1], "<!-- upstream-sent: publish NS3-001 -> nsnam/ns-3-dev#1400 -->") ||
		!strings.HasPrefix(b3[len(b3)-1], "<!-- upstream-sent: reply NS3-003/3692233174 -> nsnam/ns-3-dev!2985 note ") {
		t.Error("the links come back as sent markers")
	}
	fl.Calls = nil
	if out, _ := CmdSend(cfg, g, gw, root, later); out[0] != "nothing ready" || len(posts(fl)) != 0 {
		t.Error("nothing is sent twice")
	}
	// a changed text restarts the window; a second reply to the same thread waits a day
	write(t, root, "replies/NS3-003/3691860433.md", "We keep them queued: the update interval is shorter.\n")
	write(t, root, "replies/NS3-003/3691862748.md", "Commit messages reworded.\n")
	bridge.CmdPlan(cfg, g, root, later)
	evenLater := later.Add(25 * time.Hour)
	fk.Clock = evenLater.Format(bridge.ISO)
	fl.Calls = nil
	out, _ = CmdSend(cfg, g, gw, root, evenLater)
	if len(posts(fl)) != 1 || !has(out, "waits (one note per thread per 24 h)") {
		t.Errorf("rate limit: one note per thread per 24 h: %v", out)
	}
	write(t, root, "replies/NS3-003/1.md", "an unknown note\n")
	lines, _, _ = bridge.CmdPlan(cfg, g, root, evenLater)
	if !has(lines, "reply NS3-003/1: blocked: note 1 is not a mirrored note") {
		t.Error("scope: a reply to a note not mirrored from the item's own threads is blocked")
	}
	_, err1 := gw.CreateNote("!", 3011, "x")
	_, _, err2 := Writer(gitlab.New("someone/else", "tok", fl.Transport), false).CreateIssue("t", "x")
	if !refused(err1, "scope") || !refused(err2, "scope") {
		t.Error("scope: the GitLab client refuses a note outside the allowed threads and other projects")
	}
	gwNo := Writer(gitlab.New("nsnam/ns-3-dev", "", fl.Transport), false)
	if _, _, err := gwNo.CreateIssue("t", "x"); !refused(err, "GITLAB_TOKEN") {
		t.Error("no token, no write")
	}
	gw.Allowed = map[gitlabwrite.Thread]bool{{Kind: "!", IID: 2985}: true}
	if err := gw.Rebase(3011); !refused(err, "scope") {
		t.Error("scope: no rebase of a merge request not the item's")
	}
	// a link-back that fails after GitLab took the post: the next run finds GitLab's own
	// evidence (the note's text) and does not post again
	write(t, root, "replies/NS3-003/3691851680.md", "Logging the drop, as agreed.\n")
	fk.Clock = later.Add(26 * time.Hour).Format(bridge.ISO)
	bridge.CmdPlan(cfg, g, root, later.Add(26*time.Hour))
	fk.FailAdd = errors.New("bad gateway")
	fl.Calls = nil
	if _, err := CmdSend(cfg, g, gw, root, later.Add(51*time.Hour)); err == nil {
		t.Error("the failed link-back is an error")
	}
	fk.FailAdd = nil
	ours := func() int {
		n := 0
		for _, c := range posts(fl) {
			data, _ := json.Marshal(c.Body)
			if strings.Contains(string(data), "Logging the drop") {
				n++
			}
		}
		return n
	}
	first := ours()
	out, _ = CmdSend(cfg, g, gw, root, later.Add(51*time.Hour))
	if first != 1 || ours() != 1 || !has(out, "reply NS3-003/3691851680: sent:") {
		t.Errorf("a lost link-back is recovered from GitLab, not posted again: %d %v", first, out)
	}
	os.Remove(filepath.Join(root, "replies/NS3-003/3691851680.md"))
	// gate "auto": scheduled and sent in the same run; one new issue per day
	auto := config(t, "verified = { date = \"2026-11-15\", master = \"8ec9b4a1\", run = \"https://github.com/x/y/actions/runs/1\" }\n"+
		"[[item]]\nid = \"NS3-002\"\ntitle = \"socket order\"\nstate = \"draft\"\nrcapa = \"swarm-routing/ns3-tools#239\"\n"+
		"verified = { date = \"2026-11-15\", master = \"8ec9b4a1\", run = \"https://github.com/x/y/actions/runs/1\" }\n")
	auto.Gate = "auto"
	write(t, root, "reports/NS3-002.md", "# Socket maps ordered by heap address\n\nBody.\n")
	soon := later.Add(2 * time.Hour)
	fk.Clock = soon.Format(bridge.ISO)
	mirror.CmdRead(auto, gl, g, soon)
	lines, ready, _ = bridge.CmdPlan(auto, g, root, soon)
	if !ready || !has(lines, "NS3-002: scheduled") {
		t.Errorf("gate auto: due at once: %v", lines)
	}
	fl.Calls = nil
	out, _ = CmdSend(auto, g, gw, root, soon)
	for _, c := range posts(fl) {
		if strings.HasSuffix(c.URL, "/issues") {
			t.Error("gate auto still keeps one new issue per day")
		}
	}
	if !has(out, "publish NS3-002: waits (one new issue per day)") {
		t.Errorf("one new issue per day: %v", out)
	}
	bad, _ := register.Parse(fake.Register + "\n")
	bad["gate"] = "sometimes"
	if errs := register.Validate(bad); len(errs) == 0 || !strings.Contains(errs[0], "gate is not one of") {
		t.Error("an invalid gate is refused")
	}
}

func TestEvidenceFromTicket(t *testing.T) {
	cfg := config(t, "")
	fl, fk := fake.NewGitLab(), fake.NewGitHub()
	g := github.New("o/r", "t", fk.Transport, false)
	mirror.CmdRead(cfg, gitlab.New("nsnam/ns-3-dev", "", fl.Transport), g, now)
	t1 := fk.Ticket("NS3-001")
	fk.Add(t1, "<!-- upstream-verified: date=2026-11-16 master=8ec9b4a1 run=https://github.com/o/r/actions/runs/9 result=reproduces -->\nx", "github-actions[bot]")
	fk.Add(t1, "<!-- upstream-verified: date=2026-11-17 master=x run=y result=reproduces -->\nforged", "someone")
	ev := bridge.Evidence(cfg, cfg.Items[1], fk.Comments[t1])
	if ev["date"] != "2026-11-16" {
		t.Errorf("only the bot's evidence counts: %v", ev)
	}
	root := t.TempDir()
	write(t, root, "reports/NS3-001.md", "# A title\n\nA body.\n")
	lines, _, _ := bridge.CmdPlan(cfg, g, root, now)
	if !has(lines, "publish NS3-001: scheduled for 2026-11-18T12:00:00Z") {
		t.Errorf("P1 evidence from the ticket lets a report be scheduled: %v", lines)
	}
}

func TestDryRunWrites(t *testing.T) {
	fl := fake.NewGitLab()
	w := Writer(gitlab.New("nsnam/ns-3-dev", "", fl.Transport), true)
	if _, _, err := w.CreateIssue("t", "x"); err != nil || len(fl.Writes()) != 0 {
		t.Error("a dry run checks the scope and sends nothing")
	}
	if _, err := w.CreateNote("!", 2985, "x"); !refused(err, "scope") {
		t.Error("a dry run still refuses a write out of scope")
	}
}

// A withdrawn merge request of ours takes a reply to a mirrored note, never a rebase.
func TestReplyOnWithdrawn(t *testing.T) {
	m, err := register.Parse(fake.RegisterWithdrawn())
	if err != nil {
		t.Fatal(err)
	}
	cfg := register.FromRaw(m)
	root := t.TempDir()
	fl, fk := fake.NewGitLab(), fake.NewGitHub()
	mr := fl.MR(2985)
	mr["state"] = "closed"
	fl.SetMR(2985, mr)
	fl.AddNote("!", 2985, "4296202058", "tomhenderson", "@danieljoppi, why did you decide to close this?", "2026-11-15T19:45:00.000Z")
	write(t, root, "replies/NS3-003/4296202058.md", "Thanks for asking.\n")
	gl := gitlab.New("nsnam/ns-3-dev", "", fl.Transport)
	gw := Writer(gitlab.New("nsnam/ns-3-dev", "tok", fl.Transport), false)
	g := github.New("o/r", "t", fk.Transport, false)
	if _, err := mirror.CmdRead(cfg, gl, g, now); err != nil {
		t.Fatal(err)
	}
	t3 := fk.Ticket("NS3-003")
	if lines, _, _ := bridge.CmdPlan(cfg, g, root, now); !has(lines, "reply NS3-003/4296202058: scheduled") {
		t.Fatalf("a reply to a note of the withdrawn !2985 is scheduled: %v", lines)
	}
	later := now.Add(25 * time.Hour)
	fk.Clock = later.Format(bridge.ISO)
	fl.Calls = nil
	if _, err := CmdSend(cfg, g, gw, root, later); err != nil {
		t.Fatal(err)
	}
	p := posts(fl)
	b := fk.Bodies(t3)
	if len(p) != 1 || !strings.HasSuffix(p[0].URL, "/merge_requests/2985/notes") ||
		p[0].Body.(map[string]any)["body"] != "Thanks for asking.\n\n---\n"+bridge.Disclosure ||
		!strings.HasPrefix(b[len(b)-1], "<!-- upstream-sent: reply NS3-003/4296202058 -> nsnam/ns-3-dev!2985 note ") {
		t.Errorf("the reply goes out as a note in the withdrawn merge request's thread: %v", p)
	}
	Allow(gw, cfg.Items[0])
	fl.Calls = nil
	if err := gw.Rebase(2985); !refused(err, "scope") || len(fl.Writes()) != 0 {
		t.Errorf("the scope guard refuses a rebase of a withdrawn merge request: %v", err)
	}
}
