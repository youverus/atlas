#!/usr/bin/env bash
# Module gate: internal/provider/responses.
#
# The third adapter speaks the Responses wire API, and it is the first one that
# has to keep something the server issued — an encrypted reasoning item — to stay
# stateless. The gate pins the same layering as the other two adapters and prints
# the recorded streams it replays: a deleted fixture silently removes a code path.
set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/lib.sh"
cd "$TREE_ROOT"

shopt -s nullglob
streams=(testdata/responses/*.sse)
if ((${#streams[@]} == 0)); then
	fail "testdata/responses holds no streams"
	exit 1
fi
requests=(testdata/responses/*.json)
if ((${#requests[@]} == 0)); then
	fail "testdata/responses holds no request fixtures"
	exit 1
fi

step "responses: fixtures"
for fixture in "${streams[@]}"; do
	printf '  %-26s %3s data lines\n' "${fixture##*/}" "$(grep -c '^data:' "$fixture" || true)"
done
for fixture in "${requests[@]}"; do
	printf '  %-26s %3s lines\n' "${fixture##*/}" "$(wc -l <"$fixture" | tr -d ' ')"
done

guard_deps_allowed ./internal/provider/responses \
	pi-go/internal/provider \
	pi-go/internal/sse \
	pi-go/internal/wire
guard_no_imports ./internal/provider/responses \
	pi-go/internal/agent \
	pi-go/internal/tool \
	pi-go/internal/session
run_package responses ./internal/provider/responses/...
