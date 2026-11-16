// Copyright (c) 2026 Daniel Joppi (https://github.com/swarm-routing)
// SPDX-License-Identifier: MIT OR GPL-2.0-only

// Package fake is the tests' GitLab and GitHub: a GitLab fed by the recorded public
// pages of nsnam/ns-3-dev issue 1364 and merge requests 2985 and 3011 (testdata/,
// 2026-11-16), and a GitHub that keeps issues and comments in memory. Nothing here
// reaches a network; it is imported by tests only.
package fake

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/swarm-routing/ns3-upstream-report/internal/httpx"
)

// GitLabBase and API of the fixtures.
const (
	GitLabBase = "https://gitlab.com"
	API        = GitLabBase + "/api/v4/projects/nsnam%2Fns-3-dev"
	GitHubAPI  = "https://api.github.com"
)

func dir() string {
	_, f, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(f), "testdata")
}

// Load is a fixture, decoded (numbers as json.Number).
func Load(name string) any {
	data, err := os.ReadFile(filepath.Join(dir(), name))
	if err != nil {
		panic(err)
	}
	return Decode(data)
}

// Decode is JSON as the clients see it.
func Decode(data []byte) any {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		panic(err)
	}
	return v
}

// Copy is a deep copy of a decoded value.
func Copy(v any) any {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return Decode(data)
}

func resp(status int, v any, h map[string]string) (httpx.Response, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return httpx.Response{}, err
	}
	if h == nil {
		h = map[string]string{}
	}
	return httpx.Response{Status: status, Raw: raw, Header: h}, nil
}

// Call is one request a fake saw.
type Call struct {
	Method, URL string
	Token       bool
	Body        any
}

// GitLab is the fake GitLab.
type GitLab struct {
	Data    map[string]any
	Calls   []Call
	Created []map[string]any
	Patches map[string]string // raw .patch pages
}

// NewGitLab is the fake fed by the fixtures.
func NewGitLab() *GitLab {
	return &GitLab{Data: map[string]any{
		API + "/issues/1364":                                                  Load("issue-1364.json"),
		API + "/merge_requests/2985":                                          Load("mr-2985.json"),
		API + "/merge_requests/2985/approvals":                                Load("mr-2985-approvals.json"),
		API + "/merge_requests/3011":                                          Load("mr-3011.json"),
		GitLabBase + "/nsnam/ns-3-dev/-/issues/1364/discussions.json":         Load("issue-1364-discussions.json"),
		GitLabBase + "/nsnam/ns-3-dev/-/merge_requests/2985/discussions.json": Load("mr-2985-discussions.json"),
		// the public page is cursor-paginated: its second page holds the newest notes
		GitLabBase + "/nsnam/ns-3-dev/-/merge_requests/2985/discussions.json?cursor=c2": Load("mr-2985-discussions-2.json"),
		GitLabBase + "/nsnam/ns-3-dev/-/merge_requests/3011/discussions.json":           []any{},
	}, Patches: map[string]string{}}
}

// MR is merge request iid's fixture (a map to edit; Set it back).
func (g *GitLab) MR(iid int) map[string]any {
	return Copy(g.Data[fmt.Sprintf("%s/merge_requests/%d", API, iid)]).(map[string]any)
}

// SetMR replaces merge request iid.
func (g *GitLab) SetMR(iid int, m map[string]any) {
	g.Data[fmt.Sprintf("%s/merge_requests/%d", API, iid)] = m
}

// Discussions of a thread's first page (to append a note to).
func (g *GitLab) Discussions(kind string, iid int) string {
	p := "issues"
	if kind == "!" {
		p = "merge_requests"
	}
	return fmt.Sprintf("%s/nsnam/ns-3-dev/-/%s/%d/discussions.json", GitLabBase, p, iid)
}

// AddNote appends a note to a thread's first page.
func (g *GitLab) AddNote(kind string, iid int, id, author, body, created string) {
	k := g.Discussions(kind, iid)
	l, _ := g.Data[k].([]any)
	g.Data[k] = append(l, map[string]any{"notes": []any{map[string]any{
		"id": id, "system": false, "body": body, "created_at": created,
		"author": map[string]any{"username": author, "name": author},
	}}})
}

// PopNote removes the last discussion of a thread's first page.
func (g *GitLab) PopNote(kind string, iid int) {
	k := g.Discussions(kind, iid)
	l, _ := g.Data[k].([]any)
	g.Data[k] = l[:len(l)-1]
}

// Writes is every non-GET request.
func (g *GitLab) Writes() []Call {
	var out []Call
	for _, c := range g.Calls {
		if c.Method != "GET" {
			out = append(out, c)
		}
	}
	return out
}

