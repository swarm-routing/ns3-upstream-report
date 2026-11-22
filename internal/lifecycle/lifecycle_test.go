// Copyright (c) 2026 Daniel Joppi (https://github.com/swarm-routing)
// SPDX-License-Identifier: MIT OR GPL-2.0-only

package lifecycle

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/swarm-routing/ns3-upstream-report/internal/fake"
	"github.com/swarm-routing/ns3-upstream-report/internal/github"
	"github.com/swarm-routing/ns3-upstream-report/internal/gitlab"
	"github.com/swarm-routing/ns3-upstream-report/internal/httpx"
	"github.com/swarm-routing/ns3-upstream-report/internal/mirror"
	"github.com/swarm-routing/ns3-upstream-report/internal/register"
)

var now = time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)

type org struct {
	issues map[string]string
	calls  []fake.Call
}

var orgPath = regexp.MustCompile(`^https://api\.github\.com/repos/(swarm-routing/[^/]+)/issues(?:/(\d+))?(/comments)?$`)

func (o *org) transport(method, url string, _ map[string]string, body any) (httpx.Response, error) {
	o.calls = append(o.calls, fake.Call{Method: method, URL: url, Body: body})
	m := orgPath.FindStringSubmatch(url)
	if m == nil {
		return httpx.Response{}, fmt.Errorf("unexpected %s %s", method, url)
	}
	repo, n := m[1], m[2]
	switch {
	case method == "POST" && n == "":
		num := 100 + len(o.issues)
		o.issues[fmt.Sprintf("%s#%d", repo, num)] = "open"
		return httpx.Response{Status: 201, Raw: []byte(fmt.Sprintf(`{"number": %d}`, num))}, nil
	case method == "PATCH":
		o.issues[repo+"#"+n] = body.(map[string]any)["state"].(string)
		return httpx.Response{Status: 200, Raw: []byte(`{}`)}, nil
	case method == "POST":
		return httpx.Response{Status: 201, Raw: []byte(`{"id": 1}`)}, nil
	}
	return httpx.Response{Status: 200, Raw: []byte(fmt.Sprintf(`{"state": %q}`, o.issues[repo+"#"+n]))}, nil
}

func (o *org) writes() int {
	n := 0
	for _, c := range o.calls {
		if c.Method != "GET" {
			n++
		}
	}
	return n
}

