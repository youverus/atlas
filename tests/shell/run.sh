#!/usr/bin/env bash
# Module gate: the bash tool.
#
# The interesting parts of shell are process lifecycle, so this gate runs the
# package under -race as well: a data race in the output accumulator would only
# show up while a command is still streaming.
set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/lib.sh"
cd "$TREE_ROOT"

guard_deps_allowed ./internal/tool/shell pi-go/internal/tool pi-go/internal/wire
guard_no_imports ./internal/tool/shell pi-go/internal/provider pi-go/internal/agent

run_package shell ./internal/tool/shell
