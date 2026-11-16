#!/usr/bin/env python3
# Copyright (c) 2026 Daniel Joppi (https://github.com/swarm-routing)
# SPDX-License-Identifier: MIT OR GPL-2.0-only
"""Runs upstream.py's self-test, so CI's `coverage run` measures it."""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import upstream  # noqa: E402

sys.exit(upstream.self_test())
