#!/usr/bin/env python3
# Copyright (c) 2026 Daniel Joppi (https://github.com/swarm-routing)
# SPDX-License-Identifier: MIT OR GPL-2.0-only
"""The bridge between the org's mirror tickets and ns-3's GitLab (nsnam/ns-3-dev).

Usage:
  upstream.py validate [--tracked FILE]
  upstream.py read     [--tracked FILE] [--dry-run] [--now ISO-8601]
  upstream.py gate     {publish,reply} [--tracked FILE]       (reads $GITHUB_EVENT_PATH)
  upstream.py publish  --issue N --sha256 HEX [--tracked FILE] [--dry-run]
  upstream.py reply    --issue N --comment ID --sha256 HEX [--tracked FILE] [--dry-run]
  upstream.py --self-test

`tracked.toml` is the register: one `[[item]]` per upstream report, keyed by
its NS3-NNN id (swarm-routing/ns3-tools docs/ns3-bugs.md), with its RCAPA
ticket, its state (`draft`, `published`, `closed`), its GitLab issue and
merge requests, and the mirror ticket here (`mirror`, once known). An item
only enters it by a reviewed pull request to `main`, and every command below
acts only on the tickets it names, so the file is also the allowlist of what
can be sent upstream.

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
          mentioned. With GITLAB_TOKEN it reads notes
          from the REST API; without it from the public `discussions.json`
          pages, so it runs, and its tests run, without any secret.
gate      The first job of the publish and reply workflows, without any
          secret: checks the `/publish` or `/upstream own|assisted` comment
          (author association, a tracked mirror ticket, the fields, not sent
          before), writes the exact text to the run summary for the person who
          approves the deployment, and its sha256 to $GITHUB_OUTPUT.
publish   In the `gitlab-write` environment, after the approval: reads the
          ticket again, refuses if the text's sha256 differs from the gate's,
          creates the GitLab issue (or the note on the target issue or merge
          request) and posts the link back with an `upstream-sent` marker.
reply     The same for one `/upstream` comment: its text, verbatim, as a
          GitLab note on the item's `reply_to` (or the reference the command
          names), then the link back.

Disclosure (ns-3's AI policy, nsnam/ns-3-dev!2985): a report whose "Who wrote
the report text" field says an AI assistant drafted it, and a reply sent with
`/upstream assisted`, end with DISCLOSURE. `/upstream own` sends the text as
written; a bare `/upstream` is refused, so the choice is always made
(docs/adr/0003-disclosure-on-relayed-text.md).

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
    "reply_to",
    "mirror",
    "draft",
)
ID = re.compile(r"NS3-\d{3}")
GH_REF = re.compile(r"swarm-routing/[A-Za-z0-9_.-]+#\d+")
GL_REF = re.compile(r"(?P<project>[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)?(?P<kind>[#!])(?P<iid>\d+)")
STALL_DAYS = 14
MAX_NEW_NOTES = 50  # comments one run adds per item at most; the rest come next run
# the last upstream note asks something of us: a question mark or a request
QUESTION = re.compile(r"\?|\bplease\b", re.I)
TRUSTED_ASSOCIATIONS = ("OWNER", "MEMBER", "COLLABORATOR")
DISCLOSURE = (
    "Assisted-by: an AI assistant (Claude Code) drafted this text; I reviewed it before sending."
)
AUTHORS = {
    "own": "I wrote it",
    "assisted": "An AI assistant drafted it; I reviewed it",
}
STATUS_BEGIN, STATUS_END = "<!-- upstream-status:begin -->", "<!-- upstream-status:end -->"
FOOTER = "\n\n---\n_Posted by the [upstream bridge](https://github.com/swarm-routing/ns3-upstream-report) read workflow._"


class HttpError(Exception):
    def __init__(self, method, url, status, message):
        super().__init__(f"{method} {url}: HTTP {status}: {message}")
        self.status = status


def http(method, url, headers, body=None):
    """One HTTP request: (status, parsed JSON or None). Headers are never in the error."""
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(url, data=data, method=method, headers=headers)
    if data is not None:
        req.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(req, timeout=60) as r:
            raw = r.read()
            return r.status, (json.loads(raw) if raw.strip() else None)
    except urllib.error.HTTPError as e:
        msg = e.read().decode(errors="replace")[:300]
        raise HttpError(method, url, e.code, msg) from None


# --- the register --------------------------------------------------------------


def load_tracked(path):
    with open(path, "rb") as f:
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
        if "reply_to" in it:
            m = GL_REF.fullmatch(str(it["reply_to"]))
            if not m or m["project"] or not ref_in_item(it, m["kind"], int(m["iid"])):
                errors.append(
                    f"{where}: reply_to is not one of the item's #issue or !merge request"
                )
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


def default_reply_to(it, extra=()):
    if it.get("reply_to"):
        m = GL_REF.fullmatch(it["reply_to"])
        return m["kind"], int(m["iid"])
    ours = [(k, i) for k, i, mirrored in refs(it, extra) if mirrored]
    mrs = [r for r in ours if r[0] == "!"]
    return (mrs or ours or [None])[0]


# --- GitLab --------------------------------------------------------------------


class GitLab:
    def __init__(self, project, token=None, transport=http, base=GITLAB):
        self.project, self.token, self.transport, self.base = project, token, transport, base
        self.api = f"{base}/api/v4/projects/{urllib.parse.quote(project, safe='')}"

    def _get(self, url):
        headers = {"Accept": "application/json", "User-Agent": "ns3-upstream-report"}
        if self.token:
            headers["PRIVATE-TOKEN"] = self.token
        return self.transport("GET", url, headers)[1]

    def _post(self, url, body):
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
            url = f"{self.base}/{self.project}/-/{self.path(kind)}/{iid}/discussions.json"
            for d in self._get(url):
                raw += d.get("notes", [])
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


def parse_form(body):
    """An issue form's body: {heading: value} for each `### heading` section."""
    out, cur, lines = {}, None, []
    for line in (body or "").split(STATUS_BEGIN)[0].splitlines():
        m = re.match(r"^### (.+?)\s*$", line)
        if m:
            if cur is not None:
                out[cur] = "\n".join(lines).strip()
            cur, lines = m.group(1), []
        elif cur is not None:
            lines.append(line)
    if cur is not None:
        out[cur] = "\n".join(lines).strip()
    return {k: ("" if v == "_No response_" else v) for k, v in out.items()}


