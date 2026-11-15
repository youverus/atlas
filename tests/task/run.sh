#!/usr/bin/env bash
# Module gate: the task model.
#
# A task is a line plus a process group, so both halves need watching. The
# records are appended from the goroutine that waits for the command while the
# caller keeps reading them, hence -race. The liveness probe has a !unix file
# that only a Windows build can type-check, hence the GOOS=windows vet: that
# file is the one place where "is this process alive" answers without saying
# anything about the world.
set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/lib.sh"
cd "$TREE_ROOT"

guard_deps_allowed ./internal/task pi-go/internal/tool pi-go/internal/wire
guard_no_imports ./internal/task pi-go/internal/app pi-go/internal/agent pi-go/internal/provider

step "task: go vet (windows)"
GOOS=windows go vet ./internal/task
ok "go vet (windows)"

run_package task ./internal/task
