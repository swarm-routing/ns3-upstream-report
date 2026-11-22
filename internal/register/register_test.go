// Copyright (c) 2026 Daniel Joppi (https://github.com/swarm-routing)
// SPDX-License-Identifier: MIT OR GPL-2.0-only

package register

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/swarm-routing/ns3-upstream-report/internal/fake"
)

func testRaw(t *testing.T) map[string]any {
	t.Helper()
	m, err := Parse(fake.Register)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func withItem(cfg map[string]any, items ...any) map[string]any {
	out := map[string]any{}
	for k, v := range cfg {
		out[k] = v
	}
	out["item"] = items
	return out
}

func copyItem(it map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range it {
		out[k] = v
	}
	return out
}

func TestRegisterValidates(t *testing.T) {
	cfg := testRaw(t)
	if errs := Validate(cfg); len(errs) != 0 {
		t.Fatalf("the test register is valid: %v", errs)
	}
	first := cfg["item"].([]any)[0].(map[string]any)
	for _, c := range []struct {
		name  string
		patch map[string]any
		want  string
	}{
		{"bad id", map[string]any{"id": "NS3-3"}, "id is not NS3-NNN"},
		{"bad state", map[string]any{"state": "sent"}, "state is not one of"},
		{"unknown key", map[string]any{"token": "x"}, "token is not one of"},
		{"bad rcapa", map[string]any{"rcapa": "ns3-tools#240"}, "rcapa is not"},
		{"bad mr", map[string]any{"merge_requests": []any{"2985"}}, "merge_requests is not"},
		{"bad gate", map[string]any{"gate": "later"}, "gate is not one of"},
		{"bad evidence", map[string]any{"verified": map[string]any{"date": "yesterday"}}, "verified is not"},
		{"published with nothing", nil, "published without"},
		{"bad window", nil, "window_hours is not"},
	} {
		it := copyItem(first)
		for k, v := range c.patch {
			it[k] = v
		}
		top := withItem(cfg, it)
		if c.name == "published with nothing" {
			delete(it, "issue")
			delete(it, "merge_requests")
		}
		if c.name == "bad window" {
			top["window_hours"] = int64(400)
		}
		errs := Validate(top)
		if !anyContains(errs, c.want) {
			t.Errorf("register refused: %s: %v", c.name, errs)
		}
	}
	if !anyContains(Validate(withItem(cfg, first, first)), "twice") {
		t.Error("register refused: an id twice")
	}
	if !anyContains(Validate(withItem(cfg, "x")), "is not a table") {
		t.Error("register refused: an item that is not a table")
	}
}

func anyContains(l []string, s string) bool {
	for _, x := range l {
		if strings.Contains(x, s) {
			return true
		}
	}
	return false
}

func TestRepositoryRegisterIsValid(t *testing.T) {
	cfg, err := Load("tracked.toml", "../..")
	if err != nil {
		t.Fatalf("the repository's tracked.toml is valid: %v", err)
	}
	if len(cfg.Items) == 0 || cfg.GitLabProject != "nsnam/ns-3-dev" {
		t.Fatalf("read the register: %+v", cfg)
	}
}

func TestWithdrawn(t *testing.T) {
	m, err := Parse(fake.RegisterWithdrawn())
	if err != nil {
		t.Fatal(err)
	}
	if errs := Validate(m); len(errs) != 0 {
		t.Fatalf("withdrawn is a known key, and an item with only an issue and a withdrawn MR is valid: %v", errs)
	}
	it := FromRaw(m).Items[0]
	if len(it.MergeRequests) != 0 || !it.IsWithdrawn(2985) || it.HasMR(2985) || it.IsWithdrawn(3011) {
		t.Errorf("withdrawn is read apart from merge_requests: %+v", it)
	}
	first := m["item"].([]any)[0].(map[string]any)
	for _, c := range []struct {
		name  string
		patch map[string]any
		want  string
	}{
		{"bad withdrawn", map[string]any{"withdrawn": []any{"2985"}}, "withdrawn is not a list of merge request numbers"},
		{"withdrawn and live", map[string]any{"merge_requests": []any{int64(2985)}}, "!2985 is in both merge_requests and withdrawn"},
		{"withdrawn and competing", map[string]any{"competing": []any{int64(2985)}}, "!2985 is in both competing and withdrawn"},
	} {
		it := copyItem(first)
		for k, v := range c.patch {
			it[k] = v
		}
		if errs := Validate(withItem(m, it)); !anyContains(errs, c.want) {
			t.Errorf("register refused: %s: %v", c.name, errs)
		}
	}
}

func TestLoadRefusesAnInvalidRegister(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "t.toml")
	if err := os.WriteFile(p, []byte("gitlab_project = \"nsnam/ns-3-dev\"\n[[item]]\nid = \"NS3-9\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p, d); err == nil || !strings.Contains(err.Error(), "id is not") {
		t.Fatalf("validate exits on an invalid register: %v", err)
	}
	if _, err := Load("../t.toml", filepath.Join(d, "sub")); err == nil {
		t.Fatal("a register outside its directory is refused")
	}
}

func TestReproducer(t *testing.T) {
	good := map[string]any{
		"modules": "core;network;internet;aodv",
		"files": map[string]any{"scratch/c/c.cc": "https://raw.githubusercontent.com/swarm-routing/ns3-containers/" +
			strings.Repeat("c", 40) + "/test/fixtures/manet-canary/manet-canary.cc"},
		"run":    "timeout 120 ./ns3 run --no-build 'c --protocol=aodv'",
		"expect": "exit 124",
	}
	if errs := ValidateReproduce("X", good); len(errs) != 0 {
		t.Fatalf("a reproducer table is valid: %v", errs)
	}
	for _, c := range []struct {
		name, key string
		val       any
		want      string
	}{
		{"unpinned file", "files", map[string]any{"scratch/c.cc": "https://example.com/c.cc"}, "pinned"},
		{"path out of the tree", "files", map[string]any{"scratch/../x": good["files"].(map[string]any)["scratch/c/c.cc"]}, "pinned"},
		{"bad expect", "expect", "hangs", "expect"},
		{"bad modules", "modules", "core network", "modules"},
	} {
		r := copyItem(good)
		r[c.key] = c.val
		if !anyContains(ValidateReproduce("X", r), c.want) {
			t.Errorf("reproducer refused: %s", c.name)
		}
	}
}

func TestContained(t *testing.T) {
	d := t.TempDir()
	for _, p := range []string{"../x", "/etc/passwd"} {
		if _, err := Contained(d, p); err == nil || !strings.Contains(err.Error(), "outside") {
			t.Errorf("paths are contained: %s", p)
		}
	}
	if p, err := Contained(d, "scratch/a.cc"); err != nil || !strings.HasPrefix(p, d) && !strings.Contains(p, filepath.Base(d)) {
		t.Errorf("a path inside: %s %v", p, err)
	}
	if err := os.Symlink("/etc", filepath.Join(d, "out")); err == nil {
		if _, err := Contained(d, "out/passwd"); err == nil {
			t.Error("no symlink out")
		}
	}
}
