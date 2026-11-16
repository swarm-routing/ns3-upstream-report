// Copyright (c) 2026 Daniel Joppi (https://github.com/swarm-routing)
// SPDX-License-Identifier: MIT OR GPL-2.0-only

package httpx

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			b, _ := io.ReadAll(r.Body)
			w.Header().Set("X-Next-Page-Cursor", "c2")
			_, _ = w.Write([]byte(`{"method": "` + r.Method + `", "body": ` + string(b) + `, "token": "` + r.Header.Get("PRIVATE-TOKEN") + `", "n": 3691365625}`))
		case "/empty":
			w.WriteHeader(204)
		default:
			w.WriteHeader(404)
			_, _ = w.Write([]byte(strings.Repeat("x", 400)))
		}
	}))
	defer srv.Close()
	r, err := Real("POST", srv.URL+"/ok", map[string]string{"PRIVATE-TOKEN": "tok"}, map[string]any{"a": 1})
	if err != nil {
		t.Fatal(err)
	}
	v, _ := r.JSON()
	m := v.(map[string]any)
	if m["method"] != "POST" || m["token"] != "tok" || r.Header["x-next-page-cursor"] != "c2" || m["n"].(interface{ String() string }).String() != "3691365625" {
		t.Errorf("a request: %v %v", m, r.Header)
	}
	if r, err := Real("GET", srv.URL+"/empty", nil, nil); err != nil || r.Status != 204 {
		t.Error("an empty answer")
	} else if v, err := r.JSON(); v != nil || err != nil {
		t.Error("an empty body is nil")
	}
	_, err = Real("GET", srv.URL+"/missing", map[string]string{"PRIVATE-TOKEN": "secret-token-value"}, nil)
	if Status(err) != 404 || strings.Contains(err.Error(), "secret-token-value") || len(err.(*Error).Message) != 300 {
		t.Errorf("an HTTP error: %v", err)
	}
	if _, err := Real("GET", "http://127.0.0.1:1/x", map[string]string{"Authorization": "Bearer secret-token-value"}, nil); err == nil || strings.Contains(err.Error(), "secret-token-value") {
		t.Errorf("a transport error never names a token: %v", err)
	}
	if Status(nil) != 0 {
		t.Error("status of no error")
	}
	if _, err := (Response{Raw: []byte("{")}).JSON(); err == nil {
		t.Error("bad JSON is an error")
	}
}
