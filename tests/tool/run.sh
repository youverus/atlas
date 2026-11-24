#!/usr/bin/env bash
# Module gate: internal/tool and the echo tool.
set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/lib.sh"
cd "$TREE_ROOT"

run_package tool ./internal/tool ./internal/tool/echo ./internal/tool/artifact
