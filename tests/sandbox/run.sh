#!/usr/bin/env bash
# Module gate: the sandbox policy compiler and its executor.
#
# Two things are checked beyond the package's own tests. The first is that the
# package stays on the standard library: it is the one place that decides what a
# command may touch, so it must not be reachable from anything that a config or a
# provider brings in. The second is that the kernel tests actually ran: a machine
# without an executor skips them, and a gate that passed by skipping would report
# a boundary nobody verified.
set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/lib.sh"
cd "$TREE_ROOT"

guard_stdlib_only ./internal/sandbox
guard_no_imports ./internal/sandbox pi-go/internal/tool pi-go/internal/agent pi-go/internal/config pi-go/internal/provider

step "sandbox: cross-platform vet"
GOOS=linux go vet ./internal/sandbox
GOOS=windows go vet ./internal/sandbox
ok "darwin, linux and windows executors vet cleanly"

step "sandbox: kernel boundary"
# The Seatbelt tests skip themselves when /usr/bin/sandbox-exec is missing, which
# is correct for a checkout on another platform and wrong to hide on this one.
output="$(go test -count=1 -run 'TestSeatbelt' -v ./internal/sandbox 2>&1)" || {
	printf '%s\n' "$output" >&2
	fail "kernel boundary tests failed"
	exit 1
}
if grep -q -- '--- SKIP' <<<"$output"; then
	printf '%s\n' "$output" >&2
	fail "the kernel boundary tests were skipped, so nothing verified the profile"
	exit 1
fi
ok "profile applied and refused as expected"

run_package sandbox ./internal/sandbox
