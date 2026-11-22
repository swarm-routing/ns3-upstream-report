// Copyright (c) 2026 Daniel Joppi (https://github.com/swarm-routing)
// SPDX-License-Identifier: MIT OR GPL-2.0-only

// Package register reads and validates tracked.toml, the register of upstream
// reports and the allowlist of everything the bridge may send: one [[item]] per
// NS3-NNN id (swarm-routing/ns3-tools docs/ns3-bugs.md) with its RCAPA ticket, its
// state (draft, published, closed), its GitLab issue and merge requests
// (`merge_requests`: ours, live), the competing ones (`competing`: not ours, read
// only), the withdrawn ones (`withdrawn`: ours, closed on GitLab; read mirrors
// their notes and send may reply in their threads, but P5 never maintains them,
// the writer refuses their rebase, and P6 follows them only through the issue's
// closing merge requests), the mirror ticket, the P1 evidence
// (`verified = { date, master, run }`), the P1 reproducer (`reproduce`), the
// workaround issues and, optionally, its own `gate`. A merge request number is in
// at most one of the three lists. The top level sets the gate policy: `gate = "veto-window"` (default; a
// post goes out `window_hours_new_issue` = 48 h after it is scheduled for a new
// issue, `window_hours` = 24 h for a note), "auto" or "approve" (as soon as it is
// scheduled).
package register

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/swarm-routing/ns3-upstream-report/internal/py"
)

// States, gates and the keys an item may have.
var (
	States   = []string{"draft", "published", "closed"}
	Gates    = []string{"veto-window", "auto", "approve"}
	ItemKeys = []string{"id", "title", "state", "rcapa", "issue", "merge_requests", "competing",
		"withdrawn", "mirror", "draft", "verified", "gate", "reproduce", "workarounds"}
)

// Patterns shared with the rest of the bridge.
var (
	ID    = regexp.MustCompile(`^NS3-[0-9]{3}$`)
	GHRef = regexp.MustCompile(`^swarm-routing/[A-Za-z0-9_.-]+#[0-9]+$`)
	proj  = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	date  = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)
	mods  = regexp.MustCompile(`^[a-z0-9-]+(;[a-z0-9-]+)*$`)
	fileK = regexp.MustCompile(`^(scratch|contrib)/[A-Za-z0-9_./-]+$`)
	raw   = regexp.MustCompile(`^https://raw\.githubusercontent\.com/swarm-routing/[A-Za-z0-9_.-]+/[0-9a-f]{40}/[^` + py.SpaceClass + `]+$`)
	exp   = regexp.MustCompile(`^(exit [0-9]+|output .+)$`)
)

// Reproduce is an item's P1 reproducer.
type Reproduce struct {
	Modules, Setup, Run, Expect string
	Files                       map[string]string
}

// Item is one [[item]].
type Item struct {
	ID, Title, State, RCAPA, Gate string
	Issue, Mirror                 int
	MergeRequests, Competing      []int
	Withdrawn                     []int // ours, closed: read and replied to, never maintained
	Draft, Workarounds            []string
	Verified                      map[string]string
	Reproduce                     *Reproduce
}

// Config is the register.
type Config struct {
	GitLabProject       string
	OurAccounts         []string
	Bot                 string
	Gate                string
	WindowHours         int
	WindowHoursNewIssue int
	Items               []*Item
}

// ExitError is a refusal the command line prints and exits 1 on (Python's SystemExit).
type ExitError struct{ Msg string }

func (e *ExitError) Error() string { return e.Msg }

// Exit is a refusal with a message.
func Exit(format string, a ...any) error { return &ExitError{fmt.Sprintf(format, a...)} }

// Contained is path resolved, refused unless it lies inside base (no traversal,
// no symlink out).
func Contained(base, path string) (string, error) {
	root, err := realpath(base)
	if err != nil {
		return "", err
	}
	full := path
	if !filepath.IsAbs(full) {
		full = filepath.Join(root, path)
	}
	full, err = realpath(full)
	if err != nil {
		return "", err
	}
	if full != root && !strings.HasPrefix(full, root+string(filepath.Separator)) {
		return "", Exit("refused: %s is outside %s", path, base)
	}
	return full, nil
}

