#!/usr/bin/env bash
# Module gate: the user-space permission ladder and permission.json store.
#
# internal/permission is a stdlib-only leaf that sees only internal/tool (and
# internal/wire through it). It must never import internal/agent,
# internal/config, internal/sandbox, or any provider.
set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/lib.sh"
cd "$TREE_ROOT"

guard_deps_allowed ./internal/permission pi-go/internal/tool pi-go/internal/wire
guard_no_imports ./internal/permission pi-go/internal/agent pi-go/internal/config pi-go/internal/sandbox pi-go/internal/provider

run_package permission ./internal/permission
