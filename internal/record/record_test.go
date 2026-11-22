// Copyright (c) 2026 Daniel Joppi (https://github.com/swarm-routing)
// SPDX-License-Identifier: MIT OR GPL-2.0-only

package record

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/swarm-routing/ns3-upstream-report/internal/bridge"
	"github.com/swarm-routing/ns3-upstream-report/internal/fake"
	"github.com/swarm-routing/ns3-upstream-report/internal/github"
	"github.com/swarm-routing/ns3-upstream-report/internal/httpx"
	"github.com/swarm-routing/ns3-upstream-report/internal/register"
)

// tracked is a register with a sub-table and comments, as main's is.
const tracked = `gitlab_project = "nsnam/ns-3-dev"
our_accounts = ["danieljoppi"]

[[item]]
id = "NS3-001"
title = "AODV livelock"
state = "draft"
rcapa = "swarm-routing/ns3-tools#238"
# a comment that stays
workarounds = ["swarm-routing/AntHocNet#843"]

[item.reproduce]
modules = "core;aodv"
run = "./ns3 run x"
expect = "exit 124"

[[item]]
id = "NS3-003"
title = "dsdv: null output device"
state = "published"
rcapa = "swarm-routing/ns3-tools#240"
issue = 1364
`

// bot is the org bot's side of GitHub: main's tracked.toml, refs, contents, pulls.
type bot struct {
	calls  []fake.Call
	open   []any // open pull requests on the branch
	exists bool  // the branch is already there
	put    string
}

func (b *bot) transport(method, url string, _ map[string]string, body any) (httpx.Response, error) {
	b.calls = append(b.calls, fake.Call{Method: method, URL: url, Body: body})
	ok := func(s string) (httpx.Response, error) { return httpx.Response{Status: 200, Raw: []byte(s)}, nil }
	switch {
	case method == "GET" && strings.Contains(url, "/pulls?state=open&head=o:bot/register-ns3-001-1408"):
		if b.open == nil {
			return ok(`[]`)
		}
		return ok(`[{"number": 9}]`)
	case method == "GET" && strings.HasSuffix(url, "/contents/tracked.toml?ref=main"):
		return ok(fmt.Sprintf(`{"sha": "f1", "content": %q}`, base64.StdEncoding.EncodeToString([]byte(tracked))))
	case method == "GET" && strings.HasSuffix(url, "/git/ref/heads/main"):
		return ok(`{"object": {"sha": "abc123"}}`)
	case method == "POST" && strings.HasSuffix(url, "/git/refs"):
		if b.exists {
			return httpx.Response{}, &httpx.Error{Method: method, URL: url, Status: 422, Message: "Reference already exists"}
		}
		return ok(`{}`)
	case method == "GET" && strings.HasSuffix(url, "/contents/tracked.toml?ref=bot/register-ns3-001-1408"):
		return ok(fmt.Sprintf(`{"sha": "f2", "content": %q}`, base64.StdEncoding.EncodeToString([]byte(b.put))))
	case method == "PUT" && strings.HasSuffix(url, "/contents/tracked.toml"):
		data, _ := base64.StdEncoding.DecodeString(body.(map[string]any)["content"].(string))
		b.put = string(data)
		return ok(`{}`)
	case method == "POST" && strings.HasSuffix(url, "/pulls"):
		return ok(`{"number": 77}`)
	}
	return httpx.Response{}, fmt.Errorf("unexpected bot call %s %s", method, url)
}

func (b *bot) writes() []string {
	var out []string
	for _, c := range b.calls {
		if c.Method != "GET" {
			out = append(out, c.Method+" "+strings.TrimPrefix(c.URL, fake.GitHubAPI+"/repos/o/r/"))
		}
	}
	return out
}

func setup(t *testing.T, login string) (*register.Config, *fake.GitHub, *github.Client) {
	t.Helper()
	m, err := register.Parse(tracked)
	if err != nil {
		t.Fatal(err)
	}
	cfg := register.FromRaw(m)
	fg := fake.NewGitHub()
	fg.Issues[3] = map[string]any{"number": 3, "title": "[upstream] NS3-001", "body": bridge.IDMarker("NS3-001"),
		"state": "open", "user": map[string]any{"login": "github-actions[bot]"}}
	fg.Comments[3] = nil
	fg.Add(3, "<!-- upstream-sent: publish NS3-001 -> nsnam/ns-3-dev#1408 -->\nPublished.", login)
	return cfg, fg, github.New("o/r", "t", fg.Transport, false)
}

func has(lines []string, s string) bool {
	for _, l := range lines {
		if strings.Contains(l, s) {
			return true
		}
	}
	return false
}

