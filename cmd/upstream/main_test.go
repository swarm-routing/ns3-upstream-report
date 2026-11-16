// Copyright (c) 2026 Daniel Joppi (https://github.com/swarm-routing)
// SPDX-License-Identifier: MIT OR GPL-2.0-only

package main

import (
	"bytes"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/swarm-routing/ns3-upstream-report/internal/fake"
	"github.com/swarm-routing/ns3-upstream-report/internal/verify"
)

const module = "github.com/swarm-routing/ns3-upstream-report/"

// deps is every package of this module a package imports, directly or not (its
// non-test files only).
func deps(t *testing.T, root, pkg string, seen map[string]bool) {
	t.Helper()
	if seen[pkg] {
		return
	}
	seen[pkg] = true
	files, _ := filepath.Glob(filepath.Join(root, strings.TrimPrefix(pkg, module), "*.go"))
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		ast, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, im := range ast.Imports {
			p, _ := strconv.Unquote(im.Path.Value)
			if strings.HasPrefix(p, module) {
				deps(t, root, p, seen)
			}
		}
	}
}

// The single-token decision's compensation: read, lifecycle and verify are built
// from packages with no GitLab write client compiled in; only send, maintain and
// rotate import it.
func TestReadCannotWrite(t *testing.T) {
	root, _ := filepath.Abs("../..")
	writer := module + "internal/gitlabwrite"
	for _, p := range []string{"internal/mirror", "internal/lifecycle", "internal/verify", "internal/bridge", "internal/gitlab", "internal/github", "internal/register"} {
		seen := map[string]bool{}
		deps(t, root, module+p, seen)
		if seen[writer] {
			t.Errorf("%s imports the GitLab write client", p)
		}
	}
	var importers []string
	dirs, _ := filepath.Glob(filepath.Join(root, "internal", "*"))
	for _, d := range append(dirs, filepath.Join(root, "cmd", "upstream")) {
		rel, _ := filepath.Rel(root, d)
		files, _ := filepath.Glob(filepath.Join(d, "*.go"))
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			src, _ := os.ReadFile(f)
			if strings.Contains(string(src), `"`+writer+`"`) {
				importers = append(importers, rel)
				break
			}
		}
	}
	sort.Strings(importers)
	if strings.Join(importers, " ") != "internal/maintain internal/rotate internal/send" {
		t.Errorf("only send, maintain and rotate import the write client: %v", importers)
	}
}

type harness struct {
	fl  *fake.GitLab
	fg  *fake.GitHub
	env map[string]string
	out bytes.Buffer
	err bytes.Buffer
	dir string
}

func newHarness(t *testing.T) *harness {
	h := &harness{fl: fake.NewGitLab(), fg: fake.NewGitHub(), env: map[string]string{"GITHUB_REPOSITORY": "o/r"}, dir: t.TempDir()}
	if err := os.WriteFile(filepath.Join(h.dir, "tracked.toml"), []byte(fake.Register), 0o644); err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *harness) run(args ...string) int {
	h.out.Reset()
	h.err.Reset()
	return run(args, env{
		Getenv: func(k string) string { return h.env[k] }, GitHub: h.fg.Transport, GitLab: h.fl.Transport,
		Now:   func() time.Time { return time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC) },
		Shell: nil, Fetch: nil, Encrypt: func(_, v string) (string, error) { return "x", nil },
		Stdout: &h.out, Stderr: &h.err, Workdir: h.dir, WriteFiles: true,
	})
}

// A test asserts the read path issues GET only, with the token set as in the workflows.
func TestReadPathsOnlyGET(t *testing.T) {
	h := newHarness(t)
	h.env["GITLAB_TOKEN"] = "glpat-x"
	h.env["BOT_TOKEN"] = "bot"
	h.fl.Data[fake.GitLabBase+"/api/v4/personal_access_tokens/self"] = map[string]any{"expires_at": "2026-12-01"}
	_ = os.MkdirAll(filepath.Join(h.dir, "results"), 0o755)
	_ = os.WriteFile(filepath.Join(h.dir, "results", "NS3-001.json"), []byte(`{"item": "NS3-001", "master": "m", "result": "reproduces", "detail": "d"}`), 0o644)
	for _, args := range [][]string{
		{"read"}, {"read"}, {"lifecycle"}, {"verify-record", "--results", "results", "--run-url", "https://github.com/o/r/actions/runs/1"},
		{"plan"}, {"read", "--dry-run"}, {"lifecycle", "--dry-run"},
	} {
		if code := h.run(args...); code != 0 {
			t.Fatalf("%v: exit %d: %s", args, code, h.err.String())
		}
	}
	for _, c := range h.fl.Calls {
		if c.Method != "GET" {
			t.Errorf("read, lifecycle, verify-record and plan send only GET to GitLab: %s %s", c.Method, c.URL)
		}
	}
	if len(h.fg.Issues) != 3 || !strings.Contains(h.fg.Issues[3]["title"].(string), "[P7] the GitLab token expires on 2026-12-01") {
		t.Errorf("read opened the tickets and the expiry warning")
	}
}

