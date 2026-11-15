#!/usr/bin/env bash
# Module gate: the file tools (read, write, edit, ls, find, grep).
#
# fs is the first tool package with real logic, so this gate also pins the
# layering: file tools must not reach for a provider or the turn loop, and the
# package must stay on the standard library.
set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/lib.sh"
cd "$TREE_ROOT"

guard_deps_allowed ./internal/tool/fs pi-go/internal/tool pi-go/internal/wire
guard_no_imports ./internal/tool/fs pi-go/internal/provider pi-go/internal/agent

run_package fs ./internal/tool/fs
