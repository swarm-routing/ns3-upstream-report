// Copyright (c) 2026 Daniel Joppi (https://github.com/swarm-routing)
// SPDX-License-Identifier: MIT OR GPL-2.0-only

// Command upstream is the bridge between the org's mirror tickets and ns-3's GitLab
// (nsnam/ns-3-dev), docs/adr/0002-a-zero-manual-pipeline-to-ns-3s-gitlab.md.
//
//	upstream validate      [--tracked FILE]
//	upstream lint          [--tracked FILE] [--root DIR]
//	upstream read          [--tracked FILE] [--dry-run] [--now ISO-8601]   P2
//	upstream plan          [--tracked FILE] [--root DIR] [--dry-run] [--now ISO-8601]
//	upstream send          [--tracked FILE] [--root DIR] [--dry-run] [--now ISO-8601]   P3, P4
//	upstream verify-matrix [--tracked FILE]
//	upstream verify        [--tracked FILE] ITEM --ns3 DIR   P1
//	upstream verify-record [--tracked FILE] --results DIR --run-url URL [--dry-run] [--now ISO-8601]
//	upstream lifecycle     [--tracked FILE] [--dry-run] [--now ISO-8601]   P6, and the register pull request after P3
//	upstream maintain      [--tracked FILE] [--root DIR] [--dry-run] [--now ISO-8601]   P5
//	upstream rotate        [--tracked FILE] [--dry-run] [--now ISO-8601]   P7
//
// GitHub is reached with GITHUB_TOKEN (the workflow's, issues: write), other
// repositories with BOT_TOKEN (the org App: P6's issues, P5's and the register's pull requests), the
// environment secrets with APP_TOKEN (the org App, P7), and GitLab with GITLAB_TOKEN
// (scope `api`, in both environments). No token is ever printed, logged or put in a
// URL. read, lifecycle and verify only read GitLab: the write client
// (internal/gitlabwrite) is built only for send, maintain and rotate. Output: one
// summary line per action (AntHocNet ADR-0014). Paths are relative to the working
// directory (the repository root in the workflows).
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/swarm-routing/ns3-upstream-report/internal/bridge"
	"github.com/swarm-routing/ns3-upstream-report/internal/github"
	"github.com/swarm-routing/ns3-upstream-report/internal/gitlab"
	"github.com/swarm-routing/ns3-upstream-report/internal/httpx"
	"github.com/swarm-routing/ns3-upstream-report/internal/lifecycle"
	"github.com/swarm-routing/ns3-upstream-report/internal/maintain"
	"github.com/swarm-routing/ns3-upstream-report/internal/mirror"
	"github.com/swarm-routing/ns3-upstream-report/internal/py"
	"github.com/swarm-routing/ns3-upstream-report/internal/record"
	"github.com/swarm-routing/ns3-upstream-report/internal/register"
	"github.com/swarm-routing/ns3-upstream-report/internal/rotate"
	"github.com/swarm-routing/ns3-upstream-report/internal/send"
	"github.com/swarm-routing/ns3-upstream-report/internal/verify"
)

// env is what the command reads from its environment and the network; tests replace it.
type env struct {
	Getenv     func(string) string
	GitHub     httpx.Transport
	GitLab     httpx.Transport
	Now        func() time.Time
	Shell      func(dir string) verify.Shell
	Fetch      verify.Fetch
	Encrypt    rotate.Encrypt
	Stdout     io.Writer
	Stderr     io.Writer
	Workdir    string
	WriteFiles bool
}

func main() {
	wd, _ := os.Getwd()
	os.Exit(run(os.Args[1:], env{
		Getenv: os.Getenv, GitHub: httpx.Real, GitLab: httpx.Real,
		Now:   func() time.Time { return time.Now().UTC() },
		Shell: verify.RealShell, Fetch: verify.RealFetch, Encrypt: rotate.SealedBox,
		Stdout: os.Stdout, Stderr: os.Stderr, Workdir: wd, WriteFiles: true,
	}))
}

var commands = []string{"validate", "lint", "read", "plan", "send", "verify-matrix", "verify", "verify-record", "lifecycle", "maintain", "rotate"}

func usage(w io.Writer) {
	fmt.Fprintf(w, "usage: upstream {%s} [options]\n", strings.Join(commands, ","))
}

