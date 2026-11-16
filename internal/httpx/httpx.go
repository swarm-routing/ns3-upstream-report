// Copyright (c) 2026 Daniel Joppi (https://github.com/swarm-routing)
// SPDX-License-Identifier: MIT OR GPL-2.0-only

// Package httpx is the bridge's one HTTP request: a Transport the clients call
// and the tests replace with fakes. Request headers (where the tokens are) never
// appear in an error.
package httpx

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/swarm-routing/ns3-upstream-report/internal/py"
)

// Response of one request: the status, the raw body and the headers, lowercased.
type Response struct {
	Status int
	Raw    []byte
	Header map[string]string
}

// JSON is the body parsed (numbers kept as json.Number), or nil for an empty body.
func (r Response) JSON() (any, error) {
	if py.Strip(string(r.Raw)) == "" {
		return nil, nil
	}
	d := json.NewDecoder(bytes.NewReader(r.Raw))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return nil, fmt.Errorf("not JSON: %w", err)
	}
	return v, nil
}

// Transport makes one request; body (if not nil) is sent as JSON.
type Transport func(method, url string, headers map[string]string, body any) (Response, error)

// Error is an HTTP status other than 2xx.
type Error struct {
	Method, URL string
	Status      int
	Message     string
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s %s: HTTP %d: %s", e.Method, e.URL, e.Status, e.Message)
}

// Status of an *Error, 0 for any other error.
func Status(err error) int {
	if e, ok := err.(*Error); ok {
		return e.Status
	}
	return 0
}

var client = &http.Client{Timeout: 60 * time.Second}

// MaxBody caps a response read into memory (a bounded structure, org rule 7).
const MaxBody = 64 << 20

// Real is the network Transport.
func Real(method, url string, headers map[string]string, body any) (Response, error) {
	var rd io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return Response{}, err
		}
		rd = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, url, rd)
	if err != nil {
		return Response{}, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return Response{}, fmt.Errorf("%s %s: %v", method, url, scrub(err.Error(), headers))
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, MaxBody))
	if err != nil {
		return Response{}, fmt.Errorf("%s %s: %v", method, url, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return Response{}, &Error{method, url, resp.StatusCode, py.Head(strings.ToValidUTF8(string(raw), "�"), 300)}
	}
	got := map[string]string{}
	for k, v := range resp.Header {
		got[strings.ToLower(k)] = strings.Join(v, ", ")
	}
	return Response{resp.StatusCode, raw, got}, nil
}

// scrub removes any header value from a message (a transport error never names a token).
func scrub(msg string, headers map[string]string) string {
	for _, v := range headers {
		if len(v) > 8 {
			msg = strings.ReplaceAll(msg, v, "[redacted]")
		}
	}
	return msg
}
