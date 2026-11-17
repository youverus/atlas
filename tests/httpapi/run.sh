#!/usr/bin/env bash
# Module gate: the loopback HTTP + SSE service.
#
# This package is the data plane a windowed frontend reads. It is a kernel
# package, so it holds to the same rule as the rest: the standard library plus
# wire, no third-party HTTP framework, no provider and no tool.
#
# The dependency list is the interesting part. It may reach wire (the event
# contract it republishes) and nothing above it: once httpapi can reach app or
# agent, the transport starts deciding what a turn is, and the "frontends drive
# the same loop" property that app/rpc already share is gone.
set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/lib.sh"
cd "$TREE_ROOT"

guard_deps_allowed ./internal/httpapi pi-go/internal/wire
guard_no_imports ./internal/httpapi pi-go/internal/provider pi-go/internal/tool pi-go/internal/agent pi-go/internal/app pi-go/internal/session pi-go/internal/config pi-go/internal/tui

run_package httpapi ./internal/httpapi
