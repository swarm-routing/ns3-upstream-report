#!/usr/bin/env bash
# Copyright (c) 2026 Daniel Joppi (https://github.com/swarm-routing)
# SPDX-License-Identifier: MIT OR GPL-2.0-only
#
# The Go port's byte-identical proof (swarm-routing/.github docs/adr/0002-pipelines-are-go.md):
# the Python and the Go tool run the commands of commands.json against the same recorded
# world, and compare.py diffs every exit status, stdout and write. Nothing is sent: a GET
# is served from the world, a write is recorded. Run from the repository root:
#   tools/upstream/proof/proof.sh [WORLD.json ...]
# With no world it uses the self-test's fixtures (fixtures_world.py); `py_replay.py
# record WORLD.json` records a live one (GitHub with GH_TOKEN, GitLab tokenless).
set -euo pipefail
here=tools/upstream/proof
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
worlds=("$@")
if [ "${#worlds[@]}" -eq 0 ]; then
  python3 -I "$here/fixtures_world.py" "$tmp/world-fixtures.json"
  worlds=("$tmp/world-fixtures.json")
fi
status=0
for w in "${worlds[@]}"; do
  w=$(realpath "$w")
  echo "== $(basename "$w")"
  python3 -I "$here/py_replay.py" replay "$w" "$tmp/py.json"
  UPSTREAM_PROOF_WORLD="$w" UPSTREAM_PROOF_OUT="$tmp/go.json" go test -count=1 -run '^TestProof$' ./cmd/upstream/ >/dev/null
  python3 -I "$here/compare.py" "$tmp/py.json" "$tmp/go.json" || status=1
done
exit "$status"