func TestCommandLine(t *testing.T) {
	h := newHarness(t)
	if h.run("validate") != 0 || h.out.String() != "tracked.toml: 2 item(s), valid\n" {
		t.Errorf("validate: %q", h.out.String())
	}
	if h.run("nope") != 2 || h.run() != 2 || h.run("verify") != 2 {
		t.Error("an unknown command or a missing argument is a usage error")
	}
	if h.run("validate", "--tracked", "../x.toml") != 1 || !strings.Contains(h.err.String(), "outside") {
		t.Errorf("the register stays inside the working directory: %s", h.err.String())
	}
	_ = os.MkdirAll(filepath.Join(h.dir, "replies/NS3-003"), 0o755)
	_ = os.WriteFile(filepath.Join(h.dir, "replies/NS3-003/1.md"), []byte("see #2\n"), 0o644)
	if h.run("lint") != 1 || !strings.Contains(h.out.String(), "reports/, replies/: 1 problem(s)\n") || h.err.Len() != 0 {
		t.Errorf("lint fails on a problem: %q %q", h.out.String(), h.err.String())
	}
	outFile := filepath.Join(h.dir, "out")
	h.env["GITHUB_OUTPUT"] = outFile
	if h.run("verify-matrix") != 0 || h.out.String() != "verify: nothing\n\n" {
		t.Errorf("verify-matrix: %q", h.out.String())
	}
	if data, _ := os.ReadFile(outFile); string(data) != "items=[]\n" {
		t.Errorf("verify-matrix's output: %q", data)
	}
	h.env["GITHUB_STEP_SUMMARY"] = filepath.Join(h.dir, "summary")
	if h.run("plan", "--dry-run", "--now", "2026-11-16T12:00:00Z") != 0 || h.out.String() != "nothing to send\n\ndry run: 0 GitHub write(s) not sent\n" {
		t.Errorf("plan: %q", h.out.String())
	}
	if data, _ := os.ReadFile(outFile); !strings.HasSuffix(string(data), "ready=false\n") {
		t.Errorf("plan's output: %q", data)
	}
	if h.run("send", "--dry-run") != 0 || !strings.HasPrefix(h.out.String(), "nothing ready\n") {
		t.Errorf("send: %q", h.out.String())
	}
	if h.run("rotate", "--dry-run") != 0 || h.out.String() != "P7: skipped: no GitLab token in this environment (the one-time bootstrap)\ndry run: 0 GitHub write(s) not sent\n" {
		t.Errorf("rotate: %q", h.out.String())
	}
	if h.run("maintain", "--dry-run") != 0 || !strings.Contains(h.out.String(), "nothing to maintain") {
		t.Errorf("maintain: %q", h.out.String())
	}
	if h.run("read", "--now", "yesterday") != 1 {
		t.Error("a bad --now is refused")
	}
	h.fl = fake.NewGitLab()
	h.fl.Data = map[string]any{}
	if h.run("read") != 1 || !strings.Contains(h.err.String(), "HTTP 404") {
		t.Errorf("an HTTP error fails the run: %q", h.err.String())
	}
}

func TestVerifyCommand(t *testing.T) {
	h := newHarness(t)
	reg := fake.Register + "[item.reproduce]\nmodules = \"core\"\nrun = \"run\"\nexpect = \"exit 0\"\n"
	_ = os.WriteFile(filepath.Join(h.dir, "tracked.toml"), []byte(reg), 0o644)
	ns3 := filepath.Join(h.dir, "ns3")
	_ = os.MkdirAll(ns3, 0o755)
	code := run([]string{"verify", "NS3-001", "--ns3", "ns3"}, env{
		Getenv: func(string) string { return "" }, Stdout: &h.out, Stderr: &h.err, Workdir: h.dir,
		Shell: func(string) verify.Shell {
			return func(string, time.Duration) verify.Proc { return verify.Proc{} }
		},
	})
	if code != 0 || !strings.HasPrefix(h.out.String(), "NS3-001: reproduces on master ") {
		t.Errorf("verify: %d %q %q", code, h.out.String(), h.err.String())
	}
}
