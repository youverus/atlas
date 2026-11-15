#!/usr/bin/env bash
# Module gate: internal/provider/google.
#
# The fourth adapter speaks the Gemini generative language API, so the gate pins
# the same layering as the others (no reach into agent, tool, or session) and
# that the adapter only depends on the shared provider plumbing it is meant to
# use. It also prints the recorded streams it replays: a deleted fixture
# silently removes a code path.
set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/lib.sh"
cd "$TREE_ROOT"

shopt -s nullglob
streams=(testdata/google/*.sse)
if ((${#streams[@]} == 0)); then
	fail "testdata/google holds no streams"
	exit 1
fi

step "google: fixtures"
for fixture in "${streams[@]}"; do
	printf '  %-26s %3s data lines\n' "${fixture##*/}" "$(grep -c '^data:' "$fixture" || true)"
done

guard_deps_allowed ./internal/provider/google \
	pi-go/internal/provider \
	pi-go/internal/sse \
	pi-go/internal/wire
guard_no_imports ./internal/provider/google \
	pi-go/internal/agent \
	pi-go/internal/tool \
	pi-go/internal/session
run_package google ./internal/provider/google/...
