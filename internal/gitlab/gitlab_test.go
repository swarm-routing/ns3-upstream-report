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
