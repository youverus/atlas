#!/usr/bin/env bash
# Module gate: the JSONL RPC transport.
#
# The transport is the boundary a headless client talks to, so its dependency
# set matters as much as the app's: it may reach the orchestration layer, the
# session store, and the wire types, but no concrete provider adapter. A client
# asks for a model by name and the app decides what that means.
set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/lib.sh"
cd "$TREE_ROOT"

guard_deps_allowed ./internal/rpc pi-go/internal/app pi-go/internal/agent pi-go/internal/compact pi-go/internal/provider pi-go/internal/session pi-go/internal/tool pi-go/internal/wire
guard_no_imports ./internal/rpc pi-go/internal/provider/openai pi-go/internal/sse

run_package rpc ./internal/rpc
