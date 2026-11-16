// Copyright (c) 2026 Daniel Joppi (https://github.com/swarm-routing)
// SPDX-License-Identifier: MIT OR GPL-2.0-only

// Package py holds the few Python semantics the bridge's output depends on, so
// the Go tool writes byte for byte what tools/upstream/upstream.py wrote: str.strip
// and str.splitlines over Python's whitespace and line breaks, the regex \w and \s
// classes for str patterns, str() of a decoded JSON value, json.dumps and
// urllib.parse.quote.
package py

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// SpaceClass is Python's \s for str patterns, for a regexp character class.
const SpaceClass = `\t\n\v\f\r\x1c-\x1f \x{85}\x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}`

// IsSpace is str.isspace for one character.
func IsSpace(r rune) bool {
	switch {
	case r >= '\t' && r <= '\r', r >= 0x1c && r <= 0x20, r == 0x85, r == 0xa0, r == 0x1680,
		r >= 0x2000 && r <= 0x200a, r == 0x2028, r == 0x2029, r == 0x202f, r == 0x205f, r == 0x3000:
		return true
	}
	return false
}

// IsWord is the regex \w of a str pattern: a letter, a number or `_`.
func IsWord(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r)
}

// IsDigit is the regex \d of a str pattern (a decimal digit of any script).
func IsDigit(r rune) bool { return unicode.IsDigit(r) }

// Strip is str.strip().
func Strip(s string) string { return strings.TrimFunc(s, IsSpace) }

// RStrip is str.rstrip().
func RStrip(s string) string { return strings.TrimRightFunc(s, IsSpace) }

// SplitLines is str.splitlines(): no trailing empty line, every Python line break.
func SplitLines(s string) []string {
	var out []string
	start := 0
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		switch rs[i] {
		case '\n', '\v', '\f', 0x1c, 0x1d, 0x1e, 0x85, 0x2028, 0x2029:
			out = append(out, string(rs[start:i]))
			start = i + 1
		case '\r':
			out = append(out, string(rs[start:i]))
			if i+1 < len(rs) && rs[i+1] == '\n' {
				i++
			}
			start = i + 1
		}
	}
	if start < len(rs) {
		out = append(out, string(rs[start:]))
	}
	return out
}

// Head is s[:n] in characters.
func Head(s string, n int) string {
	i := 0
	for pos := range s {
		if i == n {
			return s[:pos]
		}
		i++
	}
	return s
}

// Tail is s[-n:] in characters.
func Tail(s string, n int) string {
	c := utf8.RuneCountInString(s)
	if c <= n {
		return s
	}
	i := 0
	for pos := range s {
		if i == c-n {
			return s[pos:]
		}
		i++
	}
	return s
}

// Str is str() of a value json.loads or tomllib returned.
func Str(v any) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case bool:
		if x {
			return "True"
		}
		return "False"
	case string:
		return x
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return strconv.FormatInt(i, 10)
		}
		f, _ := x.Float64()
		return floatRepr(f)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return floatRepr(x)
	case time.Time:
		if x.Location().String() == "local-date" || (x.Hour() == 0 && x.Minute() == 0 && x.Second() == 0 && x.Nanosecond() == 0 && x.Location().String() != "UTC") {
			return x.Format("2006-01-02")
		}
		return x.Format("2006-01-02 15:04:05-07:00")
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			parts[i] = Repr(e)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = Repr(k) + ": " + Repr(x[k])
		}
		return "{" + strings.Join(parts, ", ") + "}"
	}
	return fmt.Sprint(v)
}

// Repr is repr() for the values Str meets inside a list or a dict.
func Repr(v any) string {
	if s, ok := v.(string); ok {
		q := "'"
		if strings.Contains(s, "'") && !strings.Contains(s, `"`) {
			q = `"`
		}
		var b strings.Builder
		b.WriteString(q)
		for _, r := range s {
			switch {
			case r == '\\':
				b.WriteString(`\\`)
			case string(r) == q:
				b.WriteString(`\` + q)
			case r == '\n':
				b.WriteString(`\n`)
			case r == '\r':
				b.WriteString(`\r`)
			case r == '\t':
				b.WriteString(`\t`)
			case r < 0x20 || r == 0x7f:
				fmt.Fprintf(&b, `\x%02x`, r)
			default:
				b.WriteRune(r)
			}
		}
		b.WriteString(q)
		return b.String()
	}
	return Str(v)
}

func floatRepr(f float64) string {
	if math.IsInf(f, 0) || math.IsNaN(f) {
		return map[bool]string{true: "inf", false: "nan"}[math.IsInf(f, 1)]
	}
	s := strconv.FormatFloat(f, 'g', -1, 64)
	if !strings.ContainsAny(s, ".eE") {
		s += ".0"
	}
	return s
}

// Truthy is Python's truth value of a decoded value.
func Truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case json.Number:
		f, _ := x.Float64()
		return f != 0
	case int:
		return x != 0
	case int64:
		return x != 0
	case float64:
		return x != 0
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	}
	return true
}

// Dumps is json.dumps(v, ensure_ascii=ascii) with the default separators, for
// strings, integers, lists and string-keyed maps (in the order given by Pairs).
func Dumps(v any, ascii bool) string {
	var b strings.Builder
	dump(&b, v, ascii)
	return b.String()
}

// Pairs is an ordered JSON object for Dumps.
type Pairs [][2]any

func dump(b *strings.Builder, v any, ascii bool) {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		b.WriteString(map[bool]string{true: "true", false: "false"}[x])
	case string:
		dumpString(b, x, ascii)
	case int:
		b.WriteString(strconv.Itoa(x))
	case int64:
		b.WriteString(strconv.FormatInt(x, 10))
	case json.Number:
		b.WriteString(x.String())
	case []any:
		b.WriteString("[")
		for i, e := range x {
			if i > 0 {
				b.WriteString(", ")
			}
			dump(b, e, ascii)
		}
		b.WriteString("]")
	case []string:
		b.WriteString("[")
		for i, e := range x {
			if i > 0 {
				b.WriteString(", ")
			}
			dumpString(b, e, ascii)
		}
		b.WriteString("]")
	case Pairs:
		b.WriteString("{")
		for i, kv := range x {
			if i > 0 {
				b.WriteString(", ")
			}
			dump(b, kv[0], ascii)
			b.WriteString(": ")
			dump(b, kv[1], ascii)
		}
		b.WriteString("}")
	default:
		panic(fmt.Sprintf("py.Dumps: %T", v))
	}
}

func dumpString(b *strings.Builder, s string, ascii bool) {
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			switch {
			case r < 0x20:
				fmt.Fprintf(b, `\u%04x`, r)
			case ascii && r > 0x7e:
				if r > 0xffff {
					r -= 0x10000
					fmt.Fprintf(b, `\u%04x\u%04x`, 0xd800+(r>>10), 0xdc00+(r&0x3ff))
				} else {
					fmt.Fprintf(b, `\u%04x`, r)
				}
			default:
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
}

// Quote is urllib.parse.quote(s, safe): letters, digits, `_.-~` and the safe
// characters stay; every other byte of the UTF-8 text is %XX.
func Quote(s, safe string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x80 && (c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			strings.IndexByte("_.-~", c) >= 0 || strings.IndexByte(safe, c) >= 0) {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}

// Days is timedelta.days: whole days, floored.
func Days(d time.Duration) int {
	return int(math.Floor(d.Hours() / 24))
}
