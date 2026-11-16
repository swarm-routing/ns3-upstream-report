// Copyright (c) 2026 Daniel Joppi (https://github.com/swarm-routing)
// SPDX-License-Identifier: MIT OR GPL-2.0-only

package rotate

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/nacl/box"

	"github.com/swarm-routing/ns3-upstream-report/internal/fake"
	"github.com/swarm-routing/ns3-upstream-report/internal/github"
	"github.com/swarm-routing/ns3-upstream-report/internal/gitlab"
	"github.com/swarm-routing/ns3-upstream-report/internal/httpx"
	"github.com/swarm-routing/ns3-upstream-report/internal/send"
)

var now = time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)

type app struct {
	status   int // the public key's answer when not 200
	putFails int // the PUT that fails (1-based), 0: none
	calls    []fake.Call
}

func (a *app) transport(method, url string, _ map[string]string, body any) (httpx.Response, error) {
	a.calls = append(a.calls, fake.Call{Method: method, URL: url, Body: body})
	if method == "GET" {
		if a.status != 0 {
			return httpx.Response{}, &httpx.Error{Method: method, URL: url, Status: a.status, Message: "Resource not accessible by integration"}
		}
		return httpx.Response{Status: 200, Raw: []byte(`{"key": "a2V5", "key_id": "k1"}`)}, nil
	}
	if a.putFails != 0 && a.puts() == a.putFails {
		return httpx.Response{}, &httpx.Error{Method: method, URL: url, Status: 500, Message: "boom with glpat-NEWSECRET"}
	}
	return httpx.Response{Status: 204}, nil
}

func (a *app) puts() int {
	n := 0
	for _, c := range a.calls {
		if c.Method == "PUT" {
			n++
		}
	}
	return n
}

func enc(_, v string) (string, error) { return fmt.Sprintf("ENC(%d)", len(v)), nil }

type world struct {
	fl *fake.GitLab
	fg *fake.GitHub
}

func (w *world) rotator(created, expires, token string, a *app, dry bool) *Rotator {
	w.fl.Data[fake.GitLabBase+"/api/v4/personal_access_tokens/self"] = map[string]any{"created_at": created, "expires_at": expires}
	r := &Rotator{GW: send.Writer(gitlab.New("nsnam/ns-3-dev", token, w.fl.Transport), dry),
		GH: github.New("o/r", "t", w.fg.Transport, dry), Encrypt: enc, Now: now, Dry: dry}
	if a != nil {
		r.App = github.New("o/r", "a", a.transport, dry)
	}
	return r
}

func (w *world) rotated() bool {
	for _, c := range w.fl.Calls {
		if strings.HasSuffix(c.URL, "/rotate") {
			return true
		}
	}
	return false
}

func run(t *testing.T, r *Rotator) string {
	t.Helper()
	out, err := r.Run()
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(out, "\n")
}