var unauthorized = func(m, u string) error {
	return &httpx.Error{Method: m, URL: u, Status: 401, Message: "401 Unauthorized"}
}

// Transport is the fake's httpx.Transport.
func (g *GitLab) Transport(method, url string, headers map[string]string, body any) (httpx.Response, error) {
	_, tok := headers["PRIVATE-TOKEN"]
	g.Calls = append(g.Calls, Call{method, url, tok, Copy(body)})
	if method == "PUT" && strings.HasSuffix(url, "/rebase") {
		return resp(202, map[string]any{"rebase_in_progress": true}, nil)
	}
	if strings.HasSuffix(url, "/personal_access_tokens/self/rotate") {
		b := body.(map[string]any)
		return resp(200, map[string]any{"token": "glpat-NEWSECRET", "expires_at": b["expires_at"]}, nil)
	}
	if method == "POST" {
		if !tok {
			return httpx.Response{}, unauthorized(method, url)
		}
		b := Copy(body).(map[string]any)
		if strings.HasSuffix(url, "/issues") {
			iid := 1400 + len(g.Created)
			got := map[string]any{"iid": iid, "title": b["title"], "web_url": fmt.Sprintf("%s/nsnam/ns-3-dev/-/issues/%d", GitLabBase, iid)}
			g.Created = append(g.Created, got)
			st := Copy(got).(map[string]any)
			st["state"], st["labels"], st["updated_at"] = "opened", []any{}, "2026-11-18T13:00:00.000Z"
			g.Data[fmt.Sprintf("%s/issues/%d", API, iid)] = st
			g.Data[fmt.Sprintf("%s/nsnam/ns-3-dev/-/issues/%d/discussions.json", GitLabBase, iid)] = []any{}
			return resp(201, got, nil)
		}
		nid := strconv.Itoa(9000000 + len(g.Calls))
		disc := strings.SplitN(strings.Replace(url, API, GitLabBase+"/nsnam/ns-3-dev/-", 1), "/notes", 2)[0]
		l, _ := g.Data[disc+"/discussions.json"].([]any)
		g.Data[disc+"/discussions.json"] = append(l, map[string]any{"notes": []any{map[string]any{
			"id": nid, "system": false, "note": b["body"], "created_at": "2026-11-18T13:00:00.000Z",
			"author": map[string]any{"username": "danieljoppi", "name": "Daniel"},
		}}})
		n, _ := strconv.Atoi(nid)
		return resp(201, map[string]any{"id": n}, nil)
	}
	if strings.Contains(url, "/issues?search=") {
		return resp(200, Copy(g.Created), nil)
	}
	if strings.HasSuffix(url, ".patch") {
		if p, ok := g.Patches[url]; ok {
			return httpx.Response{Status: 200, Raw: []byte(p), Header: map[string]string{}}, nil
		}
	}
	if strings.HasSuffix(url, "/notes?sort=asc&order_by=created_at&per_page=100&page=1") {
		if !tok {
			return httpx.Response{}, unauthorized(method, url)
		}
		disc := strings.SplitN(strings.Replace(url, API, GitLabBase+"/nsnam/ns-3-dev/-", 1), "/notes?", 2)[0]
		var pages []any
		if l, ok := g.Data[disc+"/discussions.json"].([]any); ok {
			pages = append(pages, l...)
		}
		if l, ok := g.Data[disc+"/discussions.json?cursor=c2"].([]any); ok {
			pages = append(pages, l...)
		}
		var notes []any
		for _, d := range pages {
			for _, n := range d.(map[string]any)["notes"].([]any) {
				m := Copy(n).(map[string]any)
				id := fmt.Sprint(m["id"])
				if i, err := strconv.Atoi(id); err == nil {
					m["id"] = i
				}
				notes = append(notes, m)
			}
		}
		return resp(200, notes, nil)
	}
	v, ok := g.Data[url]
	if !ok {
		return httpx.Response{}, &httpx.Error{Method: method, URL: url, Status: 404, Message: "404 Not Found"}
	}
	var h map[string]string
	if _, next := g.Data[url+"?cursor=c2"]; next {
		h = map[string]string{"x-next-page-cursor": "c2"}
	}
	return resp(200, Copy(v), h)
}

// GitHub is the fake GitHub of one repository's issues and comments.
type GitHub struct {
	Issues   map[int]map[string]any
	Comments map[int][]map[string]any
	Next     int
	Clock    string // created_at of the comments it adds
	Calls    []Call
	FailAdd  error // returned by every comment POST while set
}

// NewGitHub is an empty repository.
func NewGitHub() *GitHub {
	return &GitHub{Issues: map[int]map[string]any{}, Comments: map[int][]map[string]any{}, Next: 1, Clock: "2026-11-16T12:00:00Z"}
}

