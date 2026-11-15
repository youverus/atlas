#!/usr/bin/env bash
# Module gate: the orchestration layer (session + agent + model switching +
# side conversations).
#
# The app is the layer every front end drives, so its dependency set is the
# interesting part: it may reach the kernel, the compaction logic, the provider
# boundary, the session store, and the tool interface, but no concrete wire
# adapter. Choosing
# Claude-versus-GPT is the caller's job, and a front end must be able to swap
# adapters without touching the orchestration.
set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/lib.sh"
cd "$TREE_ROOT"

guard_deps_allowed ./internal/app pi-go/internal/agent pi-go/internal/compact pi-go/internal/provider pi-go/internal/session pi-go/internal/tool pi-go/internal/wire
guard_no_imports ./internal/app pi-go/internal/provider/openai pi-go/internal/sse

run_package app ./internal/app
