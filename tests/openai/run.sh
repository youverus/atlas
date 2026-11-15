#!/usr/bin/env bash
# Module gate: internal/provider/openai.
#
# The adapter is the first package that talks to a real endpoint, so the gate
# also pins the layering (no reach into agent or tool) and prints the recorded
# streams it replays: a deleted fixture silently removes a code path.
set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/lib.sh"
cd "$TREE_ROOT"

shopt -s nullglob
streams=(testdata/sse/openai_*.sse)
if ((${#streams[@]} == 0)); then
	fail "testdata/sse holds no openai streams"
	exit 1
fi
requests=(testdata/openai/*.json)
if ((${#requests[@]} == 0)); then
	fail "testdata/openai holds no request fixtures"
	exit 1
fi

step "openai: fixtures"
for fixture in "${streams[@]}"; do
	printf '  %-26s %3s data lines\n' "${fixture##*/}" "$(grep -c '^data:' "$fixture" || true)"
done
for fixture in "${requests[@]}"; do
	printf '  %-26s %3s lines\n' "${fixture##*/}" "$(wc -l <"$fixture" | tr -d ' ')"
done

guard_no_imports ./internal/provider/openai pi-go/internal/agent pi-go/internal/tool pi-go/internal/session
run_package openai ./internal/provider/openai/...
