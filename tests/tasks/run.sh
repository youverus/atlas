#!/usr/bin/env bash
# Module gate: the three tools that read and stop tasks.
#
# The tools only read files and signal process groups, so the guard is about
# direction: they may reach the task model beside them, never the agent loop,
# the app or a provider. Reaching those would turn a query into orchestration.
set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/lib.sh"
cd "$TREE_ROOT"

guard_deps_allowed ./internal/tool/tasks pi-go/internal/task pi-go/internal/tool pi-go/internal/wire
guard_no_imports ./internal/tool/tasks pi-go/internal/app pi-go/internal/agent pi-go/internal/provider

run_package tasks ./internal/tool/tasks
