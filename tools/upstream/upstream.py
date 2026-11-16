#!/usr/bin/env python3
# Copyright (c) 2026 Daniel Joppi (https://github.com/swarm-routing)
# SPDX-License-Identifier: MIT OR GPL-2.0-only
"""The bridge between the org's mirror tickets and ns-3's GitLab (nsnam/ns-3-dev).

Usage:
  upstream.py validate [--tracked FILE]
  upstream.py lint     [--tracked FILE] [--root DIR]
  upstream.py read     [--tracked FILE] [--dry-run] [--now ISO-8601]
  upstream.py plan     [--tracked FILE] [--root DIR] [--dry-run] [--now ISO-8601]
  upstream.py send     [--tracked FILE] [--root DIR] [--dry-run] [--now ISO-8601]
  upstream.py verify-matrix [--tracked FILE]
  upstream.py verify   [--tracked FILE] ITEM --ns3 DIR --out FILE
  upstream.py verify-record [--tracked FILE] --results DIR --run-url URL [--dry-run]
  upstream.py lifecycle [--tracked FILE] [--dry-run] [--now ISO-8601]
  upstream.py --self-test

`tracked.toml` is the register: one `[[item]]` per upstream report, keyed by
its NS3-NNN id (swarm-routing/ns3-tools docs/ns3-bugs.md), with its RCAPA
ticket, its state (`draft`, `published`, `closed`), its GitLab issue and
merge requests, the mirror ticket here (`mirror`, once known), the P1 verify
evidence (`verified = { date, master, run }`) and, optionally, its own
`gate`. The top level sets the gate policy: `gate = "veto-window"` (default;
a post goes out `window_hours_new_issue` = 48 h after it is scheduled for a
new issue, `window_hours` = 24 h for a note), `"auto"` (as soon as it is
scheduled) or `"approve"` (as soon as it is scheduled; the `gitlab-write`
environment approves the run if it has a required reviewer). An item enters only by a
reviewed pull request to `main`, and nothing is posted for an item not
listed, so the file is also the allowlist.

read      For every item: finds its mirror ticket (`mirror`, else an issue the
          workflow itself opened with the item's `<!-- upstream-id: … -->`
          marker), or opens one. For an item with GitLab references it fetches
          each issue and merge request (state, detailed_merge_status,
          reviewers, approvals, head pipeline, labels) and their notes, adds
          every user note not mirrored yet as a comment (author, date, link;
          deduplicated by the note id in a first-line marker), rewrites the
          status block of the ticket, and comments when the stall flags
          change: no activity for 14 days, need_rebase or conflicts, no
          reviewer, an unanswered question (the last user note is not ours
          and asks something: a `?` or "please"), a competing merge request
          mentioned. With GITLAB_TOKEN it reads notes from the REST API;
          without it from the public `discussions.json` pages, so it runs,
          and its tests run, without any secret.
lint      Checks every file of `reports/` and `replies/` (CI, on each PR).
plan      Without any secret: lists what would be posted. The text comes only
          from merged files, never from a comment: `reports/NS3-NNN.md` (its
          first line `# <GitLab title>`) becomes a new GitLab issue, and
          `replies/NS3-NNN/<note-id>.md` a note in the thread of that mirrored
          note. Each post is checked by the guards (references: no bare `#N` /
          `!N` / `owner/repo#N` that GitLab would link, ours as full GitHub
          URLs; the DISCLOSURE footer added; scope: only the item's own issue
          and merge requests, nsnam/ns-3-dev only; rate limits: one new issue
          per day, one note per thread per 24 h; evidence: a report needs P1
          verify from the last 7 days). A post that fails a guard gets one
          "not scheduled" comment saying why; a new or changed text gets an
          `upstream-scheduled` comment with its exact text and due time; the
          `hold` label on the ticket stops every post of it. Writes
          `ready=true|false` to $GITHUB_OUTPUT.
send      In the `gitlab-write` environment: recomputes the plan and posts
          every ready post, then links it back with an `upstream-sent` marker.
verify    P1: an item's `reproduce` table (`modules`, `files` pinned to a
          swarm-routing commit, optional `setup`, `run`, `expect` = `exit N` or
          `output TEXT`) run in an ns-3-dev `master` tree; `verify-record`
          comments the result with an `upstream-verified` marker, the evidence
          plan reads besides `verified` in tracked.toml.
lifecycle P6: a merged MR of ours whose commit an `ns-3.X` tag contains: "fixed
          in ns-3.X" on the ticket, one issue per `workarounds` entry (with the
          org bot's token, BOT_TOKEN), and the RCAPA ticket closed once all of
          them are closed.

GitHub is reached with GITHUB_TOKEN (the workflow's, issues: write) and
GitLab with GITLAB_TOKEN (an environment secret; read_api for gitlab-read,
api for gitlab-write). Neither token is ever printed, logged or put in a URL.
Stdlib only; Python 3.11+. Output: one summary line per action (AntHocNet
ADR-0014).
"""

import argparse
import contextlib
import hashlib
import io
import json
import os
import re
import sys
import tempfile
import tomllib
import urllib.error
import urllib.parse
import urllib.request
from datetime import datetime, timedelta, timezone

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(os.path.dirname(HERE))
TRACKED = os.path.join(ROOT, "tracked.toml")
GITHUB_API = "https://api.github.com"
GITLAB = "https://gitlab.com"
STATES = ("draft", "published", "closed")
ITEM_KEYS = (
    "id",
    "title",
    "state",
    "rcapa",
    "issue",
    "merge_requests",
    "competing",
    "mirror",
    "draft",
    "verified",
    "gate",
    "reproduce",
    "workarounds",
)
RELEASE_TAG = re.compile(r"ns-3\.\d+(?:\.\d+)?")
GATES = ("veto-window", "auto", "approve")
ALLOWED_PROJECT = "nsnam/ns-3-dev"  # the only GitLab project the bridge writes to
HOLD = "hold"  # the label that stops every post of a mirror ticket
EVIDENCE_DAYS = 7  # a report needs P1 verify evidence this recent
ID = re.compile(r"NS3-\d{3}")
GH_REF = re.compile(r"swarm-routing/[A-Za-z0-9_.-]+#\d+")
GL_REF = re.compile(r"(?P<project>[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)?(?P<kind>[#!])(?P<iid>\d+)")
GL_URL = re.compile(
    r"https://gitlab\.com/(?P<project>[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)/-/"
    r"(?P<path>issues|work_items|merge_requests)/(?P<iid>\d+)"
)
# a reference GitLab would link: `#N`, `!N`, `group/project#N` (not inside a word or a path)
OUTBOUND_REF = re.compile(
    r"(?<![\w/.\-#!])(?P<ref>[A-Za-z0-9_.-]+(?:/[A-Za-z0-9_.-]+)+)?(?P<kind>[#!])(?P<n>\d+)(?!\w)"
)
ANY_URL = re.compile(r"https?://[^\s)\]>]+")
STALL_DAYS = 14
MAX_PAGES = 100  # pages of one thread's notes at most (a guard against a cursor loop)
MAX_NEW_NOTES = 50  # comments one run adds per item at most; the rest come next run
# the last upstream note asks something of us: a question mark or a request
QUESTION = re.compile(r"\?|\bplease\b", re.I)
# every automated post ends with it (swarm-routing/ns3-upstream-report#2 guard 2; wording: D2)
DISCLOSURE = (
    "Posted by swarm-routing's upstream bridge; drafted with AI assistance (Claude) "
    "and checked by its CI, not written by hand."
)
STATUS_BEGIN, STATUS_END = "<!-- upstream-status:begin -->", "<!-- upstream-status:end -->"
FOOTER = "\n\n---\n_Posted by the [upstream bridge](https://github.com/swarm-routing/ns3-upstream-report) read workflow._"


class HttpError(Exception):
    def __init__(self, method, url, status, message):
        super().__init__(f"{method} {url}: HTTP {status}: {message}")
        self.status = status


def http(method, url, headers, body=None):
    """One HTTP request: (status, parsed JSON or None, response headers lowercased).
    Request headers are never in the error."""
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(url, data=data, method=method, headers=headers)
    if data is not None:
        req.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(req, timeout=60) as r:
            raw = r.read()
            got = {k.lower(): v for k, v in r.headers.items()}
            return r.status, (json.loads(raw) if raw.strip() else None), got
    except urllib.error.HTTPError as e:
        msg = e.read().decode(errors="replace")[:300]
        raise HttpError(method, url, e.code, msg) from None


# --- the register --------------------------------------------------------------


def load_tracked(path, base=None):
    with open(contained(base or os.getcwd(), path), "rb") as f:
        cfg = tomllib.load(f)
    errors = validate(cfg)
    if errors:
        raise SystemExit("tracked.toml: " + "; ".join(errors))
    return cfg


def validate(cfg):
    """What is wrong with the register."""
    errors = []
    proj = cfg.get("gitlab_project")
    if not isinstance(proj, str) or not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", proj):
        errors.append("gitlab_project is not a group/project path")
    accounts = cfg.get("our_accounts", [])
    if not isinstance(accounts, list) or not all(isinstance(a, str) for a in accounts):
        errors.append("our_accounts is not a list of GitLab usernames")
    if not isinstance(cfg.get("bot", "github-actions[bot]"), str):
        errors.append("bot is not a GitHub login")
    if cfg.get("gate", "veto-window") not in GATES:
        errors.append(f"gate is not one of {', '.join(GATES)}")
    for k in ("window_hours", "window_hours_new_issue"):
        if k in cfg and (type(cfg[k]) is not int or not 0 <= cfg[k] <= 24 * 14):
            errors.append(f"{k} is not a number of hours from 0 to 336")
    seen = set()
    for n, it in enumerate(cfg.get("item", [])):
        where = it.get("id", f"item {n + 1}") if isinstance(it, dict) else f"item {n + 1}"
        if not isinstance(it, dict):
            errors.append(f"{where} is not a table")
            continue
        errors += [
            f"{where}: {k} is not one of {', '.join(ITEM_KEYS)}" for k in it if k not in ITEM_KEYS
        ]
        if not isinstance(it.get("id"), str) or not ID.fullmatch(it["id"]):
            errors.append(f"{where}: id is not NS3-NNN")
        elif it["id"] in seen:
            errors.append(f"{where}: id listed twice")
        seen.add(it.get("id"))
        if not isinstance(it.get("title"), str) or not it["title"].strip():
            errors.append(f"{where}: title is missing")
        if it.get("state") not in STATES:
            errors.append(f"{where}: state is not one of {', '.join(STATES)}")
        if "rcapa" in it and not (isinstance(it["rcapa"], str) and GH_REF.fullmatch(it["rcapa"])):
            errors.append(f"{where}: rcapa is not swarm-routing/<repo>#N")
        for k in ("issue", "mirror"):
            if k in it and (type(it[k]) is not int or it[k] < 1):
                errors.append(f"{where}: {k} is not an issue number")
        for k in ("merge_requests", "competing"):
            v = it.get(k, [])
            if not isinstance(v, list) or not all(type(x) is int and x > 0 for x in v):
                errors.append(f"{where}: {k} is not a list of merge request numbers")
        d = it.get("draft", [])
        if not isinstance(d, list) or not all(
            isinstance(x, str) and GH_REF.fullmatch(x) for x in d
        ):
            errors.append(f"{where}: draft is not a list of swarm-routing/<repo>#N")
        if "reproduce" in it:
            errors += validate_reproduce(where, it["reproduce"])
        w = it.get("workarounds", [])
        if not isinstance(w, list) or not all(
            isinstance(x, str) and GH_REF.fullmatch(x) for x in w
        ):
            errors.append(f"{where}: workarounds is not a list of swarm-routing/<repo>#N")
        if "gate" in it and it["gate"] not in GATES:
            errors.append(f"{where}: gate is not one of {', '.join(GATES)}")
        v = it.get("verified", {})
        if not isinstance(v, dict) or (
            v
            and not (
                re.fullmatch(r"\d{4}-\d{2}-\d{2}", str(v.get("date", "")))
                and isinstance(v.get("master"), str)
                and str(v.get("run", "")).startswith("https://github.com/")
                and set(v) <= {"date", "master", "run"}
            )
        ):
            errors.append(f"{where}: verified is not {{ date = YYYY-MM-DD, master, run = URL }}")
        if it.get("state") == "published" and not (it.get("issue") or it.get("merge_requests")):
            errors.append(f"{where}: published without an issue or a merge request")
    return errors


