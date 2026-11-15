#!/usr/bin/env bash
# Module gate: internal/sse.
#
# The framing layer must stay dependency free, and every recorded stream in
# testdata/sse is replayed by the package tests. The fixture inventory below is
# printed so a deleted or truncated sample is visible before the tests run.
set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/lib.sh"
cd "$TREE_ROOT"

shopt -s nullglob
fixtures=(testdata/sse/*.sse)
if ((${#fixtures[@]} == 0)); then
	fail "testdata/sse holds no fixtures"
	exit 1
fi

step "sse: fixtures"
for fixture in "${fixtures[@]}"; do
	printf '  %-26s %3s data lines\n' "${fixture##*/}" "$(grep -c '^data:' "$fixture" || true)"
done

guard_stdlib_only ./internal/sse
run_package sse ./internal/sse/...
