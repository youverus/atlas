#!/usr/bin/env bash
# Module gate: a turn's receipt, shared by both front ends.
#
# internal/cost is a stdlib-only leaf that sees internal/wire and nothing else.
# It must stay that way: it is imported by the terminal, by the loopback
# transport's composition root, and by the desktop shell, so a single dependency
# above wire would drag the kernel into all three.
set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/lib.sh"
cd "$TREE_ROOT"

guard_deps_allowed ./internal/cost pi-go/internal/wire
guard_no_imports ./internal/cost pi-go/internal/agent pi-go/internal/app pi-go/internal/config pi-go/internal/tool pi-go/internal/tui pi-go/internal/httpapi

run_package cost ./internal/cost