def ref_in_item(it, kind, iid):
    if kind == "#":
        return it.get("issue") == iid
    return iid in it.get("merge_requests", [])


def refs(it, extra=()):
    """The item's GitLab references, ours first: [(kind, iid, mirrored notes?)]."""
    out = []
    if it.get("issue"):
        out.append(("#", it["issue"], True))
    out += [("!", i, True) for i in it.get("merge_requests", [])]
    out += [("!", i, False) for i in it.get("competing", [])]
    for kind, iid in extra:
        if (kind, iid) not in [(k, i) for k, i, _ in out]:
            out.append((kind, iid, True))
    return out


# --- GitLab --------------------------------------------------------------------


class GitLab:
    def __init__(self, project, token=None, transport=http, base=GITLAB):
        self.project, self.token, self.transport, self.base = project, token, transport, base
        self.allowed, self.dry = set(), False  # the threads notes may go to; dry run
        self.api = f"{base}/api/v4/projects/{urllib.parse.quote(project, safe='')}"

    def _get(self, url):
        headers = {"Accept": "application/json", "User-Agent": "ns3-upstream-report"}
        if self.token:
            headers["PRIVATE-TOKEN"] = self.token
        return self.transport("GET", url, headers)[1]

    def _post(self, url, body):
        """The scope guard, then the write: an issue, or a note on an allowed thread, in
        ALLOWED_PROJECT only; never a close, label, approval or merge."""
        rel = url[len(self.api) :] if url.startswith(self.api) else url
        m = re.fullmatch(r"/(issues|merge_requests)/(\d+)/notes", rel)
        thread = (("#" if m.group(1) == "issues" else "!"), int(m.group(2))) if m else None
        if self.project != ALLOWED_PROJECT or not (
            rel == "/issues" or (thread and thread in self.allowed)
        ):
            raise SystemExit(f"scope: {self.project} {rel} is not a write the bridge may make")
        if self.dry:
            return {"iid": 0, "id": "0", "web_url": "(dry run)"}
        if not self.token:
            raise SystemExit(
                "GITLAB_TOKEN is not set: writing to GitLab needs the gitlab-write token"
            )
        headers = {"Accept": "application/json", "User-Agent": "ns3-upstream-report"}
        headers["PRIVATE-TOKEN"] = self.token
        return self.transport("POST", url, headers, body)[1]

    @staticmethod
    def path(kind):
        return "issues" if kind == "#" else "merge_requests"

    def item(self, kind, iid):
        got = self._get(f"{self.api}/{self.path(kind)}/{iid}")
        if kind == "!":
            try:
                got["_approvals"] = self._get(f"{self.api}/merge_requests/{iid}/approvals")
            except HttpError:
                got["_approvals"] = None
        return got

    def notes(self, kind, iid, web_url):
        """Every note, oldest first, as {id, author, name, created_at, body, system, url}."""
        raw = []
        if self.token:
            page = 1
            while True:
                got = self._get(
                    f"{self.api}/{self.path(kind)}/{iid}/notes"
                    f"?sort=asc&order_by=created_at&per_page=100&page={page}"
                )
                raw += got
                if len(got) < 100:
                    break
                page += 1
        else:
            # the public page is cursor-paginated (20 discussions a page): follow
            # x-next-page-cursor to the end, or the newest notes are lost
            url = f"{self.base}/{self.project}/-/{self.path(kind)}/{iid}/discussions.json"
            headers = {"Accept": "application/json", "User-Agent": "ns3-upstream-report"}
            cursor = None
            for _ in range(MAX_PAGES):
                q = f"?cursor={urllib.parse.quote(cursor, safe='')}" if cursor else ""
                got = self.transport("GET", url + q, headers)
                for d in got[1] or []:
                    raw += d.get("notes", [])
                cursor = (got[2] if len(got) > 2 else {}).get("x-next-page-cursor")
                if not cursor:
                    break
            else:
                raise SystemExit(f"{url}: more than {MAX_PAGES} pages")
        out = [
            {
                "id": str(n["id"]),
                "author": n.get("author", {}).get("username", "?"),
                "name": n.get("author", {}).get("name", ""),
                "created_at": n.get("created_at", ""),
                "body": n.get("body", n.get("note", "")) or "",
                "system": bool(n.get("system")),
                "url": f"{web_url}#note_{n['id']}",
            }
            for n in raw
        ]
        return sorted(out, key=lambda n: (n["created_at"], n["id"]))

    def already_sent(self, cand):
        """GitLab's own evidence that a post went out (the link-back may have failed):
        (iid, url) of an issue with the same title, or the id of a note in the thread
        with the same text; None otherwise."""
        if cand["target"] == 0:
            q = urllib.parse.quote(cand["title"])
            for i in self._get(f"{self.api}/issues?search={q}&in=title&per_page=100") or []:
                if i.get("title") == cand["title"]:
                    return i["iid"], i["web_url"]
            return None
        url = web_url(self.project, cand["kind"], cand["target"])
        for n in self.notes(cand["kind"], cand["target"], url):
            if n["body"].strip() == cand["text"].strip():
                return n["id"]
        return None

    def commit_tags(self, sha):
        """The tags that contain a commit (public, no token needed)."""
        return self._get(f"{self.api}/repository/commits/{sha}/refs?type=tag&per_page=100") or []

    def create_issue(self, title, description):
        got = self._post(f"{self.api}/issues", {"title": title, "description": description})
        return got["iid"], got["web_url"]

    def create_note(self, kind, iid, body):
        got = self._post(f"{self.api}/{self.path(kind)}/{iid}/notes", {"body": body})
        return str(got["id"])


# --- GitHub --------------------------------------------------------------------


class GitHub:
    def __init__(self, repo, token=None, transport=http, dry_run=False):
        self.repo, self.token, self.transport, self.dry = repo, token, transport, dry_run
        self.writes = []

    def _call(self, method, path, body=None):
        headers = {
            "Accept": "application/vnd.github+json",
            "X-GitHub-Api-Version": "2022-11-28",
            "User-Agent": "ns3-upstream-report",
        }
        if self.token:
            headers["Authorization"] = f"Bearer {self.token}"
        if method != "GET":
            self.writes.append((method, path, body))
            if self.dry:
                return {"number": 0, "id": 0, "html_url": "(dry run)"}
        return self.transport(method, f"{GITHUB_API}/{path}", headers, body)[1]

    def _pages(self, path):
        out, page = [], 1
        while True:
            sep = "&" if "?" in path else "?"
            got = self._call("GET", f"{path}{sep}per_page=100&page={page}") or []
            out += got
            if len(got) < 100:
                return out
            page += 1

    def issues(self):
        got = self._pages(f"repos/{self.repo}/issues?state=all")
        return [i for i in got if "pull_request" not in i]

    def issue(self, n):
        return self._call("GET", f"repos/{self.repo}/issues/{n}")

    def comments(self, n):
        return self._pages(f"repos/{self.repo}/issues/{n}/comments")

    def comment(self, cid):
        return self._call("GET", f"repos/{self.repo}/issues/comments/{cid}")

    def create_issue(self, title, body, labels):
        return self._call(
            "POST", f"repos/{self.repo}/issues", {"title": title, "body": body, "labels": labels}
        )

    def update_body(self, n, body):
        return self._call("PATCH", f"repos/{self.repo}/issues/{n}", {"body": body})

    def add_comment(self, n, body):
        return self._call("POST", f"repos/{self.repo}/issues/{n}/comments", {"body": body})

    # other repositories of the org (P6, with the org bot's token)
    def create_issue_in(self, repo, title, body):
        return self._call("POST", f"repos/{repo}/issues", {"title": title, "body": body})

    def issue_state(self, ref):
        repo, _, n = ref.partition("#")
        return (self._call("GET", f"repos/{repo}/issues/{n}") or {}).get("state")

    def comment_in(self, repo, n, body):
        return self._call("POST", f"repos/{repo}/issues/{n}/comments", {"body": body})

    def close(self, repo, n):
        return self._call(
            "PATCH", f"repos/{repo}/issues/{n}", {"state": "closed", "state_reason": "completed"}
        )


# --- markers and text ----------------------------------------------------------


def id_marker(item_id):
    return f"<!-- upstream-id: {item_id} -->"


def first_line_marker(body, name):
    """The value of a `<!-- NAME: value -->` marker on a comment's first line."""
    first = (body or "").split("\n", 1)[0].strip()
    m = re.fullmatch(rf"<!-- {name}: (.+?) -->", first)
    return m.group(1) if m else None


def neutral(text):
    """GitLab text made safe to quote on GitHub: no hidden markers, no @mentions
    of GitHub users, no #N links to this repository's issues."""
    text = text.replace("<!--", "&lt;!--")
    text = re.sub(r"@(?=[A-Za-z0-9])", "@⁠", text)
    return re.sub(r"(?<![\w/])#(?=\d)", "#⁠", text)


def quote(text):
    return "\n".join("> " + line if line else ">" for line in text.splitlines()) or ">"


def ref_label(kind, iid):
    return f"{kind}{iid}"


def web_url(project, kind, iid):
    return f"{GITLAB}/{project}/-/{GitLab.path(kind)}/{iid}"


def parse_target(text, proj):
    """(kind, iid) of a GitLab reference: its full URL, `group/project#N` or `!N`; else None."""
    text = text.strip().rstrip("/")
    m = GL_URL.fullmatch(text)
    if m:
        if m["project"] != proj:
            return None
        return ("!" if m["path"] == "merge_requests" else "#"), int(m["iid"])
    m = GL_REF.fullmatch(text)
    if m and m["project"] == proj:
        return m["kind"], int(m["iid"])
    return None


def lint_outbound(text, proj):
    """What in a text bound for GitLab would link the wrong thing there: a bare `#N` or `!N`
    (GitLab links ns-3's issue or merge request N), `swarm-routing/<repo>#N` or any other
    `owner/repo#N` (GitLab links a GitLab project). Ours must be full GitHub URLs; an ns-3
    item is `nsnam/ns-3-dev#N` or its full GitLab URL. Code spans and blocks are not linked."""
    t = re.sub(r"(?ms)^ {0,3}(```|~~~).*?^ {0,3}\1[^\n]*$", " ", text)
    t = re.sub(r"``[^`]*``|`[^`\n]*`", " ", t)  # code spans (linear: no backtracking)
    t = ANY_URL.sub(" ", t)
    problems = []
    for m in OUTBOUND_REF.finditer(t):
        ref, kind, n = m["ref"], m["kind"], m["n"]
        what = f"{ref or ''}{kind}{n}"
        if ref == proj:
            continue
        if ref and ref.lower().startswith("swarm-routing/"):
            kind_path = "issues" if kind == "#" else "pull"
            problems.append(
                f"`{what}` links a GitLab project on GitLab: write "
                f"https://github.com/{ref}/{kind_path}/{n} (or /pull/{n} for a pull request)"
            )
        elif ref:
            problems.append(f"`{what}` links a GitLab project on GitLab: write the item's full URL")
        else:
            problems.append(
                f"`{what}` links ns-3's {'issue' if kind == '#' else 'merge request'} {n} on "
                f"GitLab: write `{proj}{kind}{n}` for an ns-3 item, or the full "
                f"https://github.com/swarm-routing/<repo>/issues/{n} (or /pull/{n}) for ours"
            )
    return problems


def ticket_body(cfg, it):
    """A mirror ticket's body: the item's references and where its text comes from."""
    proj = cfg["gitlab_project"]
    gitlab = [web_url(proj, "#", it["issue"])] if it.get("issue") else []
    gitlab += [web_url(proj, "!", m) for m in it.get("merge_requests", [])]
    if it["state"] == "draft":
        report = (
            "Draft, not published. Drafted in "
            + ", ".join(it.get("draft", []) or ["the RCAPA ticket"])
            + f"; it is sent from `reports/{it['id']}.md` once a pull request merges it and"
            " P1 verify evidence is recorded."
        )
    else:
        report = "Published before this bridge existed."
    return (
        f"{id_marker(it['id'])}\n"
        f"### NS3 id\n\n{it['id']}\n\n"
        f"### RCAPA ticket\n\n{it.get('rcapa', 'none yet')}\n\n"
        f"### GitLab\n\n" + ("\n".join(gitlab) or "nothing filed yet") + "\n\n"
        f"### Report\n\n{report} Replies are sent from `replies/{it['id']}/<note id>.md`."
        f" Label this ticket `{HOLD}` to stop every post of it.\n\n"
        f"{STATUS_BEGIN}\n{STATUS_END}\n"
    )


