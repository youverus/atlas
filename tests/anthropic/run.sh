#!/usr/bin/env bash
# Module gate: internal/provider/anthropic.
#
# The second adapter speaks a different wire API, so the gate pins the same
# layering as openai (no reach into agent, tool, or session) and that the
# adapter only depends on the shared provider plumbing it is meant to use. It
# also prints the recorded streams and request fixtures it replays: a deleted
# fixture silently removes a code path.
set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/lib.sh"
cd "$TREE_ROOT"

shopt -s nullglob
streams=(testdata/anthropic/*.sse)
if ((${#streams[@]} == 0)); then
	fail "testdata/anthropic holds no streams"
	exit 1
fi
requests=(testdata/anthropic/*.json)
if ((${#requests[@]} == 0)); then
	fail "testdata/anthropic holds no request fixtures"
	exit 1
fi

step "anthropic: fixtures"
for fixture in "${streams[@]}"; do
	printf '  %-26s %3s data lines\n' "${fixture##*/}" "$(grep -c '^data:' "$fixture" || true)"
done
for fixture in "${requests[@]}"; do
	printf '  %-26s %3s lines\n' "${fixture##*/}" "$(wc -l <"$fixture" | tr -d ' ')"
done

guard_deps_allowed ./internal/provider/anthropic \
	pi-go/internal/provider \
	pi-go/internal/sse \
	pi-go/internal/wire
guard_no_imports ./internal/provider/anthropic \
	pi-go/internal/agent \
	pi-go/internal/tool \
	pi-go/internal/session
run_package anthropic ./internal/provider/anthropic/...
