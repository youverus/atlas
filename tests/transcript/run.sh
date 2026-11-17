#!/usr/bin/env bash
# Module gate: the transcript fold.
#
# This package is the model half of every interface: it turns the event stream
# into blocks and owns the spans, folding and reveal window. It imports the
# standard library and wire, and nothing else — the renderers keep their own
# dependencies, so "what counts as one tool call" stays answerable without a
# terminal, a cell grid or a window.
#
# Two frontends is why it exists. A second interface that re-implemented this
# fold would drift on the first event shape it disagreed about, and the drift
# would show up as two products.
set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/lib.sh"
cd "$TREE_ROOT"

guard_deps_allowed ./internal/transcript pi-go/internal/wire
guard_no_imports ./internal/transcript pi-go/internal/provider pi-go/internal/tool pi-go/internal/agent pi-go/internal/app pi-go/internal/session pi-go/internal/config

run_package transcript ./internal/transcript