func TestLifecycle(t *testing.T) {
	m, _ := register.Parse(fake.Register)
	m["item"] = m["item"].([]any)[:1]
	m["item"].([]any)[0].(map[string]any)["workarounds"] = []any{"swarm-routing/ns3-containers#35"}
	if errs := register.Validate(m); len(errs) > 0 {
		t.Fatal(errs)
	}
	cfg := register.FromRaw(m)
	fl, fg := fake.NewGitLab(), fake.NewGitHub()
	gl := gitlab.New("nsnam/ns-3-dev", "", fl.Transport)
	gh := github.New("o/r", "t", fg.Transport, false)
	if l, _ := CmdLifecycle(cfg, gl, gh, nil, now); len(l) != 1 || l[0] != "nothing released" {
		t.Error("nothing merged, no ticket yet: nothing released")
	}
	sha := strings.Repeat("d", 40)
	mr := fl.MR(2985)
	mr["state"], mr["merge_commit_sha"] = "merged", sha
	fl.SetMR(2985, mr)
	fl.Data[fake.API+"/repository/commits/"+sha+"/refs?type=tag&per_page=100"] = []any{
		map[string]any{"type": "tag", "name": "ns-3.51"}, map[string]any{"type": "tag", "name": "ns-3.50"},
		map[string]any{"type": "tag", "name": "ns-3-dev-snapshot"}, map[string]any{"type": "tag", "name": "ns-3.50.1"},
	}
	mirror.CmdRead(cfg, gl, gh, now)
	t3 := fg.Ticket("NS3-003")
	lines, err := CmdLifecycle(cfg, gl, gh, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	b := fg.Bodies(t3)
	if lines[0] != "NS3-003: fixed in ns-3.50" || !strings.HasPrefix(b[len(b)-1], "<!-- upstream-released: ns-3.50 !2985 "+sha+" -->") ||
		!strings.Contains(lines[1], "wait for the org bot's token: swarm-routing/ns3-containers#35") {
		t.Errorf("P6: the earliest release tag with the merge commit, said once on the ticket: %v", lines)
	}
	o := &org{issues: map[string]string{"swarm-routing/ns3-tools#240": "open"}}
	bot := github.New("o/r", "bot", o.transport, false)
	lines, _ = CmdLifecycle(cfg, gl, gh, bot, now)
	var opened string
	for k := range o.issues {
		if strings.HasPrefix(k, "swarm-routing/ns3-containers#") {
			opened = k
		}
	}
	if opened == "" || !strings.HasPrefix(lines[0], "NS3-003: opened swarm-routing/ns3-containers#") || o.issues["swarm-routing/ns3-tools#240"] != "open" {
		t.Errorf("P6: one issue per workaround, recorded on the ticket; the RCAPA ticket stays open: %v", lines)
	}
	n := o.writes()
	CmdLifecycle(cfg, gl, gh, bot, now)
	if o.writes() != n {
		t.Error("P6: nothing opened twice")
	}
	o.issues[opened] = "closed"
	lines, _ = CmdLifecycle(cfg, gl, gh, bot, now)
	if o.issues["swarm-routing/ns3-tools#240"] != "closed" || lines[len(lines)-1] != "NS3-003: closed swarm-routing/ns3-tools#240" {
		t.Errorf("P6: every workaround issue closed, the RCAPA ticket is closed: %v", lines)
	}
	for _, c := range fl.Calls {
		if c.Method != "GET" {
			t.Errorf("lifecycle sends only GET to GitLab: %s %s", c.Method, c.URL)
		}
	}
}

// released is the ticket's upstream-released markers.
func released(fg *fake.GitHub, n int) []string {
	var out []string
	for _, b := range fg.Bodies(n) {
		if strings.HasPrefix(b, "<!-- upstream-released: ") {
			out = append(out, strings.SplitN(b, "\n", 2)[0])
		}
	}
	return out
}

func TestLifecycleCompeting(t *testing.T) {
	m, _ := register.Parse(fake.Register)
	m["item"] = m["item"].([]any)[:1]
	cfg := register.FromRaw(m)
	fl, fg := fake.NewGitLab(), fake.NewGitHub()
	gl := gitlab.New("nsnam/ns-3-dev", "", fl.Transport)
	gh := github.New("o/r", "t", fg.Transport, false)
	mirror.CmdRead(cfg, gl, gh, now)
	t3 := fg.Ticket("NS3-003")
	run := func() []string {
		t.Helper()
		l, err := CmdLifecycle(cfg, gl, gh, nil, now)
		if err != nil {
			t.Fatal(err)
		}
		return l
	}
	if l := run(); l[0] != "nothing released" || len(released(fg, t3)) != 0 {
		t.Errorf("P6: the competing merge request still open, nothing released: %v", l)
	}
	sha := strings.Repeat("e", 40)
	mr := fl.MR(3011)
	mr["state"] = "closed"
	fl.SetMR(3011, mr)
	fl.Data[fake.API+"/repository/commits/"+sha+"/refs?type=tag&per_page=100"] = []any{
		map[string]any{"type": "tag", "name": "ns-3.51"}, map[string]any{"type": "tag", "name": "ns-3.50"},
	}
	if l := run(); l[0] != "nothing released" || len(released(fg, t3)) != 0 {
		t.Errorf("P6: the competing merge request closed unmerged, nothing released: %v", l)
	}
	mr["state"], mr["merge_commit_sha"] = "merged", sha
	fl.SetMR(3011, mr)
	l := run()
	r := released(fg, t3)
	if l[0] != "NS3-003: fixed in ns-3.50" || len(r) != 1 || r[0] != "<!-- upstream-released: ns-3.50 !3011 "+sha+" -->" {
		t.Errorf("P6: a merged competing merge request in a release tag is the fix: %v %v", l, r)
	}
	if b := fg.Bodies(t3); !strings.Contains(b[len(b)-1], "(competing)") {
		t.Errorf("P6: the comment says the fix is a competing merge request: %q", b[len(b)-1])
	}
	run()
	if len(released(fg, t3)) != 1 {
		t.Error("P6: the release is said once")
	}
	for _, c := range fl.Calls {
		if c.Method != "GET" {
			t.Errorf("lifecycle sends only GET to GitLab: %s %s", c.Method, c.URL)
		}
	}
}

func TestLifecycleClosedBy(t *testing.T) {
	m, _ := register.Parse(fake.Register)
	m["item"] = m["item"].([]any)[:1]
	cfg := register.FromRaw(m)
	fl, fg := fake.NewGitLab(), fake.NewGitHub()
	gl := gitlab.New("nsnam/ns-3-dev", "", fl.Transport)
	gh := github.New("o/r", "t", fg.Transport, false)
	mirror.CmdRead(cfg, gl, gh, now)
	t3 := fg.Ticket("NS3-003")
	sha := strings.Repeat("f", 40)
	closedBy := fake.API + "/issues/1364/closed_by?per_page=100"
	fl.Data[closedBy] = []any{map[string]any{"iid": 3050, "state": "merged", "merge_commit_sha": nil, "squash_commit_sha": sha}}
	tagsOf := fake.API + "/repository/commits/" + sha + "/refs?type=tag&per_page=100"
	fl.Data[tagsOf] = []any{}
	if l, err := CmdLifecycle(cfg, gl, gh, nil, now); err != nil || l[0] != "nothing released" {
		t.Errorf("P6: merged but in no release tag yet, nothing released: %v %v", l, err)
	}
	fl.Data[tagsOf] = []any{map[string]any{"type": "tag", "name": "ns-3-dev-snapshot"}, map[string]any{"type": "tag", "name": "ns-3.52"}}
	l, err := CmdLifecycle(cfg, gl, gh, nil, now)
	r := released(fg, t3)
	if err != nil || l[0] != "NS3-003: fixed in ns-3.52" || len(r) != 1 || r[0] != "<!-- upstream-released: ns-3.52 !3050 "+sha+" -->" {
		t.Errorf("P6: the merge request that closed the issue, not listed, is the fix: %v %v %v", l, r, err)
	}
	if b := fg.Bodies(t3); !strings.Contains(b[len(b)-1], "which closed [nsnam/ns-3-dev#1364](https://gitlab.com/nsnam/ns-3-dev/-/issues/1364)") {
		t.Errorf("P6: the comment says the merge request closed the issue: %q", b[len(b)-1])
	}
	CmdLifecycle(cfg, gl, gh, nil, now)
	if len(released(fg, t3)) != 1 {
		t.Error("P6: the release is said once")
	}
}

// A withdrawn merge request is not one of ours to follow: closed, it is no fix; merged
// (reopened and taken by ns-3), it counts only as the issue's closing merge request.
func TestLifecycleWithdrawn(t *testing.T) {
	m, _ := register.Parse(fake.RegisterWithdrawn())
	m["item"] = m["item"].([]any)[:1]
	cfg := register.FromRaw(m)
	fl, fg := fake.NewGitLab(), fake.NewGitHub()
	gl := gitlab.New("nsnam/ns-3-dev", "", fl.Transport)
	gh := github.New("o/r", "t", fg.Transport, false)
	sha := strings.Repeat("e", 40)
	mr := fl.MR(2985)
	mr["state"], mr["merge_commit_sha"] = "merged", sha
	fl.SetMR(2985, mr)
	fl.Data[fake.API+"/repository/commits/"+sha+"/refs?type=tag&per_page=100"] = []any{map[string]any{"type": "tag", "name": "ns-3.50"}}
	mirror.CmdRead(cfg, gl, gh, now)
	t3 := fg.Ticket("NS3-003")
	if l, err := CmdLifecycle(cfg, gl, gh, nil, now); err != nil || l[0] != "nothing released" || len(released(fg, t3)) != 0 {
		t.Errorf("P6: a withdrawn merge request outside the issue's closing list is not read as the fix: %v %v", l, err)
	}
	fl.Data[fake.API+"/issues/1364/closed_by?per_page=100"] = []any{mr}
	if l, err := CmdLifecycle(cfg, gl, gh, nil, now); err != nil || l[0] != "NS3-003: fixed in ns-3.50" {
		t.Errorf("P6: merged and closing the issue, it is the fix: %v %v", l, err)
	}
}
