// Copyright (c) 2026 Daniel Joppi (https://github.com/swarm-routing)
// SPDX-License-Identifier: MIT OR GPL-2.0-only

package bridge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/swarm-routing/ns3-upstream-report/internal/register"
)

const proj = "nsnam/ns-3-dev"

func TestOutboundLint(t *testing.T) {
	for _, c := range []struct{ text, want string }{
		{"Crash in #1364.", "`#1364` links ns-3's issue 1364"},
		{"Fixed by !2985.", "write `nsnam/ns-3-dev!2985`"},
		{"RCAPA: swarm-routing/ns3-tools#240", "https://github.com/swarm-routing/ns3-tools/issues/240"},
		{"see danieljoppi/AntHocNet#5", "`danieljoppi/AntHocNet#5` links a GitLab project"},
		{"ours: swarm-routing/ns3-tools!248", "https://github.com/swarm-routing/ns3-tools/pull/248"},
	} {
		got := LintOutbound(c.text, proj)
		if len(got) != 1 || !strings.Contains(got[0], c.want) {
			t.Errorf("outbound lint flags: %s: %q", c.text, got)
		}
	}
	for _, text := range []string{
		"Same as nsnam/ns-3-dev#1364 and nsnam/ns-3-dev!2985.",
		"https://gitlab.com/nsnam/ns-3-dev/-/issues/1364#note_3755232337",
		"RCAPA: https://github.com/swarm-routing/ns3-tools/issues/240 (also /pull/248#issuecomment-1)",
		"In code: `ptr#1` and\n```\nissue #12 !3\n```\nC#1, x!2, # Heading, issue#3",
		"~~~cpp\nm[#0] = 1;\n~~~\n",
		"  ```\n#1\n   ```  tail\n",
	} {
		if got := LintOutbound(text, proj); len(got) != 0 {
			t.Errorf("outbound lint passes: %q: %q", text, got)
		}
	}
	// Python's own verdicts on the edges (recorded with upstream.py)
	got := LintOutbound("see a/b/#1 and x-y/z#2 and é/x#3 and (!4) and #5a and #6. and z!7", proj)
	want := []string{
		"`x-y/z#2` links a GitLab project on GitLab: write the item's full URL",
		"`!4` links ns-3's merge request 4 on GitLab: write `nsnam/ns-3-dev!4` for an ns-3 item, or the full https://github.com/swarm-routing/<repo>/issues/4 (or /pull/4) for ours",
		"`#6` links ns-3's issue 6 on GitLab: write `nsnam/ns-3-dev#6` for an ns-3 item, or the full https://github.com/swarm-routing/<repo>/issues/6 (or /pull/6) for ours",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("outbound lint edges:\n%q", got)
	}
	if got := LintOutbound("```\n#1\n``", proj); len(got) != 1 {
		t.Errorf("an unclosed fence hides nothing: %q", got)
	}
	if got := LintOutbound("```\nx\n~~~\n#2\n", proj); len(got) != 1 {
		t.Errorf("a fence closes only with its own kind: %q", got)
	}
}

func TestParseTarget(t *testing.T) {
	for _, c := range []struct {
		in   string
		kind string
		iid  int
		ok   bool
	}{
		{"https://gitlab.com/nsnam/ns-3-dev/-/work_items/1364", "#", 1364, true},
		{"https://gitlab.com/nsnam/ns-3-dev/-/merge_requests/2985/", "!", 2985, true},
		{"nsnam/ns-3-dev#1364", "#", 1364, true},
		{"https://gitlab.com/other/proj/-/issues/1", "", 0, false},
		{"#1364", "", 0, false},
	} {
		k, i, ok := ParseTarget(c.in, proj)
		if k != c.kind || i != c.iid || ok != c.ok {
			t.Errorf("a GitLab reference is its URL or the project form: %s -> %s %d %v", c.in, k, i, ok)
		}
	}
}