// realpath is os.path.realpath: absolute, symlinks resolved as far as the path exists.
func realpath(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	rest := ""
	cur := abs
	for {
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			if rest == "" {
				return r, nil
			}
			return filepath.Join(r, rest), nil
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return abs, nil
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

// Load reads and validates the register at path, which must lie inside base.
func Load(path, base string) (*Config, error) {
	full, err := Contained(base, path)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return nil, err
	}
	rawCfg, err := Parse(string(data))
	if err != nil {
		return nil, err
	}
	if errs := Validate(rawCfg); len(errs) > 0 {
		return nil, Exit("tracked.toml: %s", strings.Join(errs, "; "))
	}
	return FromRaw(rawCfg), nil
}

// Parse is the TOML text as plain values: tables map[string]any, arrays []any,
// integers int64.
func Parse(text string) (map[string]any, error) {
	var m map[string]any
	if _, err := toml.Decode(text, &m); err != nil {
		return nil, err
	}
	return norm(m).(map[string]any), nil
}

func norm(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			x[k] = norm(e)
		}
		return x
	case []map[string]any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = norm(e)
		}
		return out
	case []any:
		for i, e := range x {
			x[i] = norm(e)
		}
		return x
	}
	return v
}

func isInt(v any) (int64, bool) {
	i, ok := v.(int64)
	return i, ok
}

func in(s any, list []string) bool {
	str, ok := s.(string)
	if !ok {
		return false
	}
	for _, x := range list {
		if x == str {
			return true
		}
	}
	return false
}

func getd(m map[string]any, k string, d any) any {
	if v, ok := m[k]; ok {
		return v
	}
	return d
}

func list(v any) ([]any, bool) {
	l, ok := v.([]any)
	return l, ok
}

// Validate is what is wrong with the register, in tracked.toml's order of checks.
func Validate(cfg map[string]any) []string {
	var errors []string
	if p, ok := cfg["gitlab_project"].(string); !ok || !proj.MatchString(p) {
		errors = append(errors, "gitlab_project is not a group/project path")
	}
	accounts, ok := list(getd(cfg, "our_accounts", []any{}))
	if ok {
		for _, a := range accounts {
			if _, s := a.(string); !s {
				ok = false
			}
		}
	}
	if !ok {
		errors = append(errors, "our_accounts is not a list of GitLab usernames")
	}
	if _, ok := getd(cfg, "bot", "github-actions[bot]").(string); !ok {
		errors = append(errors, "bot is not a GitHub login")
	}
	if !in(getd(cfg, "gate", "veto-window"), Gates) {
		errors = append(errors, "gate is not one of "+strings.Join(Gates, ", "))
	}
	for _, k := range []string{"window_hours", "window_hours_new_issue"} {
		if v, present := cfg[k]; present {
			if i, ok := isInt(v); !ok || i < 0 || i > 24*14 {
				errors = append(errors, k+" is not a number of hours from 0 to 336")
			}
		}
	}
	seen := map[string]bool{}
	items, _ := list(getd(cfg, "item", []any{}))
	for n, raw := range items {
		it, ok := raw.(map[string]any)
		where := fmt.Sprintf("item %d", n+1)
		if ok {
			if id, present := it["id"]; present {
				where = py.Str(id)
			}
		}
		if !ok {
			errors = append(errors, where+" is not a table")
			continue
		}
		var unknown []string
		for k := range it {
			if !in(k, ItemKeys) {
				unknown = append(unknown, k)
			}
		}
		sort.Strings(unknown)
		for _, k := range unknown {
			errors = append(errors, fmt.Sprintf("%s: %s is not one of %s", where, k, strings.Join(ItemKeys, ", ")))
		}
		id, isStr := it["id"].(string)
		if !isStr || !ID.MatchString(id) {
			errors = append(errors, where+": id is not NS3-NNN")
		} else if seen[id] {
			errors = append(errors, where+": id listed twice")
		}
		if isStr {
			seen[id] = true
		}
		if t, ok := it["title"].(string); !ok || py.Strip(t) == "" {
			errors = append(errors, where+": title is missing")
		}
		if !in(it["state"], States) {
			errors = append(errors, where+": state is not one of "+strings.Join(States, ", "))
		}
		if v, present := it["rcapa"]; present {
			if s, ok := v.(string); !ok || !GHRef.MatchString(s) {
				errors = append(errors, where+": rcapa is not swarm-routing/<repo>#N")
			}
		}
		for _, k := range []string{"issue", "mirror"} {
			if v, present := it[k]; present {
				if i, ok := isInt(v); !ok || i < 1 {
					errors = append(errors, fmt.Sprintf("%s: %s is not an issue number", where, k))
				}
			}
		}
		role := map[int64]string{}
		for _, k := range []string{"merge_requests", "competing", "withdrawn"} {
			l, ok := list(getd(it, k, []any{}))
			for _, x := range l {
				i, isI := isInt(x)
				if !isI || i <= 0 {
					ok = false
					continue
				}
				if r, dup := role[i]; dup && r != k {
					errors = append(errors, fmt.Sprintf("%s: !%d is in both %s and %s", where, i, r, k))
				}
				role[i] = k
			}
			if !ok {
				errors = append(errors, fmt.Sprintf("%s: %s is not a list of merge request numbers", where, k))
			}
		}
		if !refList(getd(it, "draft", []any{})) {
			errors = append(errors, where+": draft is not a list of swarm-routing/<repo>#N")
		}
		if r, present := it["reproduce"]; present {
			errors = append(errors, ValidateReproduce(where, r)...)
		}
		if !refList(getd(it, "workarounds", []any{})) {
			errors = append(errors, where+": workarounds is not a list of swarm-routing/<repo>#N")
		}
		if g, present := it["gate"]; present && !in(g, Gates) {
			errors = append(errors, where+": gate is not one of "+strings.Join(Gates, ", "))
		}
		if !validVerified(getd(it, "verified", map[string]any{})) {
			errors = append(errors, where+": verified is not { date = YYYY-MM-DD, master, run = URL }")
		}
		if it["state"] == "published" && !(py.Truthy(it["issue"]) || py.Truthy(it["merge_requests"])) {
			errors = append(errors, where+": published without an issue or a merge request")
		}
	}
	return errors
}