def ticket_body(cfg, it):
    """A mirror ticket's body, the same sections as the upstream-report form."""
    proj = cfg["gitlab_project"]
    if it.get("issue"):
        target = f"{proj}#{it['issue']}"
    elif it.get("merge_requests"):
        target = f"{proj}!{it['merge_requests'][0]}"
    else:
        target = "new issue"
    if it["state"] == "draft":
        report = (
            "Draft, not published. The text is drafted in "
            + ", ".join(it.get("draft", []) or ["the RCAPA ticket"])
            + ". Paste the final text here, check the fields above, then comment `/publish`."
        )
    else:
        report = f"Published before this bridge existed: see {web_url(proj, *target_ref(target))}."
    return (
        f"{id_marker(it['id'])}\n"
        f"### NS3 id\n\n{it['id']}\n\n"
        f"### RCAPA ticket\n\n{it.get('rcapa', '_No response_')}\n\n"
        f"### GitLab target\n\n{target}\n\n"
        f"### GitLab title\n\n{it['title']}\n\n"
        f"### Report text\n\n{report}\n\n"
        f"### Who wrote the report text\n\n{AUTHORS['assisted']}\n\n"
        f"{STATUS_BEGIN}\n{STATUS_END}\n"
    )


def target_ref(target):
    m = GL_REF.fullmatch(target.split()[-1]) if target != "new issue" else None
    return (m["kind"], int(m["iid"])) if m else ("#", 0)


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
        label = ref_label(kind, iid)
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
        f"competing merge request !{m}" + ("" if m in it.get("competing", []) else " (not tracked)")
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
    if it["state"] != "draft":
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


# --- publish and reply ---------------------------------------------------------


def item_for_issue(gh, cfg, number, issue=None):
    """The tracked item whose mirror ticket is issue `number`, or None."""
    issue = issue or gh.issue(number)
    issues = [issue]
    for it in cfg.get("item", []):
        t = find_ticket(gh, cfg, it, issues) if not it.get("mirror") else None
        if it.get("mirror") == number or (t and t["number"] == number):
            return it
    return None