func TestRecordOpensThePullRequest(t *testing.T) {
	cfg, _, gh := setup(t, "github-actions[bot]")
	b := &bot{}
	out, err := CmdRecord(cfg, gh, github.New("o/r", "bot", b.transport, false))
	if err != nil {
		t.Fatal(err)
	}
	if !has(out, "register: opened pull request #77 for NS3-001 (nsnam/ns-3-dev#1408)") {
		t.Errorf("a publish marker without an issue in the register opens the pull request: %v", out)
	}
	if got := strings.Join(b.writes(), " | "); got != "POST git/refs | PUT contents/tracked.toml | POST pulls" {
		t.Errorf("a branch, one commit of tracked.toml, one pull request: %s", got)
	}
	want := strings.Replace(tracked, "state = \"draft\"\n", "state = \"published\"\nissue = 1408\n", 1)
	if b.put != want {
		t.Errorf("only NS3-001's state and issue change, comments and order kept:\n%s", b.put)
	}
	for _, c := range b.calls {
		if c.Method == "POST" && strings.HasSuffix(c.URL, "/pulls") {
			body := c.Body.(map[string]any)
			if body["title"] != "chore(register): record ns3-001 as published" || body["head"] != "bot/register-ns3-001-1408" || body["base"] != "main" {
				t.Errorf("the pull request: %v", body)
			}
		}
		if c.Method == "PUT" && !strings.HasSuffix(c.Body.(map[string]any)["message"].(string), bridge.Assisted) {
			t.Error("the bridge's commit carries the Assisted-by trailer (D2)")
		}
	}
	// the register's own read of the result: published with its issue, so no second publish either way
	m, _ := register.Parse(b.put)
	if errs := register.Validate(m); len(errs) > 0 || register.FromRaw(m).Items[0].Issue != 1408 {
		t.Errorf("the edited register validates and reads back: %v", errs)
	}
}

func TestRecordIsIdempotent(t *testing.T) {
	cfg, _, gh := setup(t, "github-actions[bot]")
	b := &bot{open: []any{1}}
	out, _ := CmdRecord(cfg, gh, github.New("o/r", "bot", b.transport, false))
	if !has(out, "register: pull request #9 already open") || len(b.writes()) != 0 {
		t.Errorf("an open pull request on the branch: nothing written: %v %v", out, b.writes())
	}
	// a run that stopped after its commit: the branch is reused, no second commit, no force push
	b = &bot{exists: true, put: strings.Replace(tracked, "state = \"draft\"\n", "state = \"published\"\nissue = 1408\n", 1)}
	out, err := CmdRecord(cfg, gh, github.New("o/r", "bot", b.transport, false))
	if err != nil || !has(out, "opened pull request #77") || strings.Join(b.writes(), " | ") != "POST git/refs | POST pulls" {
		t.Errorf("an existing branch with the edit: only the pull request: %v %v %v", err, out, b.writes())
	}
	cfg.Items[0].Issue = 1408
	b = &bot{}
	out, _ = CmdRecord(cfg, gh, github.New("o/r", "bot", b.transport, false))
	if out[0] != "register: up to date" || len(b.calls) != 0 {
		t.Errorf("once the register has the issue: nothing: %v", out)
	}
}

func TestRecordTrustsOnlyTheBot(t *testing.T) {
	cfg, _, gh := setup(t, "someone")
	b := &bot{}
	out, _ := CmdRecord(cfg, gh, github.New("o/r", "bot", b.transport, false))
	if out[0] != "register: up to date" || len(b.calls) != 0 {
		t.Errorf("a marker in someone else's comment is not a publish: %v", out)
	}
	cfg, _, gh = setup(t, "github-actions[bot]")
	out, _ = CmdRecord(cfg, gh, nil)
	if !has(out, "waits for the org bot's token") {
		t.Errorf("without the bot token it says what waits: %v", out)
	}
}

func TestApplyRefuses(t *testing.T) {
	if _, err := Apply(tracked, []Published{{"NS3-003", 1364}}); err == nil {
		t.Error("an item that already has an issue is refused")
	}
	if _, err := Apply(tracked, []Published{{"NS3-009", 1}}); err == nil {
		t.Error("an item not in the register is refused")
	}
}

func TestApplyOnTheRegister(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "tracked.toml"))
	if err != nil {
		t.Fatal(err)
	}
	m, _ := register.Parse(string(data))
	var pub []Published
	for i, it := range register.FromRaw(m).Items {
		if it.Issue == 0 {
			pub = append(pub, Published{it.ID, 9000 + i})
		}
	}
	if len(pub) == 0 {
		return // every item recorded
	}
	if _, err := Apply(string(data), pub); err != nil {
		t.Errorf("the committed register takes the edit: %v", err)
	}
}

// A draft left in the register after a publish never publishes twice: the marker stops it.
func TestDraftAfterPublishIsNotRepublished(t *testing.T) {
	cfg, fg, _ := setup(t, "github-actions[bot]")
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "reports"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "reports", "NS3-001.md"), []byte("# AODV livelock\n\nBody.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cands, err := bridge.Candidates(cfg, root, cfg.Items[0], fg.Comments[3], time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cands {
		if strings.HasPrefix(c.Key, "publish ") {
			t.Errorf("state = draft and no issue, but the publish marker exists: no publish candidate: %v", c.Key)
		}
	}
}
