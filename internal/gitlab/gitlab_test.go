// Copyright (c) 2026 Daniel Joppi (https://github.com/swarm-routing)
// SPDX-License-Identifier: MIT OR GPL-2.0-only

package gitlab

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/swarm-routing/ns3-upstream-report/internal/httpx"
)

func TestContains(t *testing.T) {
	fix, tip := strings.Repeat("a", 40), strings.Repeat("b", 40)
	var urls []string
	reply := func(status int, raw string) httpx.Transport {
		return func(method, url string, _ map[string]string, _ any) (httpx.Response, error) {
			urls = append(urls, method+" "+url)
			if status != 200 {
				return httpx.Response{}, &httpx.Error{Method: method, URL: url, Status: status, Message: "x"}
			}
			return httpx.Response{Status: 200, Raw: []byte(raw)}, nil
		}
	}
	for _, c := range []struct {
		status int
		raw    string
		want   bool
		fails  bool
	}{
		{200, `{"id": "` + fix + `"}`, true, false},
		{200, `{"id": "` + strings.Repeat("c", 40) + `"}`, false, false},
		{404, "", false, false},
		{400, "", false, false},
		{500, "", false, true},
	} {
		got, err := New("nsnam/ns-3-dev", "", reply(c.status, c.raw)).Contains(tip, fix)
		if got != c.want || (err != nil) != c.fails {
			t.Errorf("Contains with HTTP %d %s: %v %v", c.status, c.raw, got, err)
		}
	}
	want := "GET " + New("nsnam/ns-3-dev", "", nil).API + "/repository/merge_base?refs%5B%5D=" + fix + "&refs%5B%5D=" + tip
	if urls[0] != want {
		t.Errorf("the merge base of the fix and the tip, by GET: %s", urls[0])
	}
	n := len(urls)
	if ok, err := New("nsnam/ns-3-dev", "", reply(500, "")).Contains(fix, fix); !ok || err != nil || len(urls) != n {
		t.Error("a commit contains itself, with no request")
	}
}

func TestNotesPaging(t *testing.T) {
	var methods []string
	tr := func(method, url string, h map[string]string, _ any) (httpx.Response, error) {
		methods = append(methods, method)
		if strings.Contains(url, "/notes?") {
			page := 1
			fmt.Sscanf(url[strings.LastIndex(url, "page=")+5:], "%d", &page)
			n := 100
			if page == 2 {
				n = 3
			}
			var l []map[string]any
			for i := range n {
				l = append(l, map[string]any{"id": page*1000 + i, "created_at": "2026-01-01T00:00:00Z", "author": map[string]any{"username": "u"}, "body": "b"})
			}
			raw, _ := json.Marshal(l)
			return httpx.Response{Status: 200, Raw: raw}, nil
		}
		// a cursor that never ends
		return httpx.Response{Status: 200, Raw: []byte(`[]`), Header: map[string]string{"x-next-page-cursor": "again"}}, nil
	}
	c := New("nsnam/ns-3-dev", "tok", tr)
	notes, err := c.Notes("!", 1, "u")
	if err != nil || len(notes) != 103 {
		t.Errorf("every page of the REST API: %d %v", len(notes), err)
	}
	c.Token = ""
	if _, err := c.Notes("#", 1, "u"); err == nil || !strings.Contains(err.Error(), "more than 100 pages") {
		t.Errorf("a cursor loop is capped: %v", err)
	}
	for _, m := range methods {
		if m != "GET" {
			t.Error("the reader only GETs")
		}
	}
	if WebURL("a/b", "#", 3) != "https://gitlab.com/a/b/-/issues/3" || c.API != "https://gitlab.com/api/v4/projects/nsnam%2Fns-3-dev" {
		t.Error("URLs")
	}
}
