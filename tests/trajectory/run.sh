#!/usr/bin/env bash
# Module gate: the trajectory fold.
#
# This package turns a recorded session into the rows a debugger walks. It reads
# entries and answers in wire types, and it must not depend on the view it feeds:
# a fold that imported the window, the HTTP API or the agent would be a second
# reader of live state, and the whole point is that it reads what was written.
set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/lib.sh"
cd "$TREE_ROOT"

guard_deps_allowed ./internal/trajectory pi-go/internal/session pi-go/internal/wire
guard_no_imports ./internal/trajectory pi-go/internal/agent pi-go/internal/app pi-go/internal/httpapi pi-go/internal/provider pi-go/internal/transcript pi-go/internal/tool

run_package trajectory ./internal/trajectory
