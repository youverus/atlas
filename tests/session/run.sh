#!/usr/bin/env bash
# Module gate: the session store (JSONL v3 tree, resume, branch, fork, listing).
#
# The session package is the on-disk contract with pi, so it must stay on the
# standard library plus wire: no provider, no tools, no turn loop.
set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/lib.sh"
cd "$TREE_ROOT"

guard_deps_allowed ./internal/session pi-go/internal/wire
guard_no_imports ./internal/session pi-go/internal/provider pi-go/internal/tool pi-go/internal/agent

run_package session ./internal/session
