#!/usr/bin/env python3
# Copyright (c) 2026 Daniel Joppi (https://github.com/swarm-routing)
# SPDX-License-Identifier: MIT OR GPL-2.0-only
"""The Go port's byte-identical proof, Python side (ADR 0002 of swarm-routing/.github).

  py_replay.py record WORLD.json            GETs live (GitHub with GH_TOKEN, GitLab tokenless) while
                                            running the commands below; writes are never sent
  py_replay.py replay WORLD.json OUT.json   the same commands against WORLD only

Every command runs in-process with upstream.py's own code; a GET is served from the
world (404 when absent), a write is recorded and answered with a canned body, never sent.
OUT holds, per command, its stdout, its exit status and its writes. cmd/upstream's
TestProof writes the same file for the Go tool; compare.py diffs the two.
"""

import contextlib
import io
import json
import os
import sys
import urllib.error
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.dirname(HERE))
import upstream  # noqa: E402

# the commands of the proof, run from the repository root with these arguments
COMMANDS = json.load(open(os.path.join(HERE, "commands.json"), encoding="utf-8"))
CANNED = {"number": 0, "id": 0, "iid": 0, "html_url": "(replay)", "web_url": "(replay)"}


def main():
    mode, world_path = sys.argv[1], sys.argv[2]
    world = {} if mode == "record" else json.load(open(world_path, encoding="utf-8"))
    writes = []

    def transport(method, url, headers, body=None):
        if method != "GET":
            writes.append({"method": method, "url": url, "body": body})
            return 201, dict(CANNED), {}
        if mode == "record" and url not in world:
            h = {k: v for k, v in headers.items()}
            if url.startswith(upstream.GITHUB_API) and os.environ.get("GH_TOKEN"):
                h["Authorization"] = f"Bearer {os.environ['GH_TOKEN']}"
            h.pop("PRIVATE-TOKEN", None)
            req = urllib.request.Request(url, headers=h)
            try:
                with urllib.request.urlopen(req, timeout=60) as r:
                    raw = r.read()
                    keep = {
                        k.lower(): v
                        for k, v in r.headers.items()
                        if k.lower() == "x-next-page-cursor"
                    }
                    world[url] = [r.status, json.loads(raw) if raw.strip() else None, keep]
            except urllib.error.HTTPError as e:
                world[url] = [e.code, e.read().decode(errors="replace")[:300], {}]
        if url not in world:
            raise upstream.HttpError(method, url, 404, "not in the world")
        status, body_, hdr = world[url]
        if status >= 300:
            raise upstream.HttpError(method, url, status, body_)
        return status, json.loads(json.dumps(body_)), dict(hdr)

    upstream.GitHub.__init__.__defaults__ = (None, transport, False)
    upstream.GitLab.__init__.__defaults__ = (None, transport, upstream.GITLAB)
    results = []
    for argv in COMMANDS:
        writes.clear()
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            try:
                code = upstream.main(argv)
            except SystemExit as e:
                code = e.code if isinstance(e.code, int) else 1
                if not isinstance(e.code, int):
                    print(e.code)
            except upstream.HttpError as e:
                code = 1
                print(f"error: {e}")
        results.append(
            {"argv": argv, "code": code, "stdout": out.getvalue(), "writes": list(writes)}
        )
    if mode == "record":
        with open(world_path, "w", encoding="utf-8") as f:
            json.dump(world, f, indent=1, sort_keys=True, ensure_ascii=False)
    else:
        with open(sys.argv[3], "w", encoding="utf-8") as f:
            json.dump(results, f, indent=1, ensure_ascii=False)


if __name__ == "__main__":
    os.environ.pop("GITHUB_TOKEN", None)
    os.environ.pop("GITLAB_TOKEN", None)
    os.environ.pop("GITHUB_OUTPUT", None)
    os.environ.pop("GITHUB_STEP_SUMMARY", None)
    os.environ.pop("BOT_TOKEN", None)
    main()