func run(args []string, e env) int {
	if err := runErr(args, e); err != nil {
		var x *register.ExitError
		if errors.As(err, &x) {
			if x.Msg != "" {
				fmt.Fprintln(e.Stderr, x.Msg)
			}
		} else if !errors.Is(err, errUsage) {
			fmt.Fprintln(e.Stderr, "upstream: "+err.Error())
		}
		if errors.Is(err, errUsage) {
			return 2
		}
		return 1
	}
	return 0
}

var errUsage = errors.New("usage")

type opts struct {
	tracked, root, now, ns3, results, runURL string
	dry                                      bool
	pos                                      []string
}

func parse(cmd string, args []string, stderr io.Writer) (*opts, error) {
	o := &opts{}
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&o.tracked, "tracked", "tracked.toml", "the register")
	if cmd == "lint" || cmd == "plan" || cmd == "send" || cmd == "maintain" {
		fs.StringVar(&o.root, "root", ".", "the repository root")
	}
	switch cmd {
	case "read", "plan", "send", "verify-record", "lifecycle", "maintain", "rotate":
		fs.BoolVar(&o.dry, "dry-run", false, "write nothing; print the writes held back")
		fs.StringVar(&o.now, "now", "", "the time (ISO 8601), for tests")
	}
	if cmd == "verify" {
		fs.StringVar(&o.ns3, "ns3", "", "the ns-3-dev tree")
	}
	if cmd == "verify-record" {
		fs.StringVar(&o.results, "results", "", "the directory of results")
		fs.StringVar(&o.runURL, "run-url", "", "this run")
	}
	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			return nil, errUsage
		}
		if fs.NArg() == 0 {
			break
		}
		o.pos = append(o.pos, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	want := 0
	if cmd == "verify" {
		want = 1
	}
	if len(o.pos) != want || (cmd == "verify" && o.ns3 == "") || (cmd == "verify-record" && (o.results == "" || o.runURL == "")) {
		fmt.Fprintf(stderr, "upstream %s: wrong arguments\n", cmd)
		return nil, errUsage
	}
	return o, nil
}

func writeOutput(e env, outputs [][2]string, summary string) error {
	if e.WriteFiles {
		if p := e.Getenv("GITHUB_OUTPUT"); p != "" {
			var b strings.Builder
			for _, kv := range outputs {
				b.WriteString(kv[0] + "=" + kv[1] + "\n")
			}
			if err := appendFile(p, b.String()); err != nil {
				return err
			}
		}
		if p := e.Getenv("GITHUB_STEP_SUMMARY"); p != "" {
			if err := appendFile(p, summary); err != nil {
				return err
			}
		}
	}
	fmt.Fprintln(e.Stdout, summary)
	return nil
}