def replace_status(body, block):
    block = f"{STATUS_BEGIN}\n{block.strip()}\n{STATUS_END}"
    if STATUS_BEGIN in body and STATUS_END in body:
        head, rest = body.split(STATUS_BEGIN, 1)
        return head + block + rest.split(STATUS_END, 1)[1]
    return body.rstrip("\n") + "\n\n" + block + "\n"


# --- read ----------------------------------------------------------------------


def parse_time(s):
    return datetime.fromisoformat(s.replace("Z", "+00:00"))


def find_ticket(gh, cfg, it, issues):
    """The item's mirror ticket: `mirror`, else an issue the bot opened with its marker."""
    if it.get("mirror"):
        return next((i for i in issues if i["number"] == it["mirror"]), None) or gh.issue(
            it["mirror"]
        )
    bot = cfg.get("bot", "github-actions[bot]")
    for i in sorted(issues, key=lambda x: x["number"]):
        if (i.get("user") or {}).get("login") == bot and id_marker(it["id"]) in (
            i.get("body") or ""
        ):
            return i
    return None


def flags_for(cfg, it, states, notes, now):
    """The stall flags of one item: a list of short strings."""
    ours = set(cfg.get("our_accounts", []))
    known = {(k, i) for k, i, _ in refs(it)}
    flags, last = [], None
    for (kind, iid, mirrored), st in states.items():
        if st.get("state") not in ("opened", None) or not mirrored:
            continue
        label = f"[{ref_label(kind, iid)}]({st.get('web_url') or web_url(cfg['gitlab_project'], kind, iid)})"
        user = [n for n in notes.get((kind, iid), []) if not n["system"]]
        times = [st.get("updated_at")] + [n["created_at"] for n in notes.get((kind, iid), [])]
        times = [parse_time(t) for t in times if t]
        if times:
            last = max([last, max(times)] if last else [max(times)])
        if kind == "!":
            dms = st.get("detailed_merge_status")
            if dms == "need_rebase" or st.get("has_conflicts"):
                flags.append(f"{dms if dms == 'need_rebase' else 'conflicts'} ({label})")
            if not st.get("reviewers"):
                flags.append(f"no reviewer ({label})")
        if user and ours and user[-1]["author"] not in ours and QUESTION.search(user[-1]["body"]):
            n = user[-1]
            flags.append(f"unanswered question ({label}, {n['author']} {n['created_at'][:10]})")
    if last and now - last > timedelta(days=STALL_DAYS):
        flags.insert(0, f"no activity for {(now - last).days} days")
    mentioned = set()
    for (kind, iid), ns in notes.items():
        for n in ns:
            for m in re.finditer(r"(?<![\w/])!(\d+)", n["body"]):
                if ("!", int(m.group(1))) not in known or int(m.group(1)) in it.get(
                    "competing", []
                ):
                    mentioned.add(int(m.group(1)))
    mentioned -= set(it.get("merge_requests", []))
    flags += [
        f"competing merge request [!{m}]({web_url(cfg['gitlab_project'], '!', m)})"
        + ("" if m in it.get("competing", []) else " (not tracked)")
        for m in sorted(mentioned)
    ]
    return flags


def status_block(cfg, it, states, flags):
    proj = cfg["gitlab_project"]
    rows = []
    for (kind, iid, mirrored), st in states.items():
        url = st.get("web_url") or web_url(proj, kind, iid)
        role = "" if mirrored else " (competing)"
        reviewers = ",".join(r.get("username", "?") for r in st.get("reviewers") or []) or "none"
        ap = st.get("_approvals") or {}
        approvals = (
            f"{len(ap.get('approved_by') or [])} given, {ap.get('approvals_left', '?')} left"
            if kind == "!" and ap
            else "–"
        )
        pipe = (st.get("head_pipeline") or {}).get("status", "none") if kind == "!" else "–"
        rows.append(
            f"| [{ref_label(kind, iid)}]({url}){role} | {st.get('state', '?')} | "
            f"{st.get('detailed_merge_status', '–') if kind == '!' else '–'} | "
            f"{reviewers if kind == '!' else '–'} | {approvals} | {pipe} | "
            f"{', '.join(st.get('labels') or []) or 'none'} | {(st.get('updated_at') or '?')[:10]} |"
        )
    out = [
        "### Upstream status",
        "",
        "_Kept by the read workflow; edits here are overwritten._",
        "",
    ]
    if not states:
        out.append(f"**{it['state']}**: nothing on GitLab yet.")
    else:
        out += [
            "| GitLab | State | Merge status | Reviewers | Approvals | Pipeline | Labels | Updated |",
            "|---|---|---|---|---|---|---|---|",
            *rows,
        ]
    out += ["", "**Stalls:** " + ("; ".join(flags) if flags else "none")]
    return "\n".join(out)


def note_comment(proj, kind, iid, n):
    return (
        f"<!-- gitlab-note: {proj}{kind}{iid} {n['id']} -->\n"
        f"**{n['author']}** ({neutral(n['name'])}) on [{proj}{kind}{iid}]({n['url']}), "
        f"{n['created_at'][:16].replace('T', ' ')} UTC:\n\n{quote(neutral(n['body']))}" + FOOTER
    )


def mirrored_ids(cfg, comments):
    """Note ids already on the ticket (mirrored or sent from here), from the bot's first lines."""
    bot = cfg.get("bot", "github-actions[bot]")
    ids, extra, last_flags = set(), [], None
    for c in comments:
        if (c.get("user") or {}).get("login") != bot:
            continue
        body = c.get("body") or ""
        v = first_line_marker(body, "gitlab-note")
        if v:
            ids.add(v.split()[-1])
        v = first_line_marker(body, "upstream-sent")
        if v:
            m = re.search(r"-> (\S+?)([#!])(\d+)(?: note (\S+))?$", v)
            if m:
                if m.group(4):
                    ids.add(m.group(4))
                extra.append((m.group(2), int(m.group(3))))
        v = first_line_marker(body, "upstream-flags")
        if v is not None:
            last_flags = v
    return ids, extra, last_flags


def read_item(gl, gh, cfg, it, issues, now, out):
    proj = cfg["gitlab_project"]
    ticket = find_ticket(gh, cfg, it, issues)
    if ticket is None:
        title = f"[upstream] {it['id']} {it['title']}"
        got = gh.create_issue(title, ticket_body(cfg, it), ["rcapa", "ns3"])
        ticket = {"number": got.get("number", 0), "body": ticket_body(cfg, it)}
        out.append(f"{it['id']}: opened mirror ticket #{ticket['number']}")
        comments = []
    else:
        comments = gh.comments(ticket["number"]) if ticket.get("number") else []
    n = ticket["number"]
    done, extra, last_flags = mirrored_ids(cfg, comments)
    states, notes = {}, {}
    if it["state"] != "draft" or extra:  # a draft the bridge published is tracked at once
        for kind, iid, mirrored in refs(it, extra):
            st = gl.item(kind, iid)
            states[(kind, iid, mirrored)] = st
            notes[(kind, iid)] = gl.notes(kind, iid, st.get("web_url") or web_url(proj, kind, iid))
    added = 0
    for (kind, iid, mirrored), st in states.items():
        if not mirrored:
            continue
        for note in notes[(kind, iid)]:
            if note["system"] or note["id"] in done:
                continue
            if added >= MAX_NEW_NOTES:
                out.append(f"{it['id']}: more notes left for the next run")
                break
            gh.add_comment(n, note_comment(proj, kind, iid, note))
            done.add(note["id"])
            added += 1
    if added:
        out.append(f"{it['id']}: mirrored {added} note(s) on #{n}")
    flags = flags_for(cfg, it, states, notes, now) if states else []
    if n:  # the latest body, so an edit made during this run is kept
        ticket = gh.issue(n) or ticket
    body = ticket.get("body") or ""
    new = replace_status(body, status_block(cfg, it, states, flags))
    if new != body:
        gh.update_body(n, new)
        out.append(f"{it['id']}: status block updated on #{n}")
    key = ",".join(flags)
    if states and key != (last_flags or ""):
        text = (
            "**Stalls:**\n\n" + "\n".join(f"- {f}" for f in flags)
            if flags
            else "**Stalls cleared.**"
        )
        gh.add_comment(n, f"<!-- upstream-flags: {key} -->\n{text}{FOOTER}")
        out.append(f"{it['id']}: stalls {'flagged: ' + key if flags else 'cleared'}")
    if not out or not out[-1].startswith(it["id"]):
        out.append(f"{it['id']}: #{n} up to date")


def cmd_read(cfg, gl, gh, now):
    out = []
    issues = gh.issues()
    for it in cfg.get("item", []):
        if it["state"] == "closed" and not it.get("mirror"):
            continue
        read_item(gl, gh, cfg, it, issues, now, out)
    return out


# --- send: reports and replies from merged files, behind the gate policy ------


def sent_markers(cfg, comments):
    """(value, created_at) of every `upstream-sent` marker the bot wrote."""
    bot = cfg.get("bot", "github-actions[bot]")
    out = []
    for c in comments:
        v = first_line_marker(c.get("body"), "upstream-sent")
        if v and (c.get("user") or {}).get("login") == bot:
            out.append((v, c.get("created_at", "")))
    return out


def outbound(text):
    """The text as it goes out: the file's text and the fixed disclosure footer."""
    return text.strip() + "\n\n---\n" + DISCLOSURE


def digest(*parts):
    return hashlib.sha256(json.dumps(parts, ensure_ascii=False).encode()).hexdigest()


def read_file(base, rel):
    """A file of the repository's `base`, refused outside it; None if missing."""
    try:
        with open(contained(base, rel), encoding="utf-8") as f:
            return f.read()
    except FileNotFoundError:
        return None


def report_file(root, it):
    return os.path.join(root, "reports", f"{it['id']}.md")


def parse_report(text):
    """(title, body) of reports/NS3-NNN.md: a first line `# <GitLab title>`, then the body."""
    first, _, body = text.lstrip("﻿").partition("\n")
    m = re.fullmatch(r"# (\S.*)", first.strip())
    if not m or not body.strip():
        raise ValueError("a report starts with `# <GitLab title>`, then its body")
    return m.group(1), body.strip()


def reply_files(root, it):
    """[(note id, path)] of replies/NS3-NNN/<note-id>.md, in note id order."""
    d = os.path.join(root, "replies", it["id"])
    if not os.path.isdir(d):
        return []
    out = []
    for name in sorted(os.listdir(d)):
        m = re.fullmatch(r"(\d+)\.md", name)
        if m:
            out.append((m.group(1), os.path.join(d, name)))
    return out


def lint_files(cfg, root):
    """Every problem of reports/ and replies/ (CI runs this on each PR)."""
    proj = cfg["gitlab_project"]
    ids = {it["id"] for it in cfg.get("item", [])}
    problems = []
    for sub in ("reports", "replies"):
        base = os.path.join(root, sub)
        for dirpath, _, files in os.walk(base):
            for name in sorted(files):
                if name == "README.md" and dirpath == base:
                    continue  # the directory's own explanation, never sent
                path = os.path.join(dirpath, name)
                rel = os.path.relpath(path, root)
                text = read_file(root, rel) or ""
                if sub == "reports":
                    m = re.fullmatch(r"(NS3-\d{3})\.md", name)
                    if not m or m.group(1) not in ids or dirpath != base:
                        problems.append(f"{rel}: not reports/<an id of tracked.toml>.md")
                        continue
                    try:
                        title, body = parse_report(text)
                    except ValueError as e:
                        problems.append(f"{rel}: {e}")
                        continue
                    text = title + "\n" + body
                else:
                    item = os.path.basename(dirpath)
                    if item not in ids or not re.fullmatch(r"\d+\.md", name):
                        problems.append(f"{rel}: not replies/<an id of tracked.toml>/<note id>.md")
                        continue
                    if not text.strip():
                        problems.append(f"{rel}: empty")
                        continue
                problems += [f"{rel}: {p}" for p in lint_outbound(text, proj)]
                if DISCLOSURE.split(";")[0] in text:
                    problems.append(
                        f"{rel}: the disclosure footer is added on sending; leave it out"
                    )
    return problems


