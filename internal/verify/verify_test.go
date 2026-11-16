// Copyright (c) 2026 Daniel Joppi (https://github.com/swarm-routing)
// SPDX-License-Identifier: MIT OR GPL-2.0-only

package verify

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/swarm-routing/ns3-upstream-report/internal/bridge"
	"github.com/swarm-routing/ns3-upstream-report/internal/fake"
	"github.com/swarm-routing/ns3-upstream-report/internal/github"
	"github.com/swarm-routing/ns3-upstream-report/internal/gitlab"
	"github.com/swarm-routing/ns3-upstream-report/internal/mirror"
	"github.com/swarm-routing/ns3-upstream-report/internal/register"
)

var now = time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)

const reg = `
gitlab_project = "nsnam/ns-3-dev"
[[item]]
id = "NS3-001"
state = "draft"
title = "t"
[item.reproduce]
modules = "core;network;internet;aodv"
files = { "scratch/c/c.cc" = "https://raw.githubusercontent.com/swarm-routing/ns3-containers/cccccccccccccccccccccccccccccccccccccccc/test/fixtures/manet-canary/manet-canary.cc" }
run = "timeout 120 ./ns3 run --no-build 'c --protocol=aodv'"
expect = "exit 124"
`

func TestVerify(t *testing.T) {
	m, err := register.Parse(reg)
	if err != nil {
		t.Fatal(err)
	}
	if errs := register.Validate(m); len(errs) > 0 {
		t.Fatal(errs)
	}
	cfg := register.FromRaw(m)
	ns3 := t.TempDir()
	var ran []string
	shellFor := func(runRC int, fail string) Shell {
		return func(cmd string, _ time.Duration) Proc {
			ran = append(ran, cmd)
			if strings.HasPrefix(cmd, "git rev-parse") {
				return Proc{0, "8ec9b4a190b52dadb5a3c35572dec687e1b75379\n", ""}
			}
			if fail != "" && strings.HasPrefix(cmd, fail) {
				return Proc{2, "", "boom"}
			}
			if cmd == cfg.Items[0].Reproduce.Run {
				return Proc{runRC, "", ""}
			}
			return Proc{0, "", ""}
		}
	}
	fetch := func(string) ([]byte, error) { return []byte("// code\n"), nil }
	got, err := Run(cfg, "NS3-001", ns3, fetch, shellFor(124, ""), ns3, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	_, e1 := os.Stat(filepath.Join(ns3, "scratch/c/c.cc"))
	data, e2 := os.ReadFile(filepath.Join(ns3, "results", "NS3-001.json"))
	conf := false
	for _, c := range ran {
		conf = conf || strings.Contains(c, "--enable-modules='core;network;internet;aodv'")
	}
	if got.Result != "reproduces" || !strings.HasPrefix(got.Master, "8ec9b4a1") || e1 != nil || e2 != nil || !conf {
		t.Errorf("P1: the reproducer runs in the tree, its expected exit is 'reproduces': %+v", got)
	}
	if string(data) != `{"item": "NS3-001", "master": "8ec9b4a190b52dadb5a3c35572dec687e1b75379", "result": "reproduces", "detail": "`+"`timeout 120 ./ns3 run --no-build 'c --protocol=aodv'` exited 124"+`"}` {
		t.Errorf("the result file is json.dump's: %s", data)
	}
	if back, _ := ParseResult(data); back != got {
		t.Error("a result reads back")
	}
	miss, _ := Run(cfg, "NS3-001", ns3, fetch, shellFor(0, ""), ns3, io.Discard)
	if miss.Result != "not-reproduced" {
		t.Error("P1: another exit is 'not-reproduced'")
	}
	ran = nil
	bad, _ := Run(cfg, "NS3-001", ns3, fetch, shellFor(0, "./ns3 build"), ns3, io.Discard)
	for _, c := range ran {
		if c == cfg.Items[0].Reproduce.Run {
			t.Error("P1: after a failed build the run is not attempted")
		}
	}
	if bad.Result != "error" || !strings.Contains(bad.Detail, "`./ns3 build` exited 2: boom") {
		t.Errorf("P1: a failed build is 'error': %+v", bad)
	}
	// output-kind expectation
	cfg.Items[0].Reproduce.Expect = "output DIFFERS"
	out := func(cmd string, _ time.Duration) Proc {
		if cmd == cfg.Items[0].Reproduce.Run {
			return Proc{1, "x\nDIFFERS\n", ""}
		}
		return Proc{0, "abc\n", ""}
	}
	if g, _ := Run(cfg, "NS3-001", ns3, fetch, out, ns3, io.Discard); g.Result != "reproduces" {
		t.Error("P1: an expected output is 'reproduces'")
	}
	// the record
	m2, _ := register.Parse(fake.Register)
	rcfg := register.FromRaw(m2)
	fl, fg := fake.NewGitLab(), fake.NewGitHub()
	gh := github.New("o/r", "t", fg.Transport, false)
	mirror.CmdRead(rcfg, gitlab.New("nsnam/ns-3-dev", "", fl.Transport), gh, now)
	lines, err := Record(rcfg, gh, []Result{got}, "https://github.com/o/r/actions/runs/9", now)
	if err != nil {
		t.Fatal(err)
	}
	t1 := fg.Ticket("NS3-001")
	ev := bridge.Evidence(rcfg, rcfg.Items[1], fg.Comments[t1])
	if len(lines) != 1 || lines[0] != "NS3-001: recorded reproduces on #2" || ev["date"] != "2026-11-16" || !strings.HasPrefix(ev["master"], "8ec9b4a1") {
		t.Errorf("P1: the result is recorded on the ticket and becomes the evidence plan reads: %v %v", lines, ev)
	}
}

func TestRealShell(t *testing.T) {
	p := RealShell(t.TempDir())("echo out; echo err >&2; exit 3", 10*time.Second)
	if p.Code != 3 || p.Stdout != "out\n" || p.Stderr != "err\n" {
		t.Errorf("the shell: %+v", p)
	}
}
