# Copyright (c) 2026 Daniel Joppi (https://github.com/swarm-routing)
# SPDX-License-Identifier: MIT OR GPL-2.0-only
"""The proof's directory: every file of the proof lives in it under a fixed name (no path
comes from an argument). proof.sh gives each run a private TMPDIR (mktemp -d) and exports
it to every stage, so the Python scripts, the Go test and the shell agree on it."""

import os
import stat
import tempfile

WORK = os.path.join(tempfile.gettempdir(), "upstream-proof")


def workdir():
    """WORK, created private (0700); an existing one is refused unless it is a real
    directory of this user that nobody else can write."""
    os.makedirs(WORK, mode=0o700, exist_ok=True)
    st = os.lstat(WORK)
    if not stat.S_ISDIR(st.st_mode) or st.st_uid != os.getuid() or st.st_mode & 0o022:
        raise SystemExit(f"refused: {WORK} is not a private directory of this user")
    return WORK
