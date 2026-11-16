// Copyright (c) 2026 Daniel Joppi (https://github.com/swarm-routing)
// SPDX-License-Identifier: MIT OR GPL-2.0-only

// Package gitlabwrite is the bridge's only GitLab writer, imported only by send,
// maintain and rotate. Every write passes the scope guard first: nsnam/ns-3-dev
// only; a new issue, a note on one of the item's own threads, GitLab's rebase of
// one of the item's own merge requests, or the token's self-rotation; never a
// close, label, approval, merge or push.
package gitlabwrite

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/swarm-routing/ns3-upstream-report/internal/gitlab"
	"github.com/swarm-routing/ns3-upstream-report/internal/register"
)

// AllowedProject is the only GitLab project the bridge writes to.
const AllowedProject = "nsnam/ns-3-dev"

// Thread is an issue (#) or a merge request (!).
type Thread struct {
	Kind string
	IID  int
}

// Client writes; R reads (already-sent checks need it).
type Client struct {
	R       *gitlab.Client
	Allowed map[Thread]bool // the threads notes and rebases may go to
	Dry     bool
}

// New is a writer over a reader.
func New(r *gitlab.Client) *Client { return &Client{R: r, Allowed: map[Thread]bool{}} }

var notesPath = regexp.MustCompile(`^/(issues|merge_requests)/([0-9]+)/(notes|rebase)$`)

func (c *Client) write(method, url string, body any) (map[string]any, error) {
	rel := strings.TrimPrefix(url, c.R.API)
	m := notesPath.FindStringSubmatch(rel)
	var th *Thread
	if m != nil {
		iid, _ := strconv.Atoi(m[2])
		k := "#"
		if m[1] == "merge_requests" {
			k = "!"
		}
		th = &Thread{k, iid}
	}
	ok := c.R.Project == AllowedProject && strings.HasPrefix(url, c.R.API) && ((method == "POST" && rel == "/issues") ||
		(th != nil && c.Allowed[*th] && (method == "POST" && m[3] == "notes" || method == "PUT" && m[3] == "rebase" && th.Kind == "!")))
	if !ok {
		return nil, register.Exit("scope: %s %s is not a write the bridge may make", c.R.Project, rel)
	}
	if c.Dry {
		return map[string]any{"iid": 0, "id": "0", "web_url": "(dry run)"}, nil
	}
	if c.R.Token == "" {
		return nil, register.Exit("GITLAB_TOKEN is not set: writing to GitLab needs the gitlab-write token")
	}
	h := map[string]string{"Accept": "application/json", "User-Agent": "ns3-upstream-report", "PRIVATE-TOKEN": c.R.Token}
	r, err := c.R.T(method, url, h, body)
	if err != nil {
		return nil, err
	}
	v, err := r.JSON()
	if err != nil {
		return nil, err
	}
	got, _ := v.(map[string]any)
	return got, nil
}

// CreateIssue opens an issue: (iid, url).
func (c *Client) CreateIssue(title, description string) (int, string, error) {
	got, err := c.write("POST", c.R.API+"/issues", map[string]any{"title": title, "description": description})
	if err != nil {
		return 0, "", err
	}
	iid, _ := strconv.Atoi(fmt.Sprint(got["iid"]))
	u, _ := got["web_url"].(string)
	return iid, u, nil
}

// CreateNote posts a note on an allowed thread: its id.
func (c *Client) CreateNote(kind string, iid int, body string) (string, error) {
	got, err := c.write("POST", fmt.Sprintf("%s/%s/%d/notes", c.R.API, gitlab.Path(kind), iid), map[string]any{"body": body})
	if err != nil {
		return "", err
	}
	return fmt.Sprint(got["id"]), nil
}

// Rebase asks GitLab to rebase one of the item's merge requests (P5). It needs push
// access to the source branch, which GitLab checks; the bridge pushes nothing itself.
func (c *Client) Rebase(iid int) error {
	_, err := c.write("PUT", fmt.Sprintf("%s/merge_requests/%d/rebase", c.R.API, iid), map[string]any{})
	return err
}

// RotateSelf rotates the token (GitLab revokes the old one) and returns the new one (P7).
func (c *Client) RotateSelf(expiresAt string) (string, error) {
	if c.Dry {
		return "", register.Exit("dry run: no rotation")
	}
	if c.R.Token == "" {
		return "", register.Exit("GITLAB_TOKEN is not set")
	}
	h := map[string]string{"Accept": "application/json", "User-Agent": "ns3-upstream-report", "PRIVATE-TOKEN": c.R.Token}
	r, err := c.R.T("POST", c.R.Base+"/api/v4/personal_access_tokens/self/rotate", h, map[string]any{"expires_at": expiresAt})
	if err != nil {
		return "", err
	}
	v, err := r.JSON()
	if err != nil {
		return "", err
	}
	m, _ := v.(map[string]any)
	tok, _ := m["token"].(string)
	if tok == "" {
		return "", register.Exit("rotate: GitLab returned no token")
	}
	return tok, nil
}