func TestRotate(t *testing.T) {
	w := &world{fake.NewGitLab(), fake.NewGitHub()}
	if out := run(t, w.rotator("2026-08-01T00:00:00Z", "2027-11-16", "", &app{}, false)); !strings.Contains(out, "no GitLab token") {
		t.Errorf("no GitLab token: a clear skip: %s", out)
	}
	out := run(t, w.rotator("2026-08-01T00:00:00Z", "2027-11-16", "glpat-OLD", nil, false))
	if !strings.Contains(out, "no org App token; #1 says so") || len(w.fg.Issues) != 1 || w.rotated() {
		t.Errorf("P7: no App token: an issue, nothing rotated: %s", out)
	}
	run(t, w.rotator("2026-08-01T00:00:00Z", "2027-11-16", "glpat-OLD", nil, false))
	if len(w.fg.Issues) != 1 {
		t.Error("the issue is opened once")
	}
	a403 := &app{status: 403}
	out = run(t, w.rotator("2026-08-01T00:00:00Z", "2027-11-16", "glpat-OLD", a403, false))
	body := w.fg.Issues[2]["body"].(string)
	if !strings.Contains(out, "cannot read gitlab-read's secrets public key (HTTP 403)") || !strings.Contains(body, "**Secrets: Read and write**") || w.rotated() || a403.puts() != 0 {
		t.Errorf("P7: without the permission, an issue naming it, before anything is rotated: %s", out)
	}
	ok := &app{}
	out = run(t, w.rotator("2026-11-01T00:00:00Z", "2027-11-16", "glpat-OLD", ok, false))
	if out != "P7: token is 15 days old (expires 2027-11-16); rotates at 60" || ok.puts() != 0 || w.rotated() {
		t.Errorf("P7: a young token is left alone: %s", out)
	}
	out = run(t, w.rotator("2026-11-01T00:00:00Z", "2026-11-25", "glpat-OLD", ok, true))
	if !strings.Contains(out, "dry run: would") || ok.puts() != 0 || w.rotated() {
		t.Errorf("P7: a dry run writes nothing: %s", out)
	}
	out = run(t, w.rotator("2026-09-01T00:00:00Z", "2027-11-16", "glpat-OLD", ok, false))
	var puts []fake.Call
	for _, c := range ok.calls {
		if c.Method == "PUT" {
			puts = append(puts, c)
		}
	}
	var rot fake.Call
	for _, c := range w.fl.Calls {
		if strings.HasSuffix(c.URL, "/rotate") {
			rot = c
		}
	}
	if out != "P7: rotated (the token was 76 days old), stored in gitlab-read and gitlab-write; expires 2027-02-14" ||
		len(puts) != 4 || rot.Body.(map[string]any)["expires_at"] != "2027-02-14" || !rot.Token {
		t.Fatalf("P7: an old token is rotated (90-day expiry) and stored in both environments: %s %d", out, len(puts))
	}
	for i, env := range []string{"gitlab-read", "gitlab-write", "gitlab-read", "gitlab-write"} {
		if !strings.HasSuffix(puts[i].URL, "/environments/"+env+"/secrets/GITLAB_TOKEN") {
			t.Errorf("put %d: %s", i, puts[i].URL)
		}
	}
	want := []string{"ENC(9)", "ENC(9)", "ENC(15)", "ENC(15)"} // the current value first (the App's write proved), then the new one
	for i, p := range puts {
		b := p.Body.(map[string]any)
		if b["encrypted_value"] != want[i] || b["key_id"] != "k1" {
			t.Errorf("put %d: %v", i, b)
		}
	}
	if out := run(t, w.rotator("2026-11-10T00:00:00Z", "2026-11-25", "glpat-OLD", &app{}, false)); !strings.Contains(out, "rotated") {
		t.Errorf("P7: a token expiring within 14 days is rotated: %s", out)
	}
	pre := &app{putFails: 1}
	w.fl.Calls = nil
	out = run(t, w.rotator("2026-09-01T00:00:00Z", "2027-11-16", "glpat-OLD", pre, false))
	if !strings.Contains(out, "cannot write the environment secrets") || w.rotated() {
		t.Errorf("P7: when the App cannot write, nothing is rotated: %s", out)
	}
	post := &app{putFails: 3}
	out = run(t, w.rotator("2026-09-01T00:00:00Z", "2027-11-16", "glpat-OLD", post, false))
	data, _ := json.Marshal(w.fg.Issues)
	if !strings.Contains(out, "rotated, but storing failed") || strings.Contains(string(data), "NEWSECRET") || strings.Contains(string(data), "glpat-OLD") ||
		!strings.Contains(string(data), "could not be stored") {
		t.Errorf("P7: a failed store opens an issue, and no token text leaks: %s", out)
	}
	for _, c := range w.fg.Calls {
		cd, _ := json.Marshal(c)
		if strings.Contains(string(cd), "glpat-") && !strings.Contains(string(cd), "glpat-[redacted]") {
			t.Error("no token reaches GitHub")
		}
	}
}

func TestRotationFailure(t *testing.T) {
	w := &world{fake.NewGitLab(), fake.NewGitHub()}
	r := w.rotator("2026-09-01T00:00:00Z", "2027-11-16", "glpat-OLD", &app{}, false)
	inner := r.GW.R.T
	r.GW.R.T = func(method, url string, h map[string]string, body any) (httpx.Response, error) {
		if strings.HasSuffix(url, "/rotate") {
			return httpx.Response{}, &httpx.Error{Method: method, URL: url, Status: 400, Message: "glpat-OLD expired"}
		}
		return inner(method, url, h, body)
	}
	out := run(t, r)
	data, _ := json.Marshal(w.fg.Issues)
	if !strings.Contains(out, "rotation failed") || strings.Contains(string(data), "glpat-OLD") {
		t.Errorf("P7: a failed rotation opens an issue: %s", out)
	}
}

func TestSealedBox(t *testing.T) {
	pub, priv, err := box.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	got, err := SealedBox(base64.StdEncoding.EncodeToString(pub[:]), "glpat-secret")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := base64.StdEncoding.DecodeString(got)
	plain, ok := box.OpenAnonymous(nil, raw, pub, priv)
	if !ok || string(plain) != "glpat-secret" {
		t.Error("the sealed box opens with the environment's key")
	}
	if _, err := SealedBox("a2V5", "x"); err == nil {
		t.Error("a bad key is refused")
	}
}
