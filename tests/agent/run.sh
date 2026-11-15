#!/usr/bin/env bash
# Module gate: internal/agent.
#
# This is the only place that knows the turn loop, so the loop tests are the
# contract for every later consumer (rpc, httpapi, app).
set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/lib.sh"
cd "$TREE_ROOT"

run_package agent ./internal/agent
