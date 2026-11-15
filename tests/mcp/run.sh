#!/usr/bin/env bash
# Module gate: the MCP client.
#
# Three things beyond the usual four checks.
#
# The Windows vet, because process_unix.go and process_windows.go are a pair of
# build tags: the one this machine never compiles is the one that stops working
# first, and nothing else in the tree would notice.
#
# The dependency closure, pinned to tool, wire and sse. This is the gate that
# keeps the "write it ourselves" decision honest: the official Go SDK is third
# party, and the day it becomes reachable from here is the day the client is no
# longer testable in an empty module cache.
#
# The unreachable-layer list, because a client that grew a dependency on config or
# app would make the protocol layer know about sessions and files.
set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/lib.sh"
cd "$TREE_ROOT"

guard_deps_allowed ./internal/mcp pi-go/internal/tool pi-go/internal/wire pi-go/internal/sse
guard_no_imports ./internal/mcp pi-go/internal/provider pi-go/internal/agent pi-go/internal/config pi-go/internal/app pi-go/internal/session

step "mcp: go vet for windows"
GOOS=windows go vet ./internal/mcp
ok "GOOS=windows go vet"

run_package mcp ./internal/mcp