def sent_markers(cfg, comments):
    bot = cfg.get("bot", "github-actions[bot]")
    return [
        first_line_marker(c.get("body"), "upstream-sent")
        for c in comments
        if (c.get("user") or {}).get("login") == bot
        and first_line_marker(c.get("body"), "upstream-sent")
    ]


def publish_payload(cfg, it, issue):
    """(kind, iid, title, text) the ticket asks to send, or raise SystemExit with why not."""
    f = parse_form(issue.get("body"))
    proj = cfg["gitlab_project"]
    if f.get("NS3 id", "").strip() != it["id"]:
        raise SystemExit(f"the ticket's NS3 id is not {it['id']}")
    target = f.get("GitLab target", "").strip()
    if target == "new issue":
        kind, iid = "#", 0
    else:
        m = GL_REF.fullmatch(target)
        if not m or (m["project"] or proj) != proj:
            raise SystemExit(f"GitLab target is neither `new issue` nor {proj}#N or {proj}!N")
        kind, iid = m["kind"], int(m["iid"])
    title = f.get("GitLab title", "").strip()
    if kind == "#" and iid == 0 and not title:
        raise SystemExit("a new GitLab issue needs a GitLab title")
    text = f.get("Report text", "").strip()
    if not text or text.startswith("Draft, not published."):
        raise SystemExit("the Report text is empty or still the draft placeholder")
    who = f.get("Who wrote the report text", "").strip()
    if who not in AUTHORS.values():
        raise SystemExit(
            "`Who wrote the report text` is not one of: " + " | ".join(AUTHORS.values())
        )
    if who == AUTHORS["assisted"]:
        text += "\n\n---\n" + DISCLOSURE
    return kind, iid, title, text


def reply_payload(cfg, it, comment, extra=()):
    """(kind, iid, text) one `/upstream` comment asks to send."""
    body = (comment.get("body") or "").replace("\r\n", "\n")
    first, _, rest = body.partition("\n")
    words = first.split()
    if not words or words[0] != "/upstream":
        raise SystemExit("the comment does not start with /upstream")
    if len(words) < 2 or words[1] not in AUTHORS:
        raise SystemExit(
            "say who wrote the text: `/upstream own` (you did) or `/upstream assisted` "
            "(an AI assistant drafted it; the disclosure line is added)"
        )
    if len(words) > 3:
        raise SystemExit("`/upstream own|assisted [#N|!N]`, then the text on the next lines")
    if len(words) == 3:
        m = GL_REF.fullmatch(words[2])
        if (
            not m
            or m["project"]
            or not (
                ref_in_item(it, m["kind"], int(m["iid"])) or (m["kind"], int(m["iid"])) in extra
            )
        ):
            raise SystemExit(f"{words[2]} is not one of {it['id']}'s GitLab references")
        kind, iid = m["kind"], int(m["iid"])
    else:
        target = default_reply_to(it, extra)
        if target is None:
            raise SystemExit(f"{it['id']} has nothing on GitLab to reply to yet")
        kind, iid = target
    text = rest.strip()
    if not text:
        raise SystemExit("the text to send goes on the next lines, after `/upstream …`")
    if words[1] == "assisted":
        text += "\n\n---\n" + DISCLOSURE
    return kind, iid, text


def digest(*parts):
    return hashlib.sha256(json.dumps(parts, ensure_ascii=False).encode()).hexdigest()