def policy(cfg, it):
    """(gate, window for a new issue, window for a note) of an item."""
    gate = it.get("gate", cfg.get("gate", "veto-window"))
    if gate != "veto-window":
        return gate, timedelta(0), timedelta(0)
    return (
        gate,
        timedelta(hours=cfg.get("window_hours_new_issue", 48)),
        timedelta(hours=cfg.get("window_hours", 24)),
    )


def candidates(cfg, root, it, comments, now):
    """What this item would post: [{key, kind, target, title, text, sha, problems}]."""
    proj = cfg["gitlab_project"]
    out = []
    published = any(v.startswith("publish ") for v, _ in sent_markers(cfg, comments))
    text = read_file(root, os.path.relpath(report_file(root, it), root))
    if text is not None and not (it.get("issue") or published):
        problems = []
        try:
            title, body = parse_report(text)
        except ValueError as e:
            title, body = "", ""
            problems.append(str(e))
        if it["state"] != "draft":
            problems.append(f"{it['id']} is {it['state']}, not a draft: a report is filed once")
        v = evidence(cfg, it, comments)
        when = v.get("date")
        if not when or now - parse_time(f"{when}T00:00:00Z") > timedelta(days=EVIDENCE_DAYS):
            problems.append(
                f"no P1 verify evidence from the last {EVIDENCE_DAYS} days "
                "(`verified = { date, master, run }` in tracked.toml)"
            )
        body = outbound(body)
        problems += lint_outbound(title + "\n" + body, proj)
        out.append(
            {
                "key": f"publish {it['id']}",
                "kind": "#",
                "target": 0,
                "title": title,
                "text": body,
                "sha": digest("publish", it["id"], title, body),
                "problems": problems,
            }
        )
    threads = {}  # note id -> (kind, iid), from the read workflow's mirrored-note markers
    bot = cfg.get("bot", "github-actions[bot]")
    for c in comments:
        v = first_line_marker(c.get("body"), "gitlab-note")
        if v and (c.get("user") or {}).get("login") == bot:
            ref, nid = v.split()
            m = GL_REF.fullmatch(ref)
            if m and m["project"] == proj:
                threads[nid] = (m["kind"], int(m["iid"]))
    sent = {v.split(" -> ")[0] for v, _ in sent_markers(cfg, comments)}
    for nid, path in reply_files(root, it):
        key = f"reply {it['id']}/{nid}"
        if key in sent:
            continue
        body = outbound(read_file(root, os.path.relpath(path, root)) or "")
        problems = []
        target = threads.get(nid)
        if target is None or not ref_in_item(it, *target):
            problems.append(f"note {nid} is not a mirrored note of {it['id']}'s own issue or MRs")
            target = target or ("#", 0)
        problems += lint_outbound(body, proj)
        out.append(
            {
                "key": key,
                "kind": target[0],
                "target": target[1],
                "title": "",
                "text": body,
                "sha": digest("reply", it["id"], nid, target, body),
                "problems": problems,
            }
        )
    return out


def scheduled(cfg, comments):
    """key -> (sha, due) of the latest `upstream-scheduled` marker per key."""
    bot = cfg.get("bot", "github-actions[bot]")
    out = {}
    for c in comments:
        v = first_line_marker(c.get("body"), "upstream-scheduled")
        if v and (c.get("user") or {}).get("login") == bot:
            m = re.fullmatch(r"(.+) sha=([0-9a-f]{64}) due=(\S+)", v)
            if m:
                out[m.group(1)] = (m.group(2), parse_time(m.group(3)))
    return out


def rate_limited(cand, history, now):
    """Why a post must wait for the rate limits, or None. history: [(key, target, when)]."""
    day = now - timedelta(hours=24)
    if cand["key"].startswith("publish "):
        if any(k.startswith("publish ") and t >= day for k, _, t in history):
            return "one new issue per day"
    target = (cand["kind"], cand["target"])
    if any(tg == target and t >= day for _, tg, t in history):
        return "one note per thread per 24 h"
    return None


def history_of(cfg, tickets):
    """Every post the bridge made: [(key, (kind, iid), when)], from the sent markers."""
    out = []
    for _, comments in tickets:
        for v, when in sent_markers(cfg, comments):
            key, _, rest = v.partition(" -> ")
            m = re.match(r"\S+?([#!])(\d+)", rest)
            if m and when:
                out.append((key, (m.group(1), int(m.group(2))), parse_time(when)))
    return out


def plan(cfg, gh, root, now):
    """[(item, ticket, comments, candidate, status)] for every candidate post.
    status: blocked, held, schedule, waiting, rate, ready."""
    issues = gh.issues()
    rows, tickets = [], []
    for it in cfg.get("item", []):
        t = find_ticket(gh, cfg, it, issues)
        if t is None:
            continue
        comments = gh.comments(t["number"])
        tickets.append((t, comments))
        held = any(
            (lb.get("name") if isinstance(lb, dict) else lb) == HOLD for lb in t.get("labels", [])
        )
        sched = scheduled(cfg, comments)
        gate, w_issue, w_note = policy(cfg, it)
        for cand in candidates(cfg, root, it, comments, now):
            if cand["problems"]:
                status = "blocked"
            elif held:
                status = "held"
            elif sched.get(cand["key"], (None,))[0] != cand["sha"]:
                status = "schedule"
                cand["due"] = now + (w_issue if cand["key"].startswith("publish ") else w_note)
            elif sched[cand["key"]][1] > now:
                status = "waiting"
                cand["due"] = sched[cand["key"]][1]
            else:
                status = "ready"
            cand["gate"] = gate
            rows.append([it, t, comments, cand, status])
    history = history_of(cfg, tickets)
    for row in rows:
        if row[4] == "ready":
            why = rate_limited(row[3], history, now)
            if why:
                row[4], row[3]["rate"] = "rate", why
    return rows, history


def cmd_plan(cfg, gh, root, now):
    """Comment the schedule and the blocks on the tickets; returns (lines, ready?)."""
    lines, ready = [], False
    rows, _ = plan(cfg, gh, root, now)
    for it, t, comments, c, status in rows:
        n = t["number"]
        if status == "blocked":
            mark = f"<!-- upstream-blocked: {c['key']} sha={c['sha']} -->"
            if not any((x.get("body") or "").startswith(mark) for x in comments):
                gh.add_comment(
                    n,
                    f"{mark}\n**Not scheduled:** `{c['key']}` fails its guards:\n\n"
                    + "\n".join(f"- {p}" for p in c["problems"])
                    + FOOTER,
                )
            lines.append(f"{c['key']}: blocked: {'; '.join(c['problems'])}")
        elif status == "schedule":
            due = c["due"].strftime("%Y-%m-%dT%H:%M:%SZ")
            where = (
                "a new issue"
                if c["target"] == 0
                else f"a note on [{cfg['gitlab_project']}{c['kind']}{c['target']}]"
                f"({web_url(cfg['gitlab_project'], c['kind'], c['target'])})"
            )
            gh.add_comment(
                n,
                f"<!-- upstream-scheduled: {c['key']} sha={c['sha']} due={due} -->\n"
                f"**Scheduled:** `{c['key']}` goes out as {where} after {due} "
                f"(gate `{c['gate']}`), unless this ticket is labelled `{HOLD}`.\n\n"
                + (f"**Title:** {c['title']}\n\n" if c["title"] else "")
                + f"````markdown\n{c['text']}\n````"
                + FOOTER,
            )
            lines.append(f"{c['key']}: scheduled for {due}")
            ready = ready or c["due"] <= now
        else:
            lines.append(f"{c['key']}: {status}" + (f" ({c['rate']})" if status == "rate" else ""))
            ready = ready or status == "ready"
    if not rows:
        lines.append("nothing to send")
    return lines, ready


def cmd_send(cfg, gh, gl, root, now):
    """Post every ready candidate, re-checked here, and link it back."""
    lines = []
    rows, history = plan(cfg, gh, root, now)
    proj = cfg["gitlab_project"]
    for it, t, _, c, status in rows:
        if status == "rate":
            lines.append(f"{c['key']}: waits ({c['rate']})")
        if status != "ready":
            continue
        why = rate_limited(c, history, now)
        if why:
            lines.append(f"{c['key']}: waits ({why})")
            continue
        gl.allowed = {(k, i) for k, i, mine in refs(it) if mine}
        done = gl.already_sent(c)  # a write GitLab took whose link-back was lost
        if c["target"] == 0:
            iid, url = done or gl.create_issue(c["title"], c["text"])
            marker = f"{c['key']} -> {proj}#{iid}"
            msg = (
                f"Published as [{proj}#{iid}]({url}). "
                f'Add `issue = {iid}` and `state = "published"` to {it["id"]} in tracked.toml.'
            )
            history.append((c["key"], ("#", iid), now))
        else:
            nid = done or gl.create_note(c["kind"], c["target"], c["text"])
            url = f"{web_url(proj, c['kind'], c['target'])}#note_{nid}"
            marker = f"{c['key']} -> {proj}{c['kind']}{c['target']} note {nid}"
            msg = f"Sent as [a note on {proj}{c['kind']}{c['target']}]({url})."
            history.append((c["key"], (c["kind"], c["target"]), now))
        gh.add_comment(t["number"], f"<!-- upstream-sent: {marker} -->\n{msg}{FOOTER}")
        lines.append(f"{c['key']}: sent: {url}")
    return lines or ["nothing ready"]


# --- P1 verify and P6 lifecycle -------------------------------------------------


def evidence(cfg, it, comments):
    """The newest P1 evidence of an item: `verified` in tracked.toml, or the verify
    workflow's `upstream-verified` marker on the mirror ticket; {} if none."""
    best = dict(it.get("verified") or {})
    bot = cfg.get("bot", "github-actions[bot]")
    for c in comments:
        v = first_line_marker(c.get("body"), "upstream-verified")
        if not v or (c.get("user") or {}).get("login") != bot:
            continue
        got = dict(x.split("=", 1) for x in v.split() if "=" in x)
        if got.get("result") == "reproduces" and got.get("date", "") > best.get("date", ""):
            best = got
    return best


RAW = re.compile(
    r"https://raw\.githubusercontent\.com/swarm-routing/[A-Za-z0-9_.-]+/[0-9a-f]{40}/\S+"
)


def validate_reproduce(where, r):
    """What is wrong with an item's `reproduce` table."""
    if not isinstance(r, dict):
        return [f"{where}: reproduce is not a table"]
    errors = [
        f"{where}: reproduce.{k} is not one of modules, files, setup, run, expect"
        for k in r
        if k not in ("modules", "files", "setup", "run", "expect")
    ]
    if not re.fullmatch(r"[a-z0-9-]+(;[a-z0-9-]+)*", str(r.get("modules", ""))):
        errors.append(f"{where}: reproduce.modules is not a ;-separated list of ns-3 modules")
    files = r.get("files", {})
    if not isinstance(files, dict) or not all(
        re.fullmatch(r"(scratch|contrib)/[A-Za-z0-9_./-]+", k)
        and ".." not in k
        and RAW.fullmatch(str(v))
        for k, v in files.items()
    ):
        errors.append(
            f"{where}: reproduce.files maps scratch/ or contrib/ paths to raw.githubusercontent.com "
            "swarm-routing URLs pinned to a commit"
        )
    if not isinstance(r.get("run"), str) or not r["run"].strip():
        errors.append(f"{where}: reproduce.run is missing")
    if not re.fullmatch(r"(exit \d+|output .+)", str(r.get("expect", ""))):
        errors.append(f"{where}: reproduce.expect is `exit N` or `output TEXT`")
    return errors


def contained(base, path):
    """`path` resolved, refused unless it lies inside `base` (no traversal, no symlink out)."""
    root = os.path.realpath(base)
    full = os.path.realpath(os.path.join(root, path))
    if os.path.commonpath([root, full]) != root:
        raise SystemExit(f"refused: {path} is outside {base}")
    return full


