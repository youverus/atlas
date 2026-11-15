#!/usr/bin/env bash
# Module gate: the read-only git tools.
#
# The read-only promise is an argv whitelist, so this gate's job is the layering:
# the package may reach the tool contract and nothing else. The whitelist itself
# is asserted by the tests, with a real git in a temporary repository.
set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/lib.sh"
cd "$TREE_ROOT"

guard_deps_allowed ./internal/tool/git pi-go/internal/tool pi-go/internal/wire
guard_no_imports ./internal/tool/git pi-go/internal/provider pi-go/internal/agent pi-go/internal/config

run_package git ./internal/tool/git