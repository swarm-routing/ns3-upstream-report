// Copyright (c) 2026 Daniel Joppi (https://github.com/swarm-routing)
// SPDX-License-Identifier: MIT OR GPL-2.0-only

// Package github is the bridge's GitHub REST client. In a dry run every write is
// recorded and held back.
package github

import (
	"fmt"
	"strings"

	"github.com/swarm-routing/ns3-upstream-report/internal/httpx"
)

// API is GitHub's REST root.
const API = "https://api.github.com"

// Write is one write a run made (or held back in a dry run).
type Write struct {
	Method, Path string
	Body         any
}

// Client of one repository.
type Client struct {
	Repo, Token string
	T           httpx.Transport
	Dry         bool
	Writes      []Write
}

// New is a client of repo.
func New(repo, token string, t httpx.Transport, dry bool) *Client {
	return &Client{Repo: repo, Token: token, T: t, Dry: dry}
}

// Call is one request.
func (c *Client) Call(method, path string, body any) (any, error) {
	h := map[string]string{
		"Accept":               "application/vnd.github+json",
		"X-GitHub-Api-Version": "2022-11-28",
		"User-Agent":           "ns3-upstream-report",
	}
	if c.Token != "" {
		h["Authorization"] = "Bearer " + c.Token
	}
	if method != "GET" {
		c.Writes = append(c.Writes, Write{method, path, body})
		if c.Dry {
			return map[string]any{"number": 0, "id": 0, "html_url": "(dry run)"}, nil
		}
	}
	r, err := c.T(method, API+"/"+path, h, body)
	if err != nil {
		return nil, err
	}
	return r.JSON()
}

func obj(v any, err error) (map[string]any, error) {
	if err != nil {
		return nil, err
	}
	m, _ := v.(map[string]any)
	return m, nil
}

// Pages is every page of a list.
func (c *Client) Pages(path string) ([]map[string]any, error) {
	var out []map[string]any
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	for page := 1; page <= 1000; page++ {
		v, err := c.Call("GET", fmt.Sprintf("%s%sper_page=100&page=%d", path, sep, page), nil)
		if err != nil {
			return nil, err
		}
		got, _ := v.([]any)
		for _, x := range got {
			m, _ := x.(map[string]any)
			out = append(out, m)
		}
		if len(got) < 100 {
			return out, nil
		}
	}
	return out, nil
}

// Issues is every issue (not pull request) of the repository.
func (c *Client) Issues() ([]map[string]any, error) {
	got, err := c.Pages("repos/" + c.Repo + "/issues?state=all")
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	for _, i := range got {
		if _, pr := i["pull_request"]; !pr {
			out = append(out, i)
		}
	}
	return out, nil
}

// Issue n.
func (c *Client) Issue(n int) (map[string]any, error) {
	return obj(c.Call("GET", fmt.Sprintf("repos/%s/issues/%d", c.Repo, n), nil))
}

// Comments of issue n.
func (c *Client) Comments(n int) ([]map[string]any, error) {
	return c.Pages(fmt.Sprintf("repos/%s/issues/%d/comments", c.Repo, n))
}

// CreateIssue opens an issue here.
func (c *Client) CreateIssue(title, body string, labels []string) (map[string]any, error) {
	return obj(c.Call("POST", "repos/"+c.Repo+"/issues", map[string]any{"title": title, "body": body, "labels": labels}))
}

// UpdateBody rewrites issue n's body.
func (c *Client) UpdateBody(n int, body string) (map[string]any, error) {
	return obj(c.Call("PATCH", fmt.Sprintf("repos/%s/issues/%d", c.Repo, n), map[string]any{"body": body}))
}

// AddComment comments on issue n.
func (c *Client) AddComment(n int, body string) (map[string]any, error) {
	return obj(c.Call("POST", fmt.Sprintf("repos/%s/issues/%d/comments", c.Repo, n), map[string]any{"body": body}))
}

// CreateIssueIn opens an issue in another repository of the org (P6, the org bot's token).
func (c *Client) CreateIssueIn(repo, title, body string) (map[string]any, error) {
	return obj(c.Call("POST", "repos/"+repo+"/issues", map[string]any{"title": title, "body": body}))
}

// IssueState of `owner/repo#N`.
func (c *Client) IssueState(ref string) (string, error) {
	repo, n, _ := strings.Cut(ref, "#")
	m, err := obj(c.Call("GET", fmt.Sprintf("repos/%s/issues/%s", repo, n), nil))
	if err != nil {
		return "", err
	}
	s, _ := m["state"].(string)
	return s, nil
}

// CommentIn comments on an issue of another repository.
func (c *Client) CommentIn(repo string, n int, body string) error {
	_, err := c.Call("POST", fmt.Sprintf("repos/%s/issues/%d/comments", repo, n), map[string]any{"body": body})
	return err
}

// Close closes an issue as completed.
func (c *Client) Close(repo string, n int) error {
	_, err := c.Call("PATCH", fmt.Sprintf("repos/%s/issues/%d", repo, n), map[string]any{"state": "closed", "state_reason": "completed"})
	return err
}
