// Copyright (c) 2026 Daniel Joppi (https://github.com/swarm-routing)
// SPDX-License-Identifier: MIT OR GPL-2.0-only

// Package gitlab is the bridge's GitLab reader. It only ever sends GET: no method
// here writes, and the write client (package gitlabwrite) is a separate package
// that read, lifecycle and verify never import (the single-token decision,
// https://github.com/swarm-routing/ns3-upstream-report/issues/2#issuecomment-6921062335).
package gitlab

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/swarm-routing/ns3-upstream-report/internal/httpx"
	"github.com/swarm-routing/ns3-upstream-report/internal/py"
	"github.com/swarm-routing/ns3-upstream-report/internal/register"
)

// Base is gitlab.com; MaxPages caps one thread's pages (a guard against a cursor loop).
const (
	Base     = "https://gitlab.com"
	MaxPages = 100
)

// Client reads one project.
type Client struct {
	Project, Token, Base, API string
	T                         httpx.Transport
}

// New is a reader of project; token may be empty (GitLab's public pages).
func New(project, token string, t httpx.Transport) *Client {
	return &Client{project, token, Base, Base + "/api/v4/projects/" + py.Quote(project, ""), t}
}

// Note is one GitLab note, as the bridge mirrors it.
type Note struct {
	ID, Author, Name, CreatedAt, Body, URL string
	System                                 bool
}

// Path of an item kind: issues (#) or merge_requests (!).
func Path(kind string) string {
	if kind == "#" {
		return "issues"
	}
	return "merge_requests"
}

// WebURL of an item on gitlab.com.
func WebURL(project, kind string, iid int) string {
	return fmt.Sprintf("%s/%s/-/%s/%d", Base, project, Path(kind), iid)
}

func (c *Client) headers() map[string]string {
	h := map[string]string{"Accept": "application/json", "User-Agent": "ns3-upstream-report"}
	if c.Token != "" {
		h["PRIVATE-TOKEN"] = c.Token
	}
	return h
}

// Get is one GET, the body parsed.
func (c *Client) Get(url string) (any, error) {
	r, err := c.T("GET", url, c.headers(), nil)
	if err != nil {
		return nil, err
	}
	return r.JSON()
}

// Item is an issue or a merge request; a merge request carries `_approvals`
// (nil when GitLab refuses them).
func (c *Client) Item(kind string, iid int) (map[string]any, error) {
	v, err := c.Get(fmt.Sprintf("%s/%s/%d", c.API, Path(kind), iid))
	if err != nil {
		return nil, err
	}
	got, _ := v.(map[string]any)
	if got == nil {
		got = map[string]any{}
	}
	if kind == "!" {
		ap, err := c.Get(fmt.Sprintf("%s/merge_requests/%d/approvals", c.API, iid))
		if err != nil {
			if _, ok := err.(*httpx.Error); !ok {
				return nil, err
			}
			ap = nil
		}
		got["_approvals"] = ap
	}
	return got, nil
}

