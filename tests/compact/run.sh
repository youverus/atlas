#!/usr/bin/env bash
# Module gate: context compaction (cut point, summary prompts, file lists).
#
# Compaction reads a branch and returns a checkpoint; it never writes a session,
# so the gate pins both the layering (session, wire, and the provider interface
# only: no agent, no tools, no concrete adapter) and that nothing in the package
# can touch the disk.
set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/lib.sh"
cd "$TREE_ROOT"

guard_deps_allowed ./internal/compact \
	pi-go/internal/provider \
	pi-go/internal/session \
	pi-go/internal/wire
guard_no_imports ./internal/compact \
	pi-go/internal/agent \
	pi-go/internal/tool \
	pi-go/internal/app \
	pi-go/internal/rpc \
	pi-go/internal/provider/openai \
	pi-go/internal/provider/anthropic \
	pi-go/internal/sse

step "compact: no file system access"
imports="$(go list -f '{{join .Imports "\n"}}' ./internal/compact)"
for forbidden in os io/ioutil path/filepath; do
	if grep -qx "$forbidden" <<<"$imports"; then
		fail "./internal/compact imports $forbidden; persisting is the caller's job"
		exit 1
	fi
done
ok "./internal/compact reads and writes nothing"

run_package compact ./internal/compact/...
