#!/usr/bin/env bash
# Module gate: the palette loader.
#
# The theme package stays stdlib only even though the interface it serves does
# not. It hands out hex strings; turning those into styles is the TUI's job.
# That split is what keeps "what color is accent, and was every token named"
# answerable in a test with no terminal attached, and what lets tests/deps stay
# a rule instead of a list of exceptions.
set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/lib.sh"
cd "$TREE_ROOT"

guard_stdlib_only ./internal/theme

run_package theme ./internal/theme
