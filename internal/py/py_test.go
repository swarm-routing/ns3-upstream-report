// Copyright (c) 2026 Daniel Joppi (https://github.com/swarm-routing)
// SPDX-License-Identifier: MIT OR GPL-2.0-only

package py

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

// The expected values are Python 3's own (recorded with python3 on 2026-11-16).
func TestPython(t *testing.T) {
	if got := Dumps([]any{"a\u2028<&\x7f\u00e9\U0001F600"}, true); got != `["a\u2028<&\u007f\u00e9\ud83d\ude00"]` {
		t.Errorf("json.dumps ensure_ascii: %s", got)
	}
	if got := Dumps([]any{"q\"\\\n\x01", 2985, []any{"!", 2985}}, false); got != `["q\"\\\n\u0001", 2985, ["!", 2985]]` {
		t.Errorf("json.dumps: %s", got)
	}
	if got := Dumps(Pairs{{"item", "x"}, {"n", json.Number("3")}}, true); got != `{"item": "x", "n": 3}` {
		t.Errorf("json.dumps object: %s", got)
	}
	if got := Quote("a b/c?d=é~", ""); got != "a%20b%2Fc%3Fd%3D%C3%A9~" {
		t.Errorf("quote safe='': %s", got)
	}
	if got := Quote("a b/c", "/"); got != "a%20b/c" {
		t.Errorf("quote: %s", got)
	}
	if got := SplitLines("a\vb\x1cc\r\nd\u0085e f\n"); !reflect.DeepEqual(got, []string{"a", "b", "c", "d", "e", "f"}) {
		t.Errorf("splitlines: %q", got)
	}
	if got := SplitLines(""); got != nil {
		t.Errorf("splitlines of nothing: %q", got)
	}
	if Strip("\u00a0 x \u3000\n") != "x" || RStrip(" x \n") != " x" {
		t.Error("strip")
	}
	if Head("héllo", 2) != "hé" || Tail("héllo", 4) != "éllo" || Head("ab", 5) != "ab" || Tail("ab", 5) != "ab" {
		t.Error("slices by character")
	}
	for v, want := range map[any]string{nil: "None", true: "True", false: "False", json.Number("12"): "12",
		json.Number("1.5"): "1.5", int64(3): "3", 2.0: "2.0", "s": "s"} {
		if got := Str(v); got != want {
			t.Errorf("str(%v) = %q, want %q", v, got, want)
		}
	}
	if Str([]any{"a", int64(1)}) != "['a', 1]" || Str(map[string]any{"k": nil}) != "{'k': None}" {
		t.Error("str of a list and a dict")
	}
	if Str(time.Date(2026, 11, 15, 0, 0, 0, 0, time.FixedZone("local-date", 0))) != "2026-11-15" {
		t.Error("str of a TOML date")
	}
	if !Truthy(json.Number("1")) || Truthy(json.Number("0")) || Truthy([]any{}) || !Truthy(map[string]any{"a": 1}) || Truthy("") || Truthy(nil) {
		t.Error("truth values")
	}
	if !IsWord('é') || !IsWord('٣') || IsWord('-') || !IsSpace(' ') || IsSpace('x') || !IsDigit('١') {
		t.Error("character classes")
	}
	if Days(36*time.Hour) != 1 || Days(-time.Hour) != -1 {
		t.Error("timedelta.days")
	}
}