func appendFile(p, s string) error {
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(s); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

var resultName = regexp.MustCompile(`^NS3-[0-9]{3}\.json$`)

func runErr(args []string, e env) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		usage(e.Stdout)
		if len(args) == 0 {
			return errUsage
		}
		return nil
	}
	cmd := args[0]
	known := false
	for _, c := range commands {
		known = known || c == cmd
	}
	if !known {
		usage(e.Stderr)
		return errUsage
	}
	o, err := parse(cmd, args[1:], e.Stderr)
	if err != nil {
		return err
	}
	cfg, err := register.Load(o.tracked, e.Workdir)
	if err != nil {
		return err
	}
	out := e.Stdout
	switch cmd {
	case "validate":
		fmt.Fprintf(out, "tracked.toml: %d item(s), valid\n", len(cfg.Items))
		return nil
	case "lint":
		problems, err := bridge.LintFiles(cfg, abs(e, o.root))
		if err != nil {
			return err
		}
		for _, p := range problems {
			fmt.Fprintln(out, p)
		}
		fmt.Fprintf(out, "reports/, replies/: %d problem(s)\n", len(problems))
		if len(problems) > 0 {
			return &register.ExitError{Msg: ""}
		}
		return nil
	case "verify-matrix":
		var ids []string
		for _, it := range cfg.Items {
			if it.Reproduce != nil && it.State != "closed" {
				ids = append(ids, it.ID)
			}
		}
		l := strings.Join(ids, ", ")
		if l == "" {
			l = "nothing"
		}
		if ids == nil {
			ids = []string{}
		}
		return writeOutput(e, [][2]string{{"items", py.Dumps(ids, true)}}, "verify: "+l+"\n")
	case "verify":
		ns3 := abs(e, o.ns3)
		got, err := verify.Run(cfg, o.pos[0], ns3, e.Fetch, e.Shell(ns3), e.Workdir, out)
		if err != nil {
			return err
		}
		if got.Result == "error" {
			return &register.ExitError{Msg: ""}
		}
		return nil
	}
	repo := e.Getenv("GITHUB_REPOSITORY")
	if repo == "" {
		repo = "swarm-routing/ns3-upstream-report"
	}
	gh := github.New(repo, e.Getenv("GITHUB_TOKEN"), e.GitHub, o.dry)
	gl := gitlab.New(cfg.GitLabProject, e.Getenv("GITLAB_TOKEN"), e.GitLab)
	now := e.Now()
	if o.now != "" {
		if now, err = bridge.ParseTime(o.now); err != nil {
			return err
		}
	}
	var lines []string
	switch cmd {
	case "read":
		if lines, err = mirror.CmdRead(cfg, gl, gh, now); err != nil {
			return err
		}
		more, err := mirror.CheckExpiry(gl, gh, now)
		if err != nil {
			return err
		}
		lines = append(lines, more...)
	case "plan":
		l, ready, err := bridge.CmdPlan(cfg, gh, abs(e, o.root), now)
		if err != nil {
			return err
		}
		r := "false"
		if ready {
			r = "true"
		}
		if err := writeOutput(e, [][2]string{{"ready", r}}, strings.Join(l, "\n")+"\n"); err != nil {
			return err
		}
	case "send":
		if lines, err = send.CmdSend(cfg, gh, send.Writer(gl, o.dry), abs(e, o.root), now); err != nil {
			return err
		}
	case "verify-record":
		base, err := register.Contained(e.Workdir, o.results)
		if err != nil {
			return err
		}
		ents, err := os.ReadDir(base)
		if err != nil {
			return err
		}
		var results []verify.Result
		for _, en := range ents {
			if !resultName.MatchString(en.Name()) {
				continue
			}
			r, err := readResult(base, en.Name())
			if err != nil {
				return err
			}
			results = append(results, r)
		}
		if lines, err = verify.Record(cfg, gh, results, o.runURL, now); err != nil {
			return err
		}
	case "lifecycle":
		var org *github.Client
		if t := e.Getenv("BOT_TOKEN"); t != "" {
			org = github.New(repo, t, e.GitHub, o.dry)
		}
		if lines, err = lifecycle.CmdLifecycle(cfg, gl, gh, org, now); err != nil {
			return err
		}
		more, err := record.CmdRecord(cfg, gh, org)
		if err != nil {
			return err
		}
		lines = append(lines, more...)
	case "maintain":
		var bot *github.Client
		if t := e.Getenv("BOT_TOKEN"); t != "" {
			bot = github.New(repo, t, e.GitHub, o.dry)
		}
		m := &maintain.Maintainer{Cfg: cfg, GH: gh, GW: send.Writer(gl, o.dry), Bot: bot, Root: abs(e, o.root), Now: now}
		if lines, err = m.Run(); err != nil {
			return err
		}
	case "rotate":
		var app *github.Client
		if t := e.Getenv("APP_TOKEN"); t != "" {
			app = github.New(repo, t, e.GitHub, o.dry)
		}
		r := &rotate.Rotator{GW: send.Writer(gl, o.dry), App: app, GH: gh, Encrypt: e.Encrypt, Now: now, Dry: o.dry}
		if lines, err = r.Run(); err != nil {
			return err
		}
	}
	for _, l := range lines {
		fmt.Fprintln(out, l)
	}
	if o.dry {
		fmt.Fprintf(out, "dry run: %d GitHub write(s) not sent\n", len(gh.Writes))
	}
	return nil
}

func abs(e env, p string) string {
	if p == "" || strings.HasPrefix(p, "/") {
		return p
	}
	return e.Workdir + "/" + p
}

func readResult(base, name string) (verify.Result, error) {
	p, err := register.Contained(base, name)
	if err != nil {
		return verify.Result{}, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return verify.Result{}, err
	}
	return verify.ParseResult(data)
}