var (
	issuesPath   = regexp.MustCompile(`^repos/[^/]+/[^/]+/issues(.*)$`)
	issueN       = regexp.MustCompile(`^/(\d+)$`)
	commentsPath = regexp.MustCompile(`^/(\d+)/comments(?:\?per_page=100&page=(\d+))?$`)
	pageRe       = regexp.MustCompile(`&page=(\d+)`)
)

// Transport is the fake's httpx.Transport.
func (f *GitHub) Transport(method, url string, _ map[string]string, body any) (httpx.Response, error) {
	f.Calls = append(f.Calls, Call{method, url, false, Copy(body)})
	path := strings.TrimPrefix(url, GitHubAPI+"/")
	m := issuesPath.FindStringSubmatch(path)
	if m == nil {
		return httpx.Response{}, fmt.Errorf("unexpected GitHub call %s %s", method, url)
	}
	rest := m[1]
	switch {
	case strings.HasPrefix(rest, "?state=all"):
		page, _ := strconv.Atoi(pageRe.FindStringSubmatch(rest)[1])
		var ns []int
		for n := range f.Issues {
			ns = append(ns, n)
		}
		sort.Ints(ns)
		var got []any
		for i, n := range ns {
			if i >= (page-1)*100 && i < page*100 {
				got = append(got, f.Issues[n])
			}
		}
		if got == nil {
			got = []any{}
		}
		return resp(200, Copy(got), nil)
	case rest == "" && method == "POST":
		b := Copy(body).(map[string]any)
		n := f.Next
		f.Next++
		f.Issues[n] = map[string]any{"number": n, "title": b["title"], "body": b["body"], "labels": b["labels"],
			"state": "open", "user": map[string]any{"login": "github-actions[bot]"}}
		f.Comments[n] = nil
		return resp(201, map[string]any{"number": n}, nil)
	}
	if mm := issueN.FindStringSubmatch(rest); mm != nil {
		n, _ := strconv.Atoi(mm[1])
		if method == "PATCH" {
			f.Issues[n]["body"] = body.(map[string]any)["body"]
		}
		return resp(200, Copy(f.Issues[n]), nil)
	}
	if mm := commentsPath.FindStringSubmatch(rest); mm != nil {
		n, _ := strconv.Atoi(mm[1])
		if method == "POST" {
			if f.FailAdd != nil {
				return httpx.Response{}, f.FailAdd
			}
			return resp(201, f.Add(n, body.(map[string]any)["body"].(string), "github-actions[bot]"), nil)
		}
		page, _ := strconv.Atoi(mm[2])
		var got []any
		for i, c := range f.Comments[n] {
			if i >= (page-1)*100 && i < page*100 {
				got = append(got, c)
			}
		}
		if got == nil {
			got = []any{}
		}
		return resp(200, Copy(got), nil)
	}
	return httpx.Response{}, fmt.Errorf("unexpected GitHub call %s %s", method, url)
}

// Add is a comment by login on issue n.
func (f *GitHub) Add(n int, body, login string) map[string]any {
	total := 0
	for _, cs := range f.Comments {
		total += len(cs)
	}
	cid := 5000 + total
	c := map[string]any{"id": cid, "body": body, "user": map[string]any{"login": login},
		"created_at": f.Clock, "html_url": fmt.Sprintf("https://github.com/o/r/issues/%d#issuecomment-%d", n, cid)}
	f.Comments[n] = append(f.Comments[n], c)
	return c
}

// Bodies of issue n's comments.
func (f *GitHub) Bodies(n int) []string {
	var out []string
	for _, c := range f.Comments[n] {
		out = append(out, c["body"].(string))
	}
	return out
}

// Ticket is the number of the issue whose title contains s (0 if none).
func (f *GitHub) Ticket(s string) int {
	for n, i := range f.Issues {
		if strings.Contains(fmt.Sprint(i["title"]), s) {
			return n
		}
	}
	return 0
}

// Writes is every non-GET request.
func (f *GitHub) Writes() []Call {
	var out []Call
	for _, c := range f.Calls {
		if c.Method != "GET" {
			out = append(out, c)
		}
	}
	return out
}

// Register is the self-test's register: NS3-003 (published: issue 1364, our merge
// request 2985, the competing 3011) and NS3-001 (a draft).
const Register = `
gitlab_project = "nsnam/ns-3-dev"
our_accounts = ["danieljoppi"]
[[item]]
id = "NS3-003"
title = "dsdv: null output device"
state = "published"
rcapa = "swarm-routing/ns3-tools#240"
issue = 1364
merge_requests = [2985]
competing = [3011]
[[item]]
id = "NS3-001"
title = "AODV livelock"
state = "draft"
rcapa = "swarm-routing/ns3-tools#238"
draft = ["swarm-routing/ns3-containers#55"]
`
