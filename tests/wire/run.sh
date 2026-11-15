#!/usr/bin/env bash
# Module gate: internal/wire.
#
# The JSON contract is the only package every other layer imports, so it must
# stay free of dependencies and free of formatting drift.
set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/lib.sh"
cd "$TREE_ROOT"

guard_stdlib_only ./internal/wire
run_package wire ./internal/wire/...
