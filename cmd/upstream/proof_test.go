// Copyright (c) 2026 Daniel Joppi (https://github.com/swarm-routing)
// SPDX-License-Identifier: MIT OR GPL-2.0-only

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/swarm-routing/ns3-upstream-report/internal/httpx"
)

// TestProof is the Go side of the port's byte-identical proof: with
// UPSTREAM_PROOF_WORLD and UPSTREAM_PROOF_OUT set, it runs the commands of
// tools/upstream/proof/commands.json against the recorded world (a GET served from
// it, a write recorded and answered with a canned body, never sent) and writes the
// same file tools/upstream/proof/py_replay.py writes for the Python tool.
func TestProof(t *testing.T) {
	worldPath, outPath := os.Getenv("UPSTREAM_PROOF_WORLD"), os.Getenv("UPSTREAM_PROOF_OUT")
	if worldPath == "" || outPath == "" {
		t.Skip("UPSTREAM_PROOF_WORLD and UPSTREAM_PROOF_OUT not set")
	}
	root, _ := filepath.Abs("../..")
	var world map[string][3]json.RawMessage
	readJSON(t, worldPath, &world)
	var commands [][]string
	readJSON(t, filepath.Join(root, "tools/upstream/proof/commands.json"), &commands)
	var writes []map[string]any
	tr := func(method, url string, _ map[string]string, body any) (httpx.Response, error) {
		if method != "GET" {
			writes = append(writes, map[string]any{"method": method, "url": url, "body": body})
			raw, _ := json.Marshal(map[string]any{"number": 0, "id": 0, "iid": 0, "html_url": "(replay)", "web_url": "(replay)"})
			return httpx.Response{Status: 201, Raw: raw, Header: map[string]string{}}, nil
		}
		w, ok := world[url]
		if !ok {
			return httpx.Response{}, &httpx.Error{Method: method, URL: url, Status: 404, Message: "not in the world"}
		}
		var status int
		_ = json.Unmarshal(w[0], &status)
		if status >= 300 {
			var msg string
			_ = json.Unmarshal(w[1], &msg)
			return httpx.Response{}, &httpx.Error{Method: method, URL: url, Status: status, Message: msg}
		}
		h := map[string]string{}
		_ = json.Unmarshal(w[2], &h)
		return httpx.Response{Status: status, Raw: w[1], Header: h}, nil
	}
	var results []map[string]any
	for _, argv := range commands {
		writes = nil
		var out, errb bytes.Buffer
		code := run(argv, env{
			Getenv: func(string) string { return "" }, GitHub: tr, GitLab: tr,
			Now:    func() time.Time { return time.Now().UTC() },
			Stdout: &out, Stderr: &errb, Workdir: root,
		})
		if errb.Len() > 0 {
			out.WriteString("stderr: " + errb.String())
		}
		results = append(results, map[string]any{"argv": argv, "code": code, "stdout": out.String(), "writes": writes})
	}
	data, _ := json.MarshalIndent(results, "", " ")
	if err := os.WriteFile(outPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatal(err)
	}
}
