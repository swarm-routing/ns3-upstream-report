#!/usr/bin/env python3
# Copyright (c) 2026 Daniel Joppi (https://github.com/swarm-routing)
# SPDX-License-Identifier: MIT OR GPL-2.0-only
"""fixtures_world.py OUT.json: the proof's world from tools/upstream/fixtures (the
self-test's recorded GitLab pages of nsnam/ns-3-dev, 2026-11-16) and an empty GitHub."""

import json
import os
import sys

FX = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "fixtures")
GL = "https://gitlab.com"
API = f"{GL}/api/v4/projects/nsnam%2Fns-3-dev"


def load(name):
    with open(os.path.join(FX, name), encoding="utf-8") as f:
        return json.load(f)


world = {
    f"{API}/issues/1364": [200, load("issue-1364.json"), {}],
    f"{API}/merge_requests/2985": [200, load("mr-2985.json"), {}],
    f"{API}/merge_requests/2985/approvals": [200, load("mr-2985-approvals.json"), {}],
    f"{API}/merge_requests/3011": [200, load("mr-3011.json"), {}],
    f"{GL}/nsnam/ns-3-dev/-/issues/1364/discussions.json": [
        200,
        load("issue-1364-discussions.json"),
        {},
    ],
    f"{GL}/nsnam/ns-3-dev/-/merge_requests/2985/discussions.json": [
        200,
        load("mr-2985-discussions.json"),
        {"x-next-page-cursor": "c2"},
    ],
    f"{GL}/nsnam/ns-3-dev/-/merge_requests/2985/discussions.json?cursor=c2": [
        200,
        load("mr-2985-discussions-2.json"),
        {},
    ],
    f"{GL}/nsnam/ns-3-dev/-/merge_requests/3011/discussions.json": [200, [], {}],
    "https://api.github.com/repos/swarm-routing/ns3-upstream-report/issues?state=all&per_page=100&page=1": [
        200,
        [],
        {},
    ],
}
with open(sys.argv[1], "w", encoding="utf-8") as f:
    json.dump(world, f, indent=1, ensure_ascii=False)