def cmd_gate(cfg, gh, which, event):
    """Check the comment; returns (outputs, summary). Raises SystemExit to refuse."""
    if event.get("action") != "created" or event.get("issue", {}).get("pull_request"):
        raise SystemExit("not a new comment on an issue")
    c = event["comment"]
    if c.get("author_association") not in TRUSTED_ASSOCIATIONS:
        raise SystemExit("only the repository's owner, members and collaborators can send upstream")
    n = event["issue"]["number"]
    issue = gh.issue(n)
    it = item_for_issue(gh, cfg, n, issue)
    if it is None:
        raise SystemExit(f"#{n} is not the mirror ticket of an item in tracked.toml on main")
    comments = gh.comments(n)
    sent = sent_markers(cfg, comments)
    proj = cfg["gitlab_project"]
    if which == "publish":
        if (c.get("body") or "").strip() != "/publish":
            raise SystemExit("the comment is not exactly /publish")
        if any(s.startswith("publish ") for s in sent):
            raise SystemExit(f"{it['id']} was already published from this ticket")
        kind, iid, title, text = publish_payload(cfg, it, issue)
        h = digest("publish", n, kind, iid, title, text)
        where = "a new issue" if iid == 0 else f"a note on {proj}{kind}{iid}"
        summary = f"## /publish {it['id']} as {where}\n\n" + (
            f"**Title:** {title}\n\n" if iid == 0 else ""
        )
        summary += f"sha256 `{h}`\n\n````markdown\n{text}\n````\n"
        return {"issue": str(n), "sha256": h}, summary
    if any(s.startswith(f"comment {c['id']} ") for s in sent):
        raise SystemExit(f"comment {c['id']} was already sent")
    _, extra, _ = mirrored_ids(cfg, comments)
    kind, iid, text = reply_payload(cfg, it, c, extra)
    h = digest("reply", n, c["id"], kind, iid, text)
    summary = f"## /upstream {it['id']}: a note on {proj}{kind}{iid}\n\nsha256 `{h}`\n\n````markdown\n{text}\n````\n"
    return {"issue": str(n), "comment": str(c["id"]), "sha256": h}, summary


def cmd_publish(cfg, gh, gl, n, sha):
    issue = gh.issue(n)
    it = item_for_issue(gh, cfg, n, issue)
    if it is None:
        raise SystemExit(f"#{n} is not a tracked mirror ticket")
    if any(s.startswith("publish ") for s in sent_markers(cfg, gh.comments(n))):
        raise SystemExit(f"{it['id']} was already published")
    kind, iid, title, text = publish_payload(cfg, it, issue)
    if digest("publish", n, kind, iid, title, text) != sha:
        gh.add_comment(
            n,
            "The ticket changed after `/publish` asked for approval, so nothing was sent. "
            "Comment `/publish` again to send the current text." + FOOTER,
        )
        raise SystemExit("the text changed since the gate: nothing sent")
    proj = cfg["gitlab_project"]
    if iid == 0:
        new_iid, url = gl.create_issue(title, text)
        marker = f"publish -> {proj}#{new_iid}"
        msg = f"Published as [{proj}#{new_iid}]({url}). Add `issue = {new_iid}` to {it['id']} in tracked.toml."
    else:
        nid = gl.create_note(kind, iid, text)
        url = f"{web_url(proj, kind, iid)}#note_{nid}"
        marker = f"publish -> {proj}{kind}{iid} note {nid}"
        msg = f"Published as [a note on {proj}{kind}{iid}]({url})."
    print(f"{it['id']}: sent: {url}")
    gh.add_comment(n, f"<!-- upstream-sent: {marker} -->\n{msg}{FOOTER}")
    return url


