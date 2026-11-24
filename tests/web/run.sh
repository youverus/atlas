#!/usr/bin/env bash
# Module gate: the two web tools (deepseek_web_search, deepseek_web_fetch).
#
# The same layering as fs and shell: a tool package may reach for tool and wire
# and must stay on the standard library. The reason is sharper here than
# elsewhere — this package talks to a billed endpoint, so an accidental import
# of a provider would mean the tool list and the billing surface stop being the
# same list.
set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/lib.sh"
cd "$TREE_ROOT"

guard_deps_allowed ./internal/tool/web pi-go/internal/tool pi-go/internal/wire
guard_no_imports ./internal/tool/web pi-go/internal/provider pi-go/internal/agent pi-go/internal/config

run_package web ./internal/tool/web