func refList(v any) bool {
	l, ok := list(v)
	if !ok {
		return false
	}
	for _, x := range l {
		if s, isS := x.(string); !isS || !GHRef.MatchString(s) {
			return false
		}
	}
	return true
}

func validVerified(v any) bool {
	m, ok := v.(map[string]any)
	if !ok {
		return false
	}
	if len(m) == 0 {
		return true
	}
	for k := range m {
		if k != "date" && k != "master" && k != "run" {
			return false
		}
	}
	_, master := m["master"].(string)
	return date.MatchString(py.Str(getd(m, "date", ""))) && master &&
		strings.HasPrefix(py.Str(getd(m, "run", "")), "https://github.com/")
}

// ValidateReproduce is what is wrong with an item's `reproduce` table.
func ValidateReproduce(where string, v any) []string {
	r, ok := v.(map[string]any)
	if !ok {
		return []string{where + ": reproduce is not a table"}
	}
	var errors, unknown []string
	for k := range r {
		if !in(k, []string{"modules", "files", "setup", "run", "expect"}) {
			unknown = append(unknown, k)
		}
	}
	sort.Strings(unknown)
	for _, k := range unknown {
		errors = append(errors, fmt.Sprintf("%s: reproduce.%s is not one of modules, files, setup, run, expect", where, k))
	}
	if !mods.MatchString(py.Str(getd(r, "modules", ""))) {
		errors = append(errors, where+": reproduce.modules is not a ;-separated list of ns-3 modules")
	}
	files, ok := getd(r, "files", map[string]any{}).(map[string]any)
	if ok {
		for k, u := range files {
			if !fileK.MatchString(k) || strings.Contains(k, "..") || !raw.MatchString(py.Str(u)) {
				ok = false
			}
		}
	}
	if !ok {
		errors = append(errors, where+": reproduce.files maps scratch/ or contrib/ paths to raw.githubusercontent.com "+
			"swarm-routing URLs pinned to a commit")
	}
	if s, ok := r["run"].(string); !ok || py.Strip(s) == "" {
		errors = append(errors, where+": reproduce.run is missing")
	}
	if !exp.MatchString(py.Str(getd(r, "expect", ""))) {
		errors = append(errors, where+": reproduce.expect is `exit N` or `output TEXT`")
	}
	return errors
}

