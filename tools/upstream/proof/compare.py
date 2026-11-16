#!/usr/bin/env python3
# Copyright (c) 2026 Daniel Joppi (https://github.com/swarm-routing)
# SPDX-License-Identifier: MIT OR GPL-2.0-only
"""compare.py PY.json GO.json: every command's exit status, stdout (byte for byte) and
writes (method, URL, JSON body) of the two tools; prints one line per command."""

import json
import sys

py, go = (json.load(open(p, encoding="utf-8")) for p in sys.argv[1:3])
bad = 0
for a, b in zip(py, go, strict=True):
    same = (
        a["argv"] == b["argv"]
        and a["code"] == b["code"]
        and a["stdout"] == b["stdout"]
        and (a["writes"] or []) == (b["writes"] or [])
    )
    bad += not same
    print(
        f"{'identical' if same else 'DIFFERS'}: {' '.join(a['argv'])}: exit {a['code']}, "
        f"{len(a['stdout'].splitlines())} line(s), {len(a['writes'] or [])} write(s)"
    )
    if not same:
        for k in ("code", "stdout", "writes"):
            if a[k] != b[k] and (a[k] or b[k]):
                print(
                    f"  {k}:\n    py: {json.dumps(a[k])[:2000]}\n    go: {json.dumps(b[k])[:2000]}"
                )
print(f"{len(py)} command(s), {bad} different")
sys.exit(1 if bad else 0)