def cmd_reply(cfg, gh, gl, n, cid, sha):
    issue = gh.issue(n)
    it = item_for_issue(gh, cfg, n, issue)
    if it is None:
        raise SystemExit(f"#{n} is not a tracked mirror ticket")
    c = gh.comment(cid)
    if not (c.get("issue_url") or "").endswith(f"/issues/{n}"):
        raise SystemExit(f"comment {cid} is not on #{n}")
    comments = gh.comments(n)
    if any(s.startswith(f"comment {cid} ") for s in sent_markers(cfg, comments)):
        raise SystemExit(f"comment {cid} was already sent")
    _, extra, _ = mirrored_ids(cfg, comments)
    kind, iid, text = reply_payload(cfg, it, c, extra)
    if digest("reply", n, int(cid), kind, iid, text) != sha:
        gh.add_comment(
            n,
            f"Comment {cid} changed after it asked for approval, so nothing was sent. "
            "Post the text again in a new `/upstream` comment." + FOOTER,
        )
        raise SystemExit("the comment changed since the gate: nothing sent")
    proj = cfg["gitlab_project"]
    nid = gl.create_note(kind, iid, text)
    url = f"{web_url(proj, kind, iid)}#note_{nid}"
    print(f"{it['id']}: sent: {url}")
    gh.add_comment(
        n,
        f"<!-- upstream-sent: comment {cid} -> {proj}{kind}{iid} note {nid} -->\n"
        f"Sent [comment]({c.get('html_url', '')}) as [a note on {proj}{kind}{iid}]({url}).{FOOTER}",
    )
    return url


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
    for name in ("validate", "read", "gate", "publish", "reply"):
        s = sub.add_parser(name)
        s.add_argument("--tracked", default=TRACKED)
        if name == "gate":
            s.add_argument("which", choices=("publish", "reply"))
        if name in ("read", "publish", "reply"):
            s.add_argument("--dry-run", action="store_true")
        if name == "read":
            s.add_argument("--now")
        if name in ("publish", "reply"):
            s.add_argument("--issue", type=int, required=True)
            s.add_argument("--sha256", required=True)
        if name == "reply":
            s.add_argument("--comment", type=int, required=True)
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
    gh, gl = clients(cfg, getattr(a, "dry_run", False))
    if a.cmd == "read":
        now = parse_time(a.now) if a.now else datetime.now(timezone.utc)
        for line in cmd_read(cfg, gl, gh, now):
            print(line)
        if a.dry_run:
            print(f"dry run: {len(gh.writes)} GitHub write(s) not sent")
        return 0
    if a.cmd == "gate":
        with open(os.environ["GITHUB_EVENT_PATH"], encoding="utf-8") as f:
            event = json.load(f)
        try:
            write_output(*cmd_gate(cfg, gh, a.which, event))
        except SystemExit as e:
            write_output({}, f"## Not sent\n\n{e}\n")
            raise
        return 0
    if a.dry_run:
        gl.transport = lambda *x: (_ for _ in ()).throw(SystemExit("dry run: GitLab not written"))
    if a.cmd == "publish":
        cmd_publish(cfg, gh, gl, a.issue, a.sha256)
    else:
        cmd_reply(cfg, gh, gl, a.issue, a.comment, a.sha256)
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
reply_to = "!2985"
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
        ("reply_to elsewhere", {"reply_to": "!3011"}, "reply_to is not one of"),
        ("published with nothing", {"issue": None}, None),
    ):
        it = dict(cfg["item"][0], **{k: v for k, v in patch.items() if v is not None})
        if name == "published with nothing":
            it = {k: v for k, v in it.items() if k not in ("issue", "merge_requests", "reply_to")}
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
        f"{GITLAB}/nsnam/ns-3-dev/-/merge_requests/3011/discussions.json": [],
    }
    gl_calls = []

    def gl_transport(method, url, headers, body=None):
        gl_calls.append((method, url, "PRIVATE-TOKEN" in headers, body))
        if method == "POST":
            if "PRIVATE-TOKEN" not in headers:
                raise HttpError(method, url, 401, "401 Unauthorized")
            if url.endswith("/issues"):
                return 201, {"iid": 1400, "web_url": f"{GITLAB}/nsnam/ns-3-dev/-/issues/1400"}
            return 201, {"id": 9000000 + len(gl_calls)}
        if url.endswith("/notes?sort=asc&order_by=created_at&per_page=100&page=1"):
            if "PRIVATE-TOKEN" not in headers:
                raise HttpError(method, url, 401, "401 Unauthorized")
            disc = url.replace(api, f"{GITLAB}/nsnam/ns-3-dev/-").split("/notes?")[0]
            notes = [n for d in gl_data[disc + "/discussions.json"] for n in d["notes"]]
            return 200, [dict(n, id=int(n["id"]) if n["id"].isdigit() else n["id"]) for n in notes]
        if url not in gl_data:
            raise HttpError(method, url, 404, "404 Not Found")
        return 200, json.loads(json.dumps(gl_data[url]))

    class FakeGitHub:
        def __init__(self):
            self.issues, self.comments, self.next = {}, {}, 1
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
        for f in ("issue-1364-discussions.json", "mr-2985-discussions.json")
        for d in load(f)
        for n in d["notes"]
        if not n["system"]
    ]
    mirrored = [c for c in fake.comments[t3["number"]] if c["body"].startswith("<!-- gitlab-note:")]
    check("tokenless read: no token sent to GitLab", not any(c[2] for c in gl_calls))
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
        len(mirrored) == len(user_notes) == 15
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
        "stalls flagged: no activity, need_rebase, no reviewer, unanswered question, competing MR",
        len(flags) == 1
        and "no activity for 60 days" in fl
        and "need_rebase (!2985)" in fl
        and "no reviewer (!2985)" in fl
        and "unanswered question (!2985, tomhenderson 2026-08-17)" in fl
        and "(#1364" not in fl
        and "competing merge request !3011" in fl
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
        and len([c for c in fake2.comments[1] if c["body"].startswith("<!-- gitlab-note:")]) == 16,
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

    # gate, publish and reply against the fake GitLab
    t1n = t1["number"]
    issue = fake.issues[t1n]

    def event(n, body, association="OWNER", login="danieljoppi"):
        c = fake.add(n, body, login, association)
        return {"action": "created", "issue": {"number": n}, "comment": c}

    def refused(f, want):
        try:
            f()
        except SystemExit as e:
            return want in str(e)
        return False

    check(
        "publish refused while the text is the draft placeholder",
        refused(lambda: cmd_gate(cfg, gh, "publish", event(t1n, "/publish")), "draft placeholder"),
    )
    issue["body"] = issue["body"].replace(
        issue["body"].split("### Report text\n\n")[1].split("\n\n###")[0], "AODV livelocks when …"
    )
    check(
        "publish refused for an outsider",
        refused(lambda: cmd_gate(cfg, gh, "publish", event(t1n, "/publish", "NONE")), "only the"),
    )
    stranger = fake.transport(
        "POST",
        f"{GITHUB_API}/repos/o/r/issues",
        {},
        {"title": "x", "body": id_marker("NS3-001"), "labels": []},
    )[1]["number"]
    fake.issues[stranger]["user"] = {"login": "mallory"}
    check(
        "a marker in someone else's issue does not make it a mirror ticket",
        refused(
            lambda: cmd_gate(cfg, gh, "publish", event(stranger, "/publish")),
            "not the mirror ticket",
        ),
    )
    outputs, summary = cmd_gate(cfg, gh, "publish", event(t1n, "/publish"))
    check(
        "publish gate: the text with its disclosure in the summary, sha256 out",
        outputs["issue"] == str(t1n)
        and len(outputs["sha256"]) == 64
        and "AODV livelocks when …\n\n---\n" + DISCLOSURE in summary
        and "**Title:** AODV livelock" in summary,
    )
    issue["body"] = issue["body"].replace("AODV livelocks when …", "AODV livelocks when … (edited)")
    gl_calls.clear()
    gl_w = GitLab("nsnam/ns-3-dev", "tok", gl_transport)
    check(
        "publish refused when the text changed after the gate",
        refused(lambda: cmd_publish(cfg, gh, gl_w, t1n, outputs["sha256"]), "changed")
        and not any(c[0] == "POST" for c in gl_calls),
    )
    outputs, _ = cmd_gate(cfg, gh, "publish", event(t1n, "/publish"))
    with contextlib.redirect_stdout(io.StringIO()):
        url = cmd_publish(cfg, gh, gl_w, t1n, outputs["sha256"])
    posted = [c for c in gl_calls if c[0] == "POST"]
    check(
        "publish creates the GitLab issue with the text, links back",
        url.endswith("/issues/1400")
        and len(posted) == 1
        and posted[0][3]["title"] == "AODV livelock"
        and posted[0][3]["description"].endswith(DISCLOSURE)
        and fake.comments[t1n][-1]["body"].startswith(
            "<!-- upstream-sent: publish -> nsnam/ns-3-dev#1400 -->"
        ),
    )
    check(
        "publish refused a second time",
        refused(lambda: cmd_gate(cfg, gh, "publish", event(t1n, "/publish")), "already published"),
    )
    check(
        "the published issue becomes a reference of the ticket",
        default_reply_to(cfg["item"][1], mirrored_ids(cfg, fake.comments[t1n])[1]) == ("#", 1400),
    )
    check(
        "publish without a token is refused before anything is sent",
        refused(
            lambda: GitLab("nsnam/ns-3-dev", None, gl_transport).create_issue("t", "x"),
            "GITLAB_TOKEN",
        ),
    )

    t3n = t3["number"]
    check(
        "a bare /upstream is refused: the author must be stated",
        refused(
            lambda: cmd_gate(cfg, gh, "reply", event(t3n, "/upstream\nThanks!")), "/upstream own"
        ),
    )
    check(
        "a reference outside the item is refused",
        refused(
            lambda: cmd_gate(cfg, gh, "reply", event(t3n, "/upstream own !1\nThanks!")),
            "not one of",
        ),
    )
    check(
        "an empty reply is refused",
        refused(lambda: cmd_gate(cfg, gh, "reply", event(t3n, "/upstream own\n\n")), "next lines"),
    )
    ev = event(t3n, "/upstream assisted\nRebased on master; attribution added to each commit.")
    outputs, summary = cmd_gate(cfg, gh, "reply", ev)
    check(
        "reply gate: reply_to target, disclosure added",
        "a note on nsnam/ns-3-dev!2985" in summary
        and summary.count(DISCLOSURE) == 1
        and outputs["comment"] == str(ev["comment"]["id"]),
    )
    gl_calls.clear()
    with contextlib.redirect_stdout(io.StringIO()):
        url = cmd_reply(cfg, gh, gl_w, t3n, ev["comment"]["id"], outputs["sha256"])
    posted = [c for c in gl_calls if c[0] == "POST"]
    check(
        "reply posts the text verbatim plus disclosure as a note, links back",
        len(posted) == 1
        and posted[0][1].endswith("/merge_requests/2985/notes")
        and posted[0][3]["body"]
        == "Rebased on master; attribution added to each commit.\n\n---\n" + DISCLOSURE
        and fake.comments[t3n][-1]["body"].startswith(
            f"<!-- upstream-sent: comment {ev['comment']['id']} -> nsnam/ns-3-dev!2985 note "
        ),
    )
    check(
        "a comment is sent once",
        refused(
            lambda: cmd_reply(cfg, gh, gl_w, t3n, ev["comment"]["id"], outputs["sha256"]),
            "already sent",
        ),
    )
    ev2 = event(t3n, "/upstream own #1364\nThanks, Eduardo.")
    outputs2, _ = cmd_gate(cfg, gh, "reply", ev2)
    fake.comments[t3n][-1]["body"] = "/upstream own #1364\nThanks, Eduardo. Also: something else"
    check(
        "a reply edited after the gate is not sent",
        refused(
            lambda: cmd_reply(cfg, gh, gl_w, t3n, ev2["comment"]["id"], outputs2["sha256"]),
            "changed",
        ),
    )
    sent = first_line_marker(
        next(c["body"] for c in fake.comments[t3n] if "upstream-sent: comment" in c["body"]),
        "upstream-sent",
    )
    disc.append(
        {
            "notes": [
                {
                    "id": sent.split()[-1],
                    "system": False,
                    "body": "Rebased on master; attribution added to each commit.",
                    "created_at": "2026-11-16T11:00:00.000Z",
                    "author": {"username": "danieljoppi", "name": "Daniel"},
                }
            ]
        }
    )
    out = cmd_read(cfg, gl, gh, now)
    check(
        "a note we sent is not mirrored back",
        not any("mirrored" in line for line in out if line.startswith("NS3-003")),
    )
    check(
        "the comment events are matched on the first line only",
        refused(
            lambda: cmd_gate(cfg, gh, "publish", event(t1n, "please /publish")), "exactly /publish"
        ),
    )

    # the command line: validate and a dry read of the repository's own register run offline
    with tempfile.TemporaryDirectory() as d:
        path = os.path.join(d, "t.toml")
        with open(path, "w", encoding="utf-8") as f:
            f.write('gitlab_project = "nsnam/ns-3-dev"\n[[item]]\nid = "NS3-9"\n')
        check(
            "validate exits on an invalid register",
            refused(lambda: load_tracked(path), "id is not"),
        )

    for f in failures:
        print(f"FAIL: {f}")
    print(f"upstream.py self-test: {'ok' if not failures else f'{len(failures)} failed'}")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
