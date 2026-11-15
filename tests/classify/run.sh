#!/usr/bin/env bash
# Module gate: the local permission classifier.
#
# internal/classify is the one package allowed to both ask a model a question and
# decide whether a tool call runs, so it sits between two layers that must not
# otherwise touch: permission (which owns the ladder) and provider (which owns the
# wire). It may see internal/tool and internal/wire through them, and nothing else.
#
# The forbidden list is the layering: config is where the model is chosen, and
# cmd/atlas is what builds this value from it. A classifier that read config would
# be a second place that knows how a model is selected, and a classifier that read
# agent or session could be argued with by the conversation it is judging.
set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/lib.sh"
cd "$TREE_ROOT"

guard_deps_allowed ./internal/classify pi-go/internal/permission pi-go/internal/provider pi-go/internal/tool pi-go/internal/wire
guard_no_imports ./internal/classify pi-go/internal/agent pi-go/internal/app pi-go/internal/config pi-go/internal/sandbox pi-go/internal/session

run_package classify ./internal/classify