// Notes is every note of a thread, oldest first: with a token from the REST API,
// without one from the public, cursor-paginated discussions.json pages.
func (c *Client) Notes(kind string, iid int, webURL string) ([]Note, error) {
	var raw []any
	if c.Token != "" {
		for page := 1; ; page++ {
			if page > MaxPages {
				return nil, register.Exit("%s/%s/%d/notes: more than %d pages", c.API, Path(kind), iid, MaxPages)
			}
			v, err := c.Get(fmt.Sprintf("%s/%s/%d/notes?sort=asc&order_by=created_at&per_page=100&page=%d",
				c.API, Path(kind), iid, page))
			if err != nil {
				return nil, err
			}
			got, _ := v.([]any)
			raw = append(raw, got...)
			if len(got) < 100 {
				break
			}
		}
	} else {
		url := fmt.Sprintf("%s/%s/-/%s/%d/discussions.json", c.Base, c.Project, Path(kind), iid)
		hdr := map[string]string{"Accept": "application/json", "User-Agent": "ns3-upstream-report"}
		cursor := ""
		done := false
		for range MaxPages {
			q := ""
			if cursor != "" {
				q = "?cursor=" + py.Quote(cursor, "")
			}
			r, err := c.T("GET", url+q, hdr, nil)
			if err != nil {
				return nil, err
			}
			v, err := r.JSON()
			if err != nil {
				return nil, err
			}
			ds, _ := v.([]any)
			for _, d := range ds {
				if dm, ok := d.(map[string]any); ok {
					ns, _ := dm["notes"].([]any)
					raw = append(raw, ns...)
				}
			}
			cursor = r.Header["x-next-page-cursor"]
			if cursor == "" {
				done = true
				break
			}
		}
		if !done {
			return nil, register.Exit("%s: more than %d pages", url, MaxPages)
		}
	}
	out := make([]Note, 0, len(raw))
	for _, x := range raw {
		n, _ := x.(map[string]any)
		author, _ := n["author"].(map[string]any)
		body, has := n["body"]
		if !has {
			body = n["note"]
		}
		bs, _ := body.(string)
		ca, _ := n["created_at"].(string)
		out = append(out, Note{
			ID:        py.Str(n["id"]),
			Author:    strOr(author, "username", "?"),
			Name:      strOr(author, "name", ""),
			CreatedAt: ca,
			Body:      bs,
			System:    py.Truthy(n["system"]),
			URL:       webURL + "#note_" + py.Str(n["id"]),
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt < out[j].CreatedAt
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func strOr(m map[string]any, k, d string) string {
	if v, ok := m[k]; ok {
		return py.Str(v)
	}
	return d
}

// IssueByTitle is (iid, url) of an issue with exactly this title, or ok=false.
func (c *Client) IssueByTitle(title string) (int, string, bool, error) {
	v, err := c.Get(fmt.Sprintf("%s/issues?search=%s&in=title&per_page=100", c.API, py.Quote(title, "/")))
	if err != nil {
		return 0, "", false, err
	}
	l, _ := v.([]any)
	for _, x := range l {
		i, _ := x.(map[string]any)
		if t, _ := i["title"].(string); t == title {
			iid, _ := i["iid"].(json.Number).Int64()
			u, _ := i["web_url"].(string)
			return int(iid), u, true, nil
		}
	}
	return 0, "", false, nil
}

// NoteWithText is the id of a note in a thread whose text is text, or "".
func (c *Client) NoteWithText(kind string, iid int, text string) (string, error) {
	notes, err := c.Notes(kind, iid, WebURL(c.Project, kind, iid))
	if err != nil {
		return "", err
	}
	for _, n := range notes {
		if py.Strip(n.Body) == py.Strip(text) {
			return n.ID, nil
		}
	}
	return "", nil
}

// CommitTags is the names of the tags that contain a commit.
func (c *Client) CommitTags(sha string) ([]string, error) {
	v, err := c.Get(fmt.Sprintf("%s/repository/commits/%s/refs?type=tag&per_page=100", c.API, sha))
	if err != nil {
		return nil, err
	}
	l, _ := v.([]any)
	var out []string
	for _, x := range l {
		if m, ok := x.(map[string]any); ok {
			out = append(out, py.Str(m["name"]))
		}
	}
	return out, nil
}

// TokenSelf is GET /personal_access_tokens/self: the token's created_at, expires_at, scopes.
func (c *Client) TokenSelf() (map[string]any, error) {
	v, err := c.Get(c.Base + "/api/v4/personal_access_tokens/self")
	if err != nil {
		return nil, err
	}
	m, _ := v.(map[string]any)
	return m, nil
}

// MRPatch is a merge request's series as `git format-patch` text (GitLab's public .patch page).
func (c *Client) MRPatch(iid int) (string, error) {
	r, err := c.T("GET", fmt.Sprintf("%s/%s/-/merge_requests/%d.patch", c.Base, c.Project, iid),
		map[string]string{"User-Agent": "ns3-upstream-report"}, nil)
	if err != nil {
		return "", err
	}
	return string(r.Raw), nil
}
