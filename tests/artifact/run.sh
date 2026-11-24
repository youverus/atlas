#!/usr/bin/env bash
# Module gate: internal/artifact (versioned, session-scoped artifact storage).
set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/lib.sh"
cd "$TREE_ROOT"

guard_stdlib_only ./internal/artifact
run_package artifact ./internal/artifact