func TestNeutralAndQuote(t *testing.T) {
	if got := Neutral("x#1 @ab #١ a/#2 <!-- @_ (#3)"); got != "x#1 @\u2060ab #\u2060١ a/#2 &lt;!-- @_ (#\u20603)" {
		t.Errorf("neutral: %q", got)
	}
	if Quote("a\n\nb") != "> a\n>\n> b" || Quote("") != ">" {
		t.Error("quote")
	}
}

func TestDigestIsPythons(t *testing.T) {
	// upstream.py's digest of the same parts (json.dumps, ensure_ascii=False)
	got := Digest("reply", "NS3-003", "3692233174", []any{"!", 2985}, "Ünïcode \"q\" \\ <b>&  \x01 \n")
	if got != "d64322198891c331ad5786ee85fb386181632cddf173d5eabd892793cfe007e3" {
		t.Errorf("digest: %s", got)
	}
}

func TestMarkers(t *testing.T) {
	if v, ok := FirstLineMarker("<!-- upstream-sent: a -> b -->\nx", "upstream-sent"); !ok || v != "a -> b" {
		t.Error("a first-line marker")
	}
	if _, ok := FirstLineMarker("x\n<!-- upstream-sent: a -->", "upstream-sent"); ok {
		t.Error("only the first line")
	}
	if _, ok := FirstLineMarker("<!-- upstream-flags:  -->", "upstream-flags"); ok {
		t.Error("an empty value is no marker")
	}
	body := ReplaceStatus("head\n"+StatusBegin+"\nold\n"+StatusEnd+"\ntail", "new")
	if body != "head\n"+StatusBegin+"\nnew\n"+StatusEnd+"\ntail" || ReplaceStatus("x\n\n", "b") != "x\n\n"+StatusBegin+"\nb\n"+StatusEnd+"\n" {
		t.Errorf("status block: %q", body)
	}
}

func write(t *testing.T, root, rel, text string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLintFiles(t *testing.T) {
	m, _ := register.Parse(testRegister)
	cfg := register.FromRaw(m)
	root := t.TempDir()
	report := "# AODV livelocks in an ICMP storm\n\nSee nsnam/ns-3-dev#1364 and https://github.com/swarm-routing/ns3-tools/issues/238.\n"
	write(t, root, "reports/NS3-001.md", report)
	write(t, root, "reports/README.md", "about #1")
	write(t, root, "reports/NS3-999.md", report)
	write(t, root, "reports/NS3-003.md", "no title\n")
	write(t, root, "replies/NS3-003/3692233174.md", "Done: see #12.\n")
	write(t, root, "replies/NS3-404/1.md", "x")
	write(t, root, "replies/NS3-003/7.md", " \n")
	write(t, root, "replies/NS3-003/5.md", "Thanks.\n\n---\n"+Disclosure)
	lint, err := LintFiles(cfg, root)
	if err != nil {
		t.Fatal(err)
	}
	all := strings.Join(lint, "\n")
	for _, want := range []string{"reports/NS3-999.md: not reports/", "reports/NS3-003.md: a report starts with",
		"replies/NS3-003/3692233174.md: `#12`", "replies/NS3-404/1.md: not replies/",
		"replies/NS3-003/5.md: the disclosure footer is added", "replies/NS3-003/7.md: empty"} {
		if !strings.Contains(all, want) {
			t.Errorf("CI lint: %s missing in\n%s", want, all)
		}
	}
	if strings.Contains(all, "reports/NS3-001.md") || strings.Contains(all, "README") {
		t.Errorf("a good report and the README pass:\n%s", all)
	}
	if _, _, err := ParseReport("\ufeff# T\n\nB\n"); err != nil {
		t.Error("a byte order mark is not part of the title")
	}
}

const testRegister = `
gitlab_project = "nsnam/ns-3-dev"
our_accounts = ["danieljoppi"]
[[item]]
id = "NS3-003"
title = "t"
state = "published"
issue = 1364
merge_requests = [2985]
[[item]]
id = "NS3-001"
title = "t"
state = "draft"
`
