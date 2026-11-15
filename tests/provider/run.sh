#!/usr/bin/env bash
# Module gate: internal/provider and the faux provider.
#
# faux is part of this gate because it is the offline baseline every later
# feature is built against; a broken faux hides regressions in the loop.
set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/lib.sh"
cd "$TREE_ROOT"

guard_no_imports ./internal/provider pi-go/internal/agent pi-go/internal/tool
guard_no_imports ./internal/provider/faux pi-go/internal/agent pi-go/internal/tool
# The conformance suite has to stay usable by an adapter without knowing about
# one: it is the shared layer, not a fourth adapter in disguise.
guard_deps_allowed ./internal/provider/conformance \
	pi-go/internal/provider \
	pi-go/internal/wire
guard_no_imports ./internal/provider/conformance \
	pi-go/internal/provider/openai \
	pi-go/internal/provider/anthropic \
	pi-go/internal/provider/responses
run_package provider ./internal/provider ./internal/provider/faux ./internal/provider/conformance