def cmd_verify(cfg, item_id, ns3, fetch=None, shell=None, out_base=None):
    """P1: put the reproducer into the ns-3-dev tree at `ns3`, configure, build, run; the
    result (reproduces / not reproduced / error) and the master commit go to out_path."""
    import subprocess

    it = next(i for i in cfg["item"] if i["id"] == item_id)
    r = it["reproduce"]
    fetch = fetch or (lambda url: urllib.request.urlopen(url, timeout=60).read())
    shell = shell or (
        lambda cmd, timeout: subprocess.run(
            ["bash", "-c", cmd], cwd=ns3, capture_output=True, text=True, timeout=timeout
        )
    )
    # the result's name comes from the register, never from an argument
    out_dir = os.path.join(out_base or os.getcwd(), "results")
    os.makedirs(out_dir, exist_ok=True)
    out_path = os.path.join(out_dir, it["id"] + ".json")
    for rel, url in sorted(r.get("files", {}).items()):
        path = contained(ns3, rel)
        os.makedirs(os.path.dirname(path), exist_ok=True)
        with open(path, "wb") as f:
            f.write(fetch(url))
    master = shell("git rev-parse HEAD", 60).stdout.strip()
    steps = []
    if r.get("setup"):
        steps.append(r["setup"])
    steps += [
        "./ns3 configure -d default --enable-examples --enable-tests --disable-werror "
        f"--enable-modules='{r['modules']}'",
        "./ns3 build",
    ]
    result = "error"
    for step in steps:
        p = shell(step, 3600)
        if p.returncode != 0:
            result, detail = "error", f"`{step}` exited {p.returncode}: {p.stderr[-500:]}"
            break
    else:
        p = shell(r["run"], 3600)
        kind, _, want = r["expect"].partition(" ")
        seen = p.returncode == int(want) if kind == "exit" else want in (p.stdout + p.stderr)
        result = "reproduces" if seen else "not-reproduced"
        detail = f"`{r['run']}` exited {p.returncode}"
    got = {"item": item_id, "master": master, "result": result, "detail": detail}
    with open(out_path, "w", encoding="utf-8") as f:
        json.dump(got, f)
    print(f"{item_id}: {result} on master {master[:12]} ({detail})")
    return got


def cmd_verify_record(cfg, gh, results, run_url, now):
    """Comment each P1 result on its mirror ticket, with the marker plan reads."""
    issues = gh.issues()
    lines = []
    for got in results:
        it = next(i for i in cfg["item"] if i["id"] == got["item"])
        t = find_ticket(gh, cfg, it, issues)
        if t is None:
            lines.append(f"{it['id']}: no mirror ticket yet")
            continue
        day = now.strftime("%Y-%m-%d")
        marker = (
            f"<!-- upstream-verified: date={day} master={got['master']} run={run_url} "
            f"result={got['result']} -->"
        )
        verdict = {
            "reproduces": "the bug **reproduces** on ns-3-dev `master`",
            "not-reproduced": "the bug **does not reproduce** on ns-3-dev `master`: check "
            "whether it was fixed upstream before anything is sent",
            "error": "the reproducer **could not run**",
        }[got["result"]]
        gh.add_comment(
            t["number"],
            f"{marker}\n**P1 verify** ({day}): {verdict} "
            f"(commit `{got['master'][:12]}`; {got['detail']}). [Run]({run_url}).{FOOTER}",
        )
        lines.append(f"{it['id']}: recorded {got['result']} on #{t['number']}")
    return lines


def cmd_lifecycle(cfg, gl, gh, now, gh_org=None):
    """P6: a merged merge request of ours whose commit is in an ns-3 release tag sets
    'fixed in ns-3.X' on the ticket and opens one issue per workaround (gh_org: a client
    for other repositories, the org bot's token); once every one of them is closed, the
    RCAPA ticket is closed. Without gh_org it says what is waiting for the token."""
    issues = gh.issues()
    lines = []
    for it in cfg.get("item", []):
        t = find_ticket(gh, cfg, it, issues)
        if t is None:
            continue
        comments = gh.comments(t["number"])
        released = None
        for c in comments:
            v = first_line_marker(c.get("body"), "upstream-released")
            if v and (c.get("user") or {}).get("login") == cfg.get("bot", "github-actions[bot]"):
                released = v
        if released is None:
            for mr in it.get("merge_requests", []):
                st = gl.item("!", mr)
                sha = st.get("merge_commit_sha") or st.get("squash_commit_sha")
                if st.get("state") != "merged" or not sha:
                    continue
                tags = sorted(
                    (x["name"] for x in gl.commit_tags(sha) if RELEASE_TAG.fullmatch(x["name"])),
                    key=lambda n: [int(p) for p in n[5:].split(".")],
                )
                if tags:
                    released = f"{tags[0]} !{mr} {sha}"
                    gh.add_comment(
                        t["number"],
                        f"<!-- upstream-released: {released} -->\n**Fixed in {tags[0]}**: "
                        f"[!{mr}]({web_url(cfg['gitlab_project'], '!', mr)}) merged as `{sha[:12]}`, "
                        f"which the release tag contains.{FOOTER}",
                    )
                    lines.append(f"{it['id']}: fixed in {tags[0]}")
                    break
        if released is None:
            continue
        opened = [
            first_line_marker(c.get("body"), "upstream-workaround")
            for c in comments
            if first_line_marker(c.get("body"), "upstream-workaround")
        ]
        todo = [
            w for w in it.get("workarounds", []) if not any(o.startswith(w + " ") for o in opened)
        ]
        if todo and gh_org is None:
            lines.append(
                f"{it['id']}: workaround issues wait for the org bot's token: {', '.join(todo)}"
            )
            continue
        tag = released.split()[0]
        for w in todo:
            repo, _, num = w.partition("#")
            got = gh_org.create_issue_in(
                repo,
                f"Remove the {it['id']} workaround: fixed in {tag}",
                f"{it['id']} ({it.get('rcapa', '')}) is fixed in {tag}. Remove the workaround "
                f"{w} names, then close this issue; the RCAPA ticket closes when every "
                f"workaround issue is closed (PROC-001).\n\nMirror ticket: "
                f"https://github.com/{gh.repo}/issues/{t['number']}{FOOTER}",
            )
            gh.add_comment(
                t["number"],
                f"<!-- upstream-workaround: {w} {repo}#{got['number']} -->\n"
                f"Opened {repo}#{got['number']} to remove the workaround of {w}.{FOOTER}",
            )
            opened.append(f"{w} {repo}#{got['number']}")
            lines.append(f"{it['id']}: opened {repo}#{got['number']}")
        if gh_org is None or not it.get("rcapa"):
            continue
        refs_open = [o.split()[1] for o in opened]
        states = [gh_org.issue_state(r) for r in refs_open]
        repo, _, num = it["rcapa"].partition("#")
        if all(s == "closed" for s in states) and gh_org.issue_state(it["rcapa"]) == "open":
            gh_org.comment_in(
                repo,
                int(num),
                f"Fixed in {tag}, and every workaround issue is closed "
                f"({', '.join(refs_open) or 'none listed'}): closing (PROC-001).{FOOTER}",
            )
            gh_org.close(repo, int(num))
            lines.append(f"{it['id']}: closed {it['rcapa']}")
    return lines or ["nothing released"]


# --- command line --------------------------------------------------------------


def clients(cfg, dry_run=False):
    repo = os.environ.get("GITHUB_REPOSITORY", "swarm-routing/ns3-upstream-report")
    gh = GitHub(repo, os.environ.get("GITHUB_TOKEN") or None, dry_run=dry_run)
    gl = GitLab(cfg["gitlab_project"], os.environ.get("GITLAB_TOKEN") or None)
    return gh, gl


def write_output(outputs, summary):
    if os.environ.get("GITHUB_OUTPUT"):
        with open(os.environ["GITHUB_OUTPUT"], "a", encoding="utf-8") as f:
            for k, v in outputs.items():
                f.write(f"{k}={v}\n")
    if os.environ.get("GITHUB_STEP_SUMMARY"):
        with open(os.environ["GITHUB_STEP_SUMMARY"], "a", encoding="utf-8") as f:
            f.write(summary)
    print(summary)


def main(argv=None):
    p = argparse.ArgumentParser(description=__doc__.split("\n", 1)[0])
    p.add_argument("--self-test", action="store_true")
    sub = p.add_subparsers(dest="cmd")
    for name in (
        "validate",
        "lint",
        "read",
        "plan",
        "send",
        "verify-matrix",
        "verify",
        "verify-record",
        "lifecycle",
    ):
        s = sub.add_parser(name)
        s.add_argument("--tracked", default=TRACKED)
        if name in ("lint", "plan", "send"):
            s.add_argument("--root", default=ROOT)
        if name in ("read", "plan", "send", "verify-record", "lifecycle"):
            s.add_argument("--dry-run", action="store_true")
            s.add_argument("--now")
        if name == "verify":
            s.add_argument("item")
            s.add_argument("--ns3", required=True)
        if name == "verify-record":
            s.add_argument("--results", required=True)
            s.add_argument("--run-url", required=True)
    a = p.parse_args(argv)
    if a.self_test:
        return self_test()
    if not a.cmd:
        p.print_help()
        return 2
    cfg = load_tracked(a.tracked)
    if a.cmd == "validate":
        print(f"tracked.toml: {len(cfg.get('item', []))} item(s), valid")
        return 0
    if a.cmd == "lint":
        problems = lint_files(cfg, a.root)
        for x in problems:
            print(x)
        print(f"reports/, replies/: {len(problems)} problem(s)")
        return 1 if problems else 0
    if a.cmd == "verify-matrix":
        ids = [
            i["id"] for i in cfg.get("item", []) if i.get("reproduce") and i["state"] != "closed"
        ]
        write_output({"items": json.dumps(ids)}, f"verify: {', '.join(ids) or 'nothing'}\n")
        return 0
    if a.cmd == "verify":
        got = cmd_verify(cfg, a.item, a.ns3)
        return 0 if got["result"] != "error" else 1
    gh, gl = clients(cfg, a.dry_run)
    gl.dry = a.dry_run
    now = parse_time(a.now) if a.now else datetime.now(timezone.utc)
    if a.cmd == "read":
        lines = cmd_read(cfg, gl, gh, now)
    elif a.cmd == "plan":
        lines, ready = cmd_plan(cfg, gh, a.root, now)
        write_output({"ready": "true" if ready else "false"}, "\n".join(lines) + "\n")
        lines = []
    elif a.cmd == "verify-record":
        results = []
        base = contained(os.getcwd(), a.results)
        for name in sorted(os.listdir(base)):
            if not re.fullmatch(r"NS3-\d{3}\.json", name):
                continue
            with open(contained(base, name), encoding="utf-8") as f:
                results.append(json.load(f))
        lines = cmd_verify_record(cfg, gh, results, a.run_url, now)
    elif a.cmd == "lifecycle":
        org = os.environ.get("BOT_TOKEN") or None
        gh_org = GitHub(gh.repo, org, dry_run=a.dry_run) if org else None
        lines = cmd_lifecycle(cfg, gl, gh, now, gh_org)
    else:
        lines = cmd_send(cfg, gh, gl, a.root, now)
    for line in lines:
        print(line)
    if a.dry_run:
        print(f"dry run: {len(gh.writes)} GitHub write(s) not sent")
    return 0


# --- self-test -----------------------------------------------------------------


