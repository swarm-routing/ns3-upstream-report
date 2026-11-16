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
# record` records a live one (GitHub with GH_TOKEN, GitLab tokenless) as
# $TMPDIR/upstream-proof/world.json. Each run of this script works in a private
# directory of its own (mktemp -d, exported as TMPDIR to every stage), removed on exit.
set -euo pipefail
here=tools/upstream/proof
TMPDIR=$(mktemp -d)
export TMPDIR
trap 'rm -rf -- "$TMPDIR"' EXIT
work="$TMPDIR/upstream-proof"
mkdir -m 700 "$work"
worlds=("$@")
status=0
run() {
  echo "== $1"
  python3 -I "$here/py_replay.py" replay
  UPSTREAM_PROOF_WORLD="$work/world.json" UPSTREAM_PROOF_OUT="$work/go.json" \
    go test -count=1 -run '^TestProof$' ./cmd/upstream/ >/dev/null
  python3 -I "$here/compare.py" || status=1
}
if [[ "${#worlds[@]}" -eq 0 ]]; then
  python3 -I "$here/fixtures_world.py"
  run fixtures
fi
for w in "${worlds[@]}"; do
  cp -- "$w" "$work/world.json"
  run "$(basename "$w")"
done
exit "$status"
