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