// FromRaw is a validated register as typed values.
func FromRaw(cfg map[string]any) *Config {
	c := &Config{
		Bot:                 "github-actions[bot]",
		Gate:                "veto-window",
		WindowHours:         24,
		WindowHoursNewIssue: 48,
	}
	c.GitLabProject, _ = cfg["gitlab_project"].(string)
	if l, ok := cfg["our_accounts"].([]any); ok {
		for _, a := range l {
			c.OurAccounts = append(c.OurAccounts, a.(string))
		}
	}
	if b, ok := cfg["bot"].(string); ok {
		c.Bot = b
	}
	if g, ok := cfg["gate"].(string); ok {
		c.Gate = g
	}
	if v, ok := cfg["window_hours"].(int64); ok {
		c.WindowHours = int(v)
	}
	if v, ok := cfg["window_hours_new_issue"].(int64); ok {
		c.WindowHoursNewIssue = int(v)
	}
	items, _ := cfg["item"].([]any)
	for _, raw := range items {
		it := raw.(map[string]any)
		item := &Item{}
		item.ID, _ = it["id"].(string)
		item.Title, _ = it["title"].(string)
		item.State, _ = it["state"].(string)
		item.RCAPA, _ = it["rcapa"].(string)
		item.Gate, _ = it["gate"].(string)
		if v, ok := it["issue"].(int64); ok {
			item.Issue = int(v)
		}
		if v, ok := it["mirror"].(int64); ok {
			item.Mirror = int(v)
		}
		item.MergeRequests = ints(it["merge_requests"])
		item.Competing = ints(it["competing"])
		item.Withdrawn = ints(it["withdrawn"])
		item.Draft = strs(it["draft"])
		item.Workarounds = strs(it["workarounds"])
		if v, ok := it["verified"].(map[string]any); ok && len(v) > 0 {
			item.Verified = map[string]string{}
			for k, x := range v {
				item.Verified[k] = py.Str(x)
			}
		}
		if r, ok := it["reproduce"].(map[string]any); ok {
			rep := &Reproduce{Files: map[string]string{}}
			rep.Modules = py.Str(getd(r, "modules", ""))
			rep.Setup, _ = r["setup"].(string)
			rep.Run, _ = r["run"].(string)
			rep.Expect = py.Str(getd(r, "expect", ""))
			if f, ok := r["files"].(map[string]any); ok {
				for k, u := range f {
					rep.Files[k] = py.Str(u)
				}
			}
			item.Reproduce = rep
		}
		c.Items = append(c.Items, item)
	}
	return c
}

func ints(v any) []int {
	l, _ := v.([]any)
	var out []int
	for _, x := range l {
		out = append(out, int(x.(int64)))
	}
	return out
}

func strs(v any) []string {
	l, _ := v.([]any)
	var out []string
	for _, x := range l {
		out = append(out, x.(string))
	}
	return out
}

// Ours is whether a GitLab username is one of our accounts.
func (c *Config) Ours(user string) bool {
	for _, a := range c.OurAccounts {
		if a == user {
			return true
		}
	}
	return false
}

// HasMR is whether iid is one of the item's merge requests.
func (it *Item) HasMR(iid int) bool { return containsInt(it.MergeRequests, iid) }

// IsCompeting is whether iid is one of the item's competing merge requests.
func (it *Item) IsCompeting(iid int) bool { return containsInt(it.Competing, iid) }

// IsWithdrawn is whether iid is one of the item's withdrawn merge requests (ours, closed).
func (it *Item) IsWithdrawn(iid int) bool { return containsInt(it.Withdrawn, iid) }

func containsInt(l []int, x int) bool {
	for _, v := range l {
		if v == x {
			return true
		}
	}
	return false
}
