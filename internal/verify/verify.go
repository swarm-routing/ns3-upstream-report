// Copyright (c) 2026 Daniel Joppi (https://github.com/swarm-routing)
// SPDX-License-Identifier: MIT OR GPL-2.0-only

// Package verify is P1: an item's reproducer run in an ns-3-dev master tree
// (`verify`), and each result recorded on its mirror ticket with the
// `upstream-verified` marker the plan reads (`verify-record`). It needs no GitLab.
package verify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/swarm-routing/ns3-upstream-report/internal/bridge"
	"github.com/swarm-routing/ns3-upstream-report/internal/github"
	"github.com/swarm-routing/ns3-upstream-report/internal/py"
	"github.com/swarm-routing/ns3-upstream-report/internal/register"
)

// Proc is a finished command.
type Proc struct {
	Code           int
	Stdout, Stderr string
}

// Shell runs a bash command in the ns-3 tree with a timeout.
type Shell func(cmd string, timeout time.Duration) Proc

// Fetch downloads a pinned file.
type Fetch func(url string) ([]byte, error)

// Result is one P1 result.
type Result struct {
	Item, Master, Result, Detail string
}

// JSON is the result as json.dump wrote it (ensure_ascii, key order item, master, result, detail).
func (r Result) JSON() string {
	return py.Dumps(py.Pairs{{"item", r.Item}, {"master", r.Master}, {"result", r.Result}, {"detail", r.Detail}}, true)
}

// RealShell runs bash -c in dir.
func RealShell(dir string) Shell {
	return func(cmd string, timeout time.Duration) Proc {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		c := exec.CommandContext(ctx, "/bin/bash", "-c", cmd)
		c.Dir = dir
		var o, e bytes.Buffer
		c.Stdout, c.Stderr = &o, &e
		err := c.Run()
		code := 0
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else if err != nil {
			code = 127
			e.WriteString(err.Error())
		}
		return Proc{code, o.String(), e.String()}
	}
}

// RealFetch downloads a URL (at most 16 MiB).
func RealFetch(url string) ([]byte, error) {
	c := &http.Client{Timeout: 60 * time.Second}
	r, err := c.Get(url)
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		return nil, fmt.Errorf("GET %s: HTTP %d", url, r.StatusCode)
	}
	return io.ReadAll(io.LimitReader(r.Body, 16<<20))
}

// Run puts the reproducer into the tree at ns3, configures, builds, runs; the result
// goes to <outBase>/results/<item>.json and one line to w.
func Run(cfg *register.Config, itemID, ns3 string, fetch Fetch, shell Shell, outBase string, w io.Writer) (Result, error) {
	var it *register.Item
	for _, i := range cfg.Items {
		if i.ID == itemID {
			it = i
		}
	}
	if it == nil || it.Reproduce == nil {
		return Result{}, register.Exit("%s: no reproduce table in tracked.toml", itemID)
	}
	r := it.Reproduce
	outDir := filepath.Join(outBase, "results") // the name comes from the register, never from an argument
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return Result{}, err
	}
	keys := make([]string, 0, len(r.Files))
	for k := range r.Files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, rel := range keys {
		p, err := register.Contained(ns3, rel)
		if err != nil {
			return Result{}, err
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return Result{}, err
		}
		data, err := fetch(r.Files[rel])
		if err != nil {
			return Result{}, err
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			return Result{}, err
		}
	}
	master := py.Strip(shell("git rev-parse HEAD", 60*time.Second).Stdout)
	var steps []string
	if r.Setup != "" {
		steps = append(steps, r.Setup)
	}
	steps = append(steps,
		"./ns3 configure -d default --enable-examples --enable-tests --disable-werror --enable-modules='"+r.Modules+"'",
		"./ns3 build")
	result, detail := "error", ""
	failed := false
	for _, step := range steps {
		p := shell(step, time.Hour)
		if p.Code != 0 {
			detail = fmt.Sprintf("`%s` exited %d: %s", step, p.Code, py.Tail(p.Stderr, 500))
			failed = true
			break
		}
	}
	if !failed {
		p := shell(r.Run, time.Hour)
		kind, want, _ := strings.Cut(r.Expect, " ")
		var seen bool
		if kind == "exit" {
			n, _ := strconv.Atoi(want)
			seen = p.Code == n
		} else {
			seen = strings.Contains(p.Stdout+p.Stderr, want)
		}
		result = "not-reproduced"
		if seen {
			result = "reproduces"
		}
		detail = fmt.Sprintf("`%s` exited %d", r.Run, p.Code)
	}
	got := Result{itemID, master, result, detail}
	if err := os.WriteFile(filepath.Join(outDir, it.ID+".json"), []byte(got.JSON()), 0o644); err != nil {
		return got, err
	}
	fmt.Fprintf(w, "%s: %s on master %s (%s)\n", itemID, result, py.Head(master, 12), detail)
	return got, nil
}

var verdicts = map[string]string{
	"reproduces":     "the bug **reproduces** on ns-3-dev `master`",
	"not-reproduced": "the bug **does not reproduce** on ns-3-dev `master`: check whether it was fixed upstream before anything is sent",
	"error":          "the reproducer **could not run**",
}

// Record comments each result on its mirror ticket, with the marker plan reads.
func Record(cfg *register.Config, gh *github.Client, results []Result, runURL string, now time.Time) ([]string, error) {
	issues, err := gh.Issues()
	if err != nil {
		return nil, err
	}
	var lines []string
	for _, got := range results {
		var it *register.Item
		for _, i := range cfg.Items {
			if i.ID == got.Item {
				it = i
			}
		}
		if it == nil {
			return nil, register.Exit("%s: not in tracked.toml", got.Item)
		}
		t, err := bridge.FindTicket(gh, cfg, it, issues)
		if err != nil {
			return nil, err
		}
		if t == nil {
			lines = append(lines, it.ID+": no mirror ticket yet")
			continue
		}
		day := now.Format("2006-01-02")
		v, ok := verdicts[got.Result]
		if !ok {
			return nil, register.Exit("%s: unknown result %q", got.Item, got.Result)
		}
		n := bridge.Number(t)
		if _, err := gh.AddComment(n, fmt.Sprintf("<!-- upstream-verified: date=%s master=%s run=%s result=%s -->\n**P1 verify** (%s): %s (commit `%s`; %s). [Run](%s).%s",
			day, got.Master, runURL, got.Result, day, v, py.Head(got.Master, 12), got.Detail, runURL, bridge.Footer)); err != nil {
			return nil, err
		}
		lines = append(lines, fmt.Sprintf("%s: recorded %s on #%d", it.ID, got.Result, n))
	}
	return lines, nil
}

// ParseResult is a result file.
func ParseResult(data []byte) (Result, error) {
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return Result{}, err
	}
	return Result{m["item"], m["master"], m["result"], m["detail"]}, nil
}