def self_test():
    failures = []

    def check(name, cond):
        if not cond:
            failures.append(name)

    fx = os.path.join(HERE, "fixtures")

    def load(name):
        with open(os.path.join(fx, name), encoding="utf-8") as f:
            return json.load(f)

    cfg = tomllib.loads(
        """
gitlab_project = "nsnam/ns-3-dev"
our_accounts = ["danieljoppi"]
[[item]]
id = "NS3-003"
title = "dsdv: null output device"
state = "published"
rcapa = "swarm-routing/ns3-tools#240"
issue = 1364
merge_requests = [2985]
competing = [3011]
[[item]]
id = "NS3-001"
title = "AODV livelock"
state = "draft"
rcapa = "swarm-routing/ns3-tools#238"
draft = ["swarm-routing/ns3-containers#55"]
"""
    )
    check("the test register is valid", validate(cfg) == [])
    for name, patch, want in (
        ("bad id", {"id": "NS3-3"}, "id is not NS3-NNN"),
        ("bad state", {"state": "sent"}, "state is not one of"),
        ("unknown key", {"token": "x"}, "token is not one of"),
        ("bad rcapa", {"rcapa": "ns3-tools#240"}, "rcapa is not"),
        ("bad mr", {"merge_requests": ["2985"]}, "merge_requests is not"),
        ("bad gate", {"gate": "later"}, "gate is not one of"),
        ("bad evidence", {"verified": {"date": "yesterday"}}, "verified is not"),
        ("published with nothing", {"issue": None}, None),
    ):
        it = dict(cfg["item"][0], **{k: v for k, v in patch.items() if v is not None})
        if name == "published with nothing":
            it = {k: v for k, v in it.items() if k not in ("issue", "merge_requests")}
            want = "published without"
        errs = validate(dict(cfg, item=[it]))
        check(f"register refused: {name}", any(want in e for e in errs))
    check(
        "register refused: an id twice",
        any("twice" in e for e in validate(dict(cfg, item=[cfg["item"][0]] * 2))),
    )
    with open(TRACKED, "rb") as f:
        check("the repository's tracked.toml is valid", validate(tomllib.load(f)) == [])

    # a fake GitLab from the recorded fixtures (nsnam/ns-3-dev, 2026-11-16) and a fake GitHub
    api = f"{GITLAB}/api/v4/projects/nsnam%2Fns-3-dev"
    gl_data = {
        f"{api}/issues/1364": load("issue-1364.json"),
        f"{api}/merge_requests/2985": load("mr-2985.json"),
        f"{api}/merge_requests/2985/approvals": load("mr-2985-approvals.json"),
        f"{api}/merge_requests/3011": load("mr-3011.json"),
        f"{GITLAB}/nsnam/ns-3-dev/-/issues/1364/discussions.json": load(
            "issue-1364-discussions.json"
        ),
        f"{GITLAB}/nsnam/ns-3-dev/-/merge_requests/2985/discussions.json": load(
            "mr-2985-discussions.json"
        ),
        # the public page is cursor-paginated: its second page holds the newest notes
        # (recorded 2026-11-16; tokenless reads before the fix never saw them)
        f"{GITLAB}/nsnam/ns-3-dev/-/merge_requests/2985/discussions.json?cursor=c2": load(
            "mr-2985-discussions-2.json"
        ),
        f"{GITLAB}/nsnam/ns-3-dev/-/merge_requests/3011/discussions.json": [],
    }
    gl_calls, gl_created = [], []

    def gl_transport(method, url, headers, body=None):
        gl_calls.append((method, url, "PRIVATE-TOKEN" in headers, body))
        if method == "POST":
            if "PRIVATE-TOKEN" not in headers:
                raise HttpError(method, url, 401, "401 Unauthorized")
            if url.endswith("/issues"):
                iid = 1400 + len(gl_created)
                got = {
                    "iid": iid,
                    "title": body["title"],
                    "web_url": f"{GITLAB}/nsnam/ns-3-dev/-/issues/{iid}",
                }
                gl_created.append(got)
                gl_data[f"{api}/issues/{iid}"] = dict(
                    got, state="opened", labels=[], updated_at="2026-11-18T13:00:00.000Z"
                )
                gl_data[f"{GITLAB}/nsnam/ns-3-dev/-/issues/{iid}/discussions.json"] = []
                return 201, got
            nid = str(9000000 + len(gl_calls))
            disc = url.replace(api, f"{GITLAB}/nsnam/ns-3-dev/-").split("/notes")[0]
            gl_data.setdefault(disc + "/discussions.json", []).append(
                {
                    "notes": [
                        {
                            "id": nid,
                            "system": False,
                            "note": body["body"],
                            "created_at": "2026-11-18T13:00:00.000Z",
                            "author": {"username": "danieljoppi", "name": "Daniel"},
                        }
                    ]
                }
            )
            return 201, {"id": int(nid)}
        if "/issues?search=" in url:
            return 200, [dict(i) for i in gl_created]
        if url.endswith("/notes?sort=asc&order_by=created_at&per_page=100&page=1"):
            if "PRIVATE-TOKEN" not in headers:
                raise HttpError(method, url, 401, "401 Unauthorized")
            disc = url.replace(api, f"{GITLAB}/nsnam/ns-3-dev/-").split("/notes?")[0]
            pages = gl_data[disc + "/discussions.json"] + gl_data.get(
                disc + "/discussions.json?cursor=c2", []
            )
            notes = [n for d in pages for n in d["notes"]]
            return 200, [dict(n, id=int(n["id"]) if n["id"].isdigit() else n["id"]) for n in notes]
        if url not in gl_data:
            raise HttpError(method, url, 404, "404 Not Found")
        nxt = {"x-next-page-cursor": "c2"} if f"{url}?cursor=c2" in gl_data else {}
        return 200, json.loads(json.dumps(gl_data[url])), nxt

    class FakeGitHub:
        def __init__(self):
            self.issues, self.comments, self.next = {}, {}, 1
            self.clock = "2026-11-16T12:00:00Z"  # created_at of the comments it adds
            self.calls = []

        def transport(self, method, url, headers, body=None):
            self.calls.append((method, url, body))
            path = url[len(GITHUB_API) + 1 :]
            m = re.match(r"repos/[^/]+/[^/]+/issues(.*)$", path)
            rest = m.group(1)
            if rest.startswith("?state=all"):
                page = int(re.search(r"&page=(\d+)", rest).group(1))
                got = sorted(self.issues.values(), key=lambda i: i["number"])
                return 200, json.loads(json.dumps(got[(page - 1) * 100 : page * 100]))
            if rest == "" and method == "POST":
                n = self.next
                self.next += 1
                self.issues[n] = {
                    "number": n,
                    "title": body["title"],
                    "body": body["body"],
                    "labels": body["labels"],
                    "user": {"login": "github-actions[bot]"},
                }
                self.comments[n] = []
                return 201, {"number": n}
            m2 = re.match(r"/(\d+)$", rest)
            if m2:
                n = int(m2.group(1))
                if method == "PATCH":
                    self.issues[n]["body"] = body["body"]
                return 200, json.loads(json.dumps(self.issues[n]))
            m2 = re.match(r"/(\d+)/comments(?:\?per_page=100&page=(\d+))?$", rest)
            if m2:
                n = int(m2.group(1))
                if method == "POST":
                    return 201, self.add(n, body["body"], "github-actions[bot]")
                page = int(m2.group(2))
                return 200, json.loads(json.dumps(self.comments[n][(page - 1) * 100 : page * 100]))
            m2 = re.match(r"/comments/(\d+)$", rest)
            if m2:
                cid = int(m2.group(1))
                for n, cs in self.comments.items():
                    for c in cs:
                        if c["id"] == cid:
                            return 200, dict(c)
                raise HttpError(method, url, 404, "Not Found")
            raise AssertionError(f"unexpected GitHub call {method} {url}")

        def add(self, n, body, login, association="NONE"):
            cid = 5000 + sum(len(c) for c in self.comments.values())
            c = {
                "id": cid,
                "body": body,
                "user": {"login": login},
                "author_association": association,
                "created_at": self.clock,
                "issue_url": f"{GITHUB_API}/repos/o/r/issues/{n}",
                "html_url": f"https://github.com/o/r/issues/{n}#issuecomment-{cid}",
            }
            self.comments[n].append(c)
            return c

    now = parse_time("2026-11-16T12:00:00Z")
    fake = FakeGitHub()
    gh = GitHub("o/r", "t", fake.transport)
    gl = GitLab("nsnam/ns-3-dev", None, gl_transport)
    out = cmd_read(cfg, gl, gh, now)
    t3 = next(i for i in fake.issues.values() if "NS3-003" in i["title"])
    t1 = next(i for i in fake.issues.values() if "NS3-001" in i["title"])
    user_notes = [
        n
        for f in (
            "issue-1364-discussions.json",
            "mr-2985-discussions.json",
            "mr-2985-discussions-2.json",
        )
        for d in load(f)
        for n in d["notes"]
        if not n["system"]
    ]
    mirrored = [c for c in fake.comments[t3["number"]] if c["body"].startswith("<!-- gitlab-note:")]
    check("tokenless read: no token sent to GitLab", not any(c[2] for c in gl_calls))
    marks = {c["body"].split("\n")[0].split()[-2] for c in mirrored}
    check(
        "tokenless read follows every page: the newest notes of !2985 are mirrored",
        {"3692328913", "3825700321", "3844625389"} <= marks
        and any(c[1].endswith("/discussions.json?cursor=c2") for c in gl_calls),
    )
    check(
        "tokenless read: notes from discussions.json",
        any(c[1].endswith("/merge_requests/2985/discussions.json") for c in gl_calls)
        and not any("/notes?" in c[1] for c in gl_calls),
    )
    check(
        "both mirror tickets opened", len(fake.issues) == 2 and out[0].startswith("NS3-003: opened")
    )
    check(
        "every user note mirrored once, none of the system notes",
        len(mirrored) == len(user_notes) == 18
        and {c["body"].split("\n")[0].split()[-2] for c in mirrored}
        == {n["id"] for n in user_notes},
    )
    first = next(c["body"] for c in mirrored if "3691365625" in c["body"].split("\n")[0])
    check(
        "a mirrored note quotes author, date and link",
        "**tommypec** (Tommaso Pecorella)" in first
        and "2026-08-16 12:33 UTC" in first
        and f"{GITLAB}/nsnam/ns-3-dev/-/merge_requests/2985#note_3691365625" in first
        and "\n> Hi, thanks for reporting" in first,
    )
    check(
        "competing merge request notes are not mirrored",
        not any("!3011 " in c["body"].split("\n")[0] for c in mirrored),
    )
    body3 = fake.issues[t3["number"]]["body"]
    check(
        "status block: state, merge status, reviewers, approvals, pipeline, labels",
        "| [!2985](https://gitlab.com/nsnam/ns-3-dev/-/merge_requests/2985) | opened | need_rebase | none | 0 given, 0 left | success | module::dsdv | 2026-09-16 |"
        in body3
        and "(competing)" in body3
        and body3.count(STATUS_BEGIN) == 1,
    )
    flags = [c for c in fake.comments[t3["number"]] if c["body"].startswith("<!-- upstream-flags:")]
    fl = flags[0]["body"] if flags else ""
    check(
        "stalls flagged: no activity, need_rebase, no reviewer, competing MR; the answered request not flagged",
        len(flags) == 1
        and "no activity for 60 days" in fl
        and "need_rebase ([!2985](https://gitlab.com/nsnam/ns-3-dev/-/merge_requests/2985))" in fl
        and "no reviewer ([!2985](https://gitlab.com/nsnam/ns-3-dev/-/merge_requests/2985))" in fl
        # our "Done: attribution added" (on the second page) answered tomhenderson's request
        and "unanswered question" not in fl
        and "#1364" not in fl
        and "competing merge request [!3011](https://gitlab.com/nsnam/ns-3-dev/-/merge_requests/3011)"
        in fl
        and "(not tracked)" not in fl,
    )
    check(
        "a draft item: a ticket with its draft pointer, nothing fetched",
        "swarm-routing/ns3-containers#55" in t1["body"]
        and "nothing on GitLab yet" in fake.issues[t1["number"]]["body"]
        and fake.comments[t1["number"]] == [],
    )
    quoted = "".join(c["body"].split("\n", 1)[1] for c in mirrored)
    check(
        "issue links neutralised, no marker below the first line",
        not re.search(r"(?<![\w/])#\d", quoted) and "<!--" not in quoted,
    )
    ours = [i["body"] for i in fake.issues.values()] + [
        c["body"] for cs in fake.comments.values() for c in cs
    ]
    bare = []
    for text in ours:
        t = re.sub(r"<!--.*?-->", " ", text)
        t = re.sub(r"\[[^\]]*\]\([^)]*\)", " ", t)  # link text does not autolink
        t = ANY_URL.sub(" ", t)
        bare += re.findall(r"(?<![\w/])#\d+", t)  # on GitHub: this repository's issue
        bare += re.findall(r"(?<!swarm-routing)/[\w.-]+#\d+", t)  # a GitHub repository's issue
    check("our side: every GitLab item as a full URL or a link, no bare #N", bare == [])
    check(
        "the ticket names its GitLab items by full URL",
        "### GitLab\n\nhttps://gitlab.com/nsnam/ns-3-dev/-/issues/1364\n"
        "https://gitlab.com/nsnam/ns-3-dev/-/merge_requests/2985\n" in t3["body"],
    )
    nw = len([c for c in fake.calls if c[0] != "GET"])
    out2 = cmd_read(cfg, gl, gh, now)
    check(
        "a second run writes nothing (dedupe by note id, status and flags unchanged)",
        len([c for c in fake.calls if c[0] != "GET"]) == nw
        and out2 == ["NS3-003: #1 up to date", "NS3-001: #2 up to date"],
    )
    # a new note upstream: mirrored on the next run, nothing else repeated
    disc = gl_data[f"{GITLAB}/nsnam/ns-3-dev/-/merge_requests/2985/discussions.json"]
    disc.append(
        {
            "notes": [
                {
                    "id": "3999999999",
                    "system": False,
                    "body": "Rebased? See #42 and @someone. <!-- upstream-sent: x -> a#1 -->",
                    "created_at": "2026-11-15T10:00:00.000Z",
                    "author": {"username": "danieljoppi", "name": "Daniel"},
                }
            ]
        }
    )
    out3 = cmd_read(cfg, gl, gh, now)
    new = fake.comments[t3["number"]]
    check(
        "a new note: one comment, flags updated (our reply, recent activity)",
        "NS3-003: mirrored 1 note(s) on #1" in out3
        and new[-1]["body"].startswith("<!-- upstream-flags:")
        and "unanswered question" not in new[-1]["body"]
        and "no activity" not in new[-1]["body"],
    )
    q = new[-2]["body"]
    check(
        "a hostile note cannot forge a marker or ping anyone",
        q.split("\n")[0] == "<!-- gitlab-note: nsnam/ns-3-dev!2985 3999999999 -->"
        and "&lt;!-- upstream-sent" in q
        and "#⁠42" in q
        and "@⁠someone" in q,
    )
    _, extra, _ = mirrored_ids(cfg, new)
    check("a forged marker inside a quote is not read", extra == [])

    # token mode: notes from the REST API, the same comments
    fake2 = FakeGitHub()
    gl_calls.clear()
    cmd_read(
        cfg,
        GitLab("nsnam/ns-3-dev", "secret-token", gl_transport),
        GitHub("o/r", "t", fake2.transport),
        now,
    )
    check(
        "token read: notes from the REST API, same comments",
        any("/notes?" in c[1] for c in gl_calls)
        and len([c for c in fake2.comments[1] if c["body"].startswith("<!-- gitlab-note:")]) == 19,
    )
    check(
        "the token never reaches GitHub or the output",
        not any("secret-token" in json.dumps(c) for c in fake2.calls),
    )

    # dry run: nothing written to GitHub
    fake3 = FakeGitHub()
    gh3 = GitHub("o/r", None, fake3.transport, dry_run=True)
    out = cmd_read(cfg, gl, gh3, now)
    check(
        "dry run: every write held back",
        all(c[0] == "GET" for c in fake3.calls) and len(gh3.writes) > 15,
    )

    # outbound lint: what would link the wrong thing on GitLab
    proj = "nsnam/ns-3-dev"
    for text, want in (
        ("Crash in #1364.", "`#1364` links ns-3's issue 1364"),
        ("Fixed by !2985.", "write `nsnam/ns-3-dev!2985`"),
        (
            "RCAPA: swarm-routing/ns3-tools#240",
            "https://github.com/swarm-routing/ns3-tools/issues/240",
        ),
        ("see danieljoppi/AntHocNet#5", "`danieljoppi/AntHocNet#5` links a GitLab project"),
        (
            "ours: swarm-routing/ns3-tools!248",
            "https://github.com/swarm-routing/ns3-tools/pull/248",
        ),
    ):
        got = lint_outbound(text, proj)
        check(f"outbound lint flags: {text}", len(got) == 1 and want in got[0])
    for text in (
        "Same as nsnam/ns-3-dev#1364 and nsnam/ns-3-dev!2985.",
        "https://gitlab.com/nsnam/ns-3-dev/-/issues/1364#note_3755232337",
        "RCAPA: https://github.com/swarm-routing/ns3-tools/issues/240 (also /pull/248#issuecomment-1)",
        "In code: `ptr#1` and\n```\nissue #12 !3\n```\nC#1, x!2, # Heading, issue#3",
        "~~~cpp\nm[#0] = 1;\n~~~\n",
    ):
        check(f"outbound lint passes: {text[:30]}", lint_outbound(text, proj) == [])
    check(
        "a GitLab reference is its URL (issues, work items, merge requests) or the project form",
        parse_target("https://gitlab.com/nsnam/ns-3-dev/-/work_items/1364", proj) == ("#", 1364)
        and parse_target("https://gitlab.com/nsnam/ns-3-dev/-/merge_requests/2985/", proj)
        == ("!", 2985)
        and parse_target("nsnam/ns-3-dev#1364", proj) == ("#", 1364)
        and parse_target("https://gitlab.com/other/proj/-/issues/1", proj) is None
        and parse_target("#1364", proj) is None,
    )

    def refused(f, want):
        try:
            f()
        except SystemExit as e:
            return want in str(e)
        return False

    def iso(t):
        return t.strftime("%Y-%m-%dT%H:%M:%SZ")

    # send: text only from merged files, the gate policy, the guards, against a fake GitLab
    with tempfile.TemporaryDirectory() as root:

        def write(rel, text):
            path = os.path.join(root, rel)
            os.makedirs(os.path.dirname(path), exist_ok=True)
            with open(path, "w", encoding="utf-8") as f:
                f.write(text)

        def remove(rel):
            os.remove(os.path.join(root, rel))

        report = "# AODV livelocks in an ICMP storm\n\nSee nsnam/ns-3-dev#1364 and https://github.com/swarm-routing/ns3-tools/issues/238.\n"
        write("reports/NS3-001.md", report)
        write("reports/NS3-999.md", report)
        write("reports/NS3-003.md", "no title\n")
        write("replies/NS3-003/3692233174.md", "Done: see #12.\n")
        write("replies/NS3-404/1.md", "x")
        write("replies/NS3-003/5.md", "Thanks.\n\n---\n" + DISCLOSURE)
        lint = lint_files(cfg, root)
        check(
            "CI lint of reports/ and replies/: names, title, references, no hand-written footer",
            any("reports/NS3-999.md: not reports/" in p for p in lint)
            and any("reports/NS3-003.md: a report starts with" in p for p in lint)
            and any("replies/NS3-003/3692233174.md: `#12`" in p for p in lint)
            and any("replies/NS3-404/1.md: not replies/" in p for p in lint)
            and any("replies/NS3-003/5.md: the disclosure footer is added" in p for p in lint)
            and not any(p.startswith("reports/NS3-001.md") for p in lint),
        )
        for rel in ("reports/NS3-999.md", "reports/NS3-003.md", "replies/NS3-404/1.md"):
            remove(rel)
        remove("replies/NS3-003/5.md")
        write("replies/NS3-003/3692233174.md", "Attribution added to each commit.\n")

        gw = GitLab("nsnam/ns-3-dev", "tok", gl_transport)
        fk = FakeGitHub()
        g = GitHub("o/r", "t", fk.transport)
        cmd_read(cfg, gl, g, now)  # opens the tickets, mirrors the notes
        t1n = next(n for n, i in fk.issues.items() if "NS3-001" in i["title"])
        t3n = next(n for n, i in fk.issues.items() if "NS3-003" in i["title"])

        def posts():
            return [c for c in gl_calls if c[0] == "POST"]

        gl_calls.clear()
        lines, ready = cmd_plan(cfg, g, root, now)
        blocked = [c["body"] for c in fk.comments[t1n] if "upstream-blocked" in c["body"]]
        check(
            "a report without fresh P1 evidence is not scheduled, and the ticket says why",
            not ready
            and len(blocked) == 1
            and "no P1 verify evidence from the last 7 days" in blocked[0],
        )
        cmd_plan(cfg, g, root, now)
        check(
            "the same block is commented once",
            len([c for c in fk.comments[t1n] if "upstream-blocked" in c["body"]]) == 1,
        )
        cfg["item"][1]["verified"] = {
            "date": "2026-11-15",
            "master": "8ec9b4a1",
            "run": "https://github.com/x/y/actions/runs/1",
        }
        check("evidence is a valid register field", validate(cfg) == [])
        lines, ready = cmd_plan(cfg, g, root, now)
        sch = [c["body"] for c in fk.comments[t1n] if "upstream-scheduled" in c["body"]]
        check(
            "veto window: a new issue is scheduled 48 h out, its exact text on the ticket",
            not ready
            and len(sch) == 1
            and "due=2026-11-18T12:00:00Z" in sch[0].split("\n")[0]
            and "**Title:** AODV livelocks in an ICMP storm" in sch[0]
            and "````markdown\n" in sch[0]
            and DISCLOSURE in sch[0],
        )
        check(
            "the reply to a mirrored note is scheduled 24 h out",
            any(
                "upstream-scheduled: reply NS3-003/3692233174" in c["body"]
                and "due=2026-11-17T12:00:00Z" in c["body"]
                for c in fk.comments[t3n]
            ),
        )
        check(
            "nothing goes out inside the window",
            cmd_send(cfg, g, gw, root, now) == ["nothing ready"] and posts() == [],
        )
        cmd_plan(cfg, g, root, now)
        check(
            "an unchanged text is scheduled once",
            len([c for c in fk.comments[t1n] if "upstream-scheduled" in c["body"]]) == 1,
        )
        later = now + timedelta(hours=49)
        fk.issues[t1n]["labels"] = [{"name": HOLD}]
        lines, ready = cmd_plan(cfg, g, root, later)
        check("the hold label stops a due post", "publish NS3-001: held" in lines)
        fk.issues[t1n]["labels"] = []
        lines, ready = cmd_plan(cfg, g, root, later)
        check("after the window, with no hold: ready", ready and "publish NS3-001: ready" in lines)
        gl_calls.clear()
        fk.clock = iso(later)
        out = cmd_send(cfg, g, gw, root, later)
        created = [c for c in posts() if c[1].endswith("/issues")]
        noted = [c for c in posts() if c[1].endswith("/notes")]
        check(
            "send: the issue from the merged file with the footer, the reply as a note on its thread",
            len(created) == 1
            and created[0][3]["title"] == "AODV livelocks in an ICMP storm"
            and created[0][3]["description"].endswith("---\n" + DISCLOSURE)
            and len(noted) == 1
            and noted[0][1].endswith("/merge_requests/2985/notes")
            and noted[0][3]["body"] == "Attribution added to each commit.\n\n---\n" + DISCLOSURE
            and "publish NS3-001: sent: https://gitlab.com/nsnam/ns-3-dev/-/issues/1400" in out,
        )
        check(
            "the links come back as sent markers",
            fk.comments[t1n][-1]["body"].startswith(
                "<!-- upstream-sent: publish NS3-001 -> nsnam/ns-3-dev#1400 -->"
            )
            and fk.comments[t3n][-1]["body"].startswith(
                "<!-- upstream-sent: reply NS3-003/3692233174 -> nsnam/ns-3-dev!2985 note "
            ),
        )
        gl_calls.clear()
        check(
            "nothing is sent twice",
            cmd_send(cfg, g, gw, root, later) == ["nothing ready"] and posts() == [],
        )
        # a changed text restarts the window; a second reply to the same thread waits a day
        write(
            "replies/NS3-003/3691860433.md",
            "We keep them queued: the update interval is shorter.\n",
        )
        write("replies/NS3-003/3691862748.md", "Commit messages reworded.\n")
        lines, _ = cmd_plan(cfg, g, root, later)
        even_later = later + timedelta(hours=25)
        fk.clock = iso(even_later)
        gl_calls.clear()
        out = cmd_send(cfg, g, gw, root, even_later)
        check(
            "rate limit: one note per thread per 24 h",
            len(posts()) == 1 and any("waits (one note per thread per 24 h)" in x for x in out),
        )
        write("replies/NS3-003/1.md", "an unknown note\n")
        lines, _ = cmd_plan(cfg, g, root, even_later)
        check(
            "scope: a reply to a note not mirrored from the item's own threads is blocked",
            any(
                x.startswith("reply NS3-003/1: blocked: note 1 is not a mirrored note")
                for x in lines
            ),
        )
        check(
            "scope: the GitLab client refuses a note outside the allowed threads and other projects",
            refused(lambda: gw.create_note("!", 3011, "x"), "scope")
            and refused(
                lambda: GitLab("someone/else", "tok", gl_transport).create_issue("t", "x"), "scope"
            ),
        )
        check(
            "no token, no write",
            refused(
                lambda: GitLab("nsnam/ns-3-dev", None, gl_transport).create_issue("t", "x"),
                "GITLAB_TOKEN",
            ),
        )
        # a link-back that fails after GitLab took the post: the next run finds GitLab's
        # own evidence (the issue's title, the note's text) and does not post again
        write("replies/NS3-003/3691851680.md", "Logging the drop, as agreed.\n")
        fk.clock = iso(later + timedelta(hours=26))
        cmd_plan(cfg, g, root, later + timedelta(hours=26))
        real = fk.add
        fk.add = lambda *a, **k: (_ for _ in ()).throw(HttpError("POST", "x", 502, "bad gateway"))
        gl_calls.clear()
        try:
            cmd_send(cfg, g, gw, root, later + timedelta(hours=51))
        except HttpError:
            pass
        fk.add = real

        def ours():
            return [c for c in posts() if "Logging the drop" in json.dumps(c[3])]

        first = len(ours())
        out = cmd_send(cfg, g, gw, root, later + timedelta(hours=51))
        check(
            "a lost link-back is recovered from GitLab, not posted again",
            first == 1
            and len(ours()) == 1
            and any(x.startswith("reply NS3-003/3691851680: sent:") for x in out),
        )
        remove("replies/NS3-003/3691851680.md")
        # gate "auto": scheduled and sent in the same run; one new issue per day
        auto = dict(cfg, gate="auto")
        auto["item"] = [dict(i) for i in cfg["item"]] + [
            dict(
                cfg["item"][1],
                id="NS3-002",
                title="socket order",
                rcapa="swarm-routing/ns3-tools#239",
            )
        ]
        check("gate is a valid register field", validate(auto) == [])
        write("reports/NS3-002.md", "# Socket maps ordered by heap address\n\nBody.\n")
        soon = later + timedelta(hours=2)  # NS3-001 went out 2 h ago
        fk.clock = iso(soon)
        cmd_read(auto, gl, g, soon)
        lines, ready = cmd_plan(auto, g, root, soon)
        check("gate auto: due at once", ready and any("NS3-002: scheduled" in x for x in lines))
        gl_calls.clear()
        out = cmd_send(auto, g, gw, root, soon)
        check(
            "gate auto still keeps one new issue per day",
            not [c for c in posts() if c[1].endswith("/issues")]
            and any("publish NS3-002: waits (one new issue per day)" in x for x in out),
        )
        check(
            "an invalid gate is refused",
            any("gate is not one of" in e for e in validate(dict(cfg, gate="sometimes"))),
        )

    # P1 verify: the reproducer table, a run with a fake shell, the evidence plan reads
    good_r = {
        "modules": "core;network;internet;aodv",
        "files": {
            "scratch/c/c.cc": "https://raw.githubusercontent.com/swarm-routing/ns3-containers/"
            + "c" * 40
            + "/test/fixtures/manet-canary/manet-canary.cc"
        },
        "run": "timeout 120 ./ns3 run --no-build 'c --protocol=aodv'",
        "expect": "exit 124",
    }
    check("a reproducer table is valid", validate_reproduce("X", good_r) == [])
    for name, patch, want in (
        ("unpinned file", {"files": {"scratch/c.cc": "https://example.com/c.cc"}}, "pinned"),
        (
            "path out of the tree",
            {"files": {"scratch/../x": good_r["files"]["scratch/c/c.cc"]}},
            "pinned",
        ),
        ("bad expect", {"expect": "hangs"}, "expect"),
        ("bad modules", {"modules": "core network"}, "modules"),
    ):
        check(
            f"reproducer refused: {name}",
            any(want in e for e in validate_reproduce("X", {**good_r, **patch})),
        )

    class Proc:
        def __init__(self, rc, out=""):
            self.returncode, self.stdout, self.stderr = rc, out, ""

    vcfg = {
        "gitlab_project": proj,
        "item": [{"id": "NS3-001", "state": "draft", "title": "t", "reproduce": good_r}],
    }
    with tempfile.TemporaryDirectory() as ns3:
        ran = []

        def shell_for(run_rc, fail=None):
            def sh(cmd, timeout):
                ran.append(cmd)
                if cmd.startswith("git rev-parse"):
                    return Proc(0, "8ec9b4a190b52dadb5a3c35572dec687e1b75379\n")
                if fail and cmd.startswith(fail):
                    return Proc(2)
                return Proc(run_rc if cmd == good_r["run"] else 0)

            return sh

        with contextlib.redirect_stdout(io.StringIO()):
            got = cmd_verify(
                vcfg,
                "NS3-001",
                ns3,
                fetch=lambda u: b"// code\n",
                shell=shell_for(124),
                out_base=ns3,
            )
            miss = cmd_verify(
                vcfg, "NS3-001", ns3, fetch=lambda u: b"x", shell=shell_for(0), out_base=ns3
            )
            err = cmd_verify(
                vcfg,
                "NS3-001",
                ns3,
                fetch=lambda u: b"x",
                shell=shell_for(0, "./ns3 build"),
                out_base=ns3,
            )
        check(
            "P1: the reproducer runs in the tree, its expected exit is 'reproduces'",
            got["result"] == "reproduces"
            and got["master"].startswith("8ec9b4a1")
            and os.path.exists(os.path.join(ns3, "scratch/c/c.cc"))
            and os.path.exists(os.path.join(ns3, "results", "NS3-001.json"))
            and any("--enable-modules='core;network;internet;aodv'" in c for c in ran),
        )
        check("P1: another exit is 'not-reproduced'", miss["result"] == "not-reproduced")
        check(
            "paths are contained: no file or result outside its directory",
            refused(lambda: contained(ns3, "../x"), "outside")
            and refused(lambda: contained(ns3, "/etc/passwd"), "outside")
            and contained(ns3, "scratch/a.cc").startswith(os.path.realpath(ns3)),
        )
        check("P1: a failed build is 'error', the run is not attempted", err["result"] == "error")
    fv = FakeGitHub()
    gv = GitHub("o/r", "t", fv.transport)
    cmd_read(cfg, gl, gv, now)
    lines = cmd_verify_record(
        cfg, gv, [dict(got, item="NS3-001")], "https://github.com/o/r/actions/runs/9", now
    )
    t1v = next(n for n, i in fv.issues.items() if "NS3-001" in i["title"])
    ev = evidence({"bot": "github-actions[bot]"}, {"id": "NS3-001"}, fv.comments[t1v])
    check(
        "P1: the result is recorded on the ticket and becomes the evidence plan reads",
        lines == [f"NS3-001: recorded reproduces on #{t1v}"]
        and ev.get("date") == "2026-11-16"
        and ev.get("master", "").startswith("8ec9b4a1"),
    )
    with tempfile.TemporaryDirectory() as root:
        os.makedirs(os.path.join(root, "reports"))
        with open(os.path.join(root, "reports", "NS3-001.md"), "w", encoding="utf-8") as f:
            f.write("# A title\n\nA body.\n")
        c2 = dict(cfg, item=[dict(i) for i in cfg["item"]])
        c2["item"][1].pop("verified", None)
        lines, _ = cmd_plan(c2, gv, root, now)
        check(
            "P1 evidence from the ticket lets a report be scheduled",
            "publish NS3-001: scheduled for 2026-11-18T12:00:00Z" in lines,
        )

    # P6 lifecycle: a merged MR's commit in a release tag; workaround issues; the RCAPA ticket
    sha = "d" * 40
    gl_data[f"{api}/merge_requests/2985"] = dict(
        load("mr-2985.json"), state="merged", merge_commit_sha=sha
    )
    gl_data[f"{api}/repository/commits/{sha}/refs?type=tag&per_page=100"] = [
        {"type": "tag", "name": "ns-3.51"},
        {"type": "tag", "name": "ns-3.50"},
        {"type": "tag", "name": "ns-3-dev-snapshot"},
    ]
    lcfg = dict(cfg, item=[dict(cfg["item"][0], workarounds=["swarm-routing/ns3-containers#35"])])
    check("workarounds is a valid register field", validate(lcfg) == [])
    fl = FakeGitHub()
    gl6 = GitHub("o/r", "t", fl.transport)
    cmd_read(lcfg, gl, gl6, now)
    t3l = next(n for n, i in fl.issues.items() if "NS3-003" in i["title"])
    lines = cmd_lifecycle(lcfg, gl, gl6, now)
    check(
        "P6: the earliest release tag with the merge commit, said once on the ticket",
        lines[0] == "NS3-003: fixed in ns-3.50"
        and fl.comments[t3l][-1]["body"].startswith(
            f"<!-- upstream-released: ns-3.50 !2985 {sha} -->"
        )
        and "wait for the org bot's token: swarm-routing/ns3-containers#35" in lines[1],
    )

    class FakeOrg:
        def __init__(self):
            self.issues = {("swarm-routing/ns3-tools", 240): "open"}
            self.calls = []

        def transport(self, method, url, headers, body=None):
            self.calls.append((method, url, body))
            m = re.match(
                rf"{GITHUB_API}/repos/(swarm-routing/[^/]+)/issues(?:/(\d+))?(/comments)?$", url
            )
            repo, n = m.group(1), m.group(2)
            if method == "POST" and not n:
                n = 100 + len(self.issues)
                self.issues[(repo, n)] = "open"
                return 201, {"number": n}
            if method == "PATCH":
                self.issues[(repo, int(n))] = body["state"]
                return 200, {}
            if method == "POST":
                return 201, {"id": 1}
            return 200, {"state": self.issues[(repo, int(n))]}

    fo = FakeOrg()
    org = GitHub("o/r", "bot", fo.transport)
    lines = cmd_lifecycle(lcfg, gl, gl6, now, org)
    opened = [k for k in fo.issues if k[0] == "swarm-routing/ns3-containers"]
    check(
        "P6: one issue per workaround, recorded on the ticket; the RCAPA ticket stays open",
        len(opened) == 1
        and any(x.startswith("NS3-003: opened swarm-routing/ns3-containers#") for x in lines)
        and fo.issues[("swarm-routing/ns3-tools", 240)] == "open",
    )
    n_calls = len([c for c in fo.calls if c[0] != "GET"])
    cmd_lifecycle(lcfg, gl, gl6, now, org)
    check("P6: nothing opened twice", len([c for c in fo.calls if c[0] != "GET"]) == n_calls)
    fo.issues[opened[0]] = "closed"
    lines = cmd_lifecycle(lcfg, gl, gl6, now, org)
    check(
        "P6: every workaround issue closed, the RCAPA ticket is closed",
        fo.issues[("swarm-routing/ns3-tools", 240)] == "closed"
        and "NS3-003: closed swarm-routing/ns3-tools#240" in lines,
    )
    gl_data[f"{api}/merge_requests/2985"] = load("mr-2985.json")

    # the command line: validate and a dry read of the repository's own register run offline
    with tempfile.TemporaryDirectory() as d:
        path = os.path.join(d, "t.toml")
        with open(path, "w", encoding="utf-8") as f:
            f.write('gitlab_project = "nsnam/ns-3-dev"\n[[item]]\nid = "NS3-9"\n')
        check(
            "validate exits on an invalid register",
            refused(lambda: load_tracked(path, d), "id is not"),
        )

    for f in failures:
        print(f"FAIL: {f}")
    print(f"upstream.py self-test: {'ok' if not failures else f'{len(failures)} failed'}")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
