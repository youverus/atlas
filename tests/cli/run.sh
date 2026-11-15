#!/usr/bin/env bash
# Module gate: the atlas CLI, which is the composition root.
#
# The rule this gate exists for is the inverse of the others: cmd/atlas is the
# one package allowed to name a concrete wire adapter, and it must stay the only
# one. tests/app and tests/rpc prove the orchestration and the transport never
# reach for an adapter; this checks nobody else does either, so "add vendor X"
# is a change in one file instead of a search.
#
# The rest is the ordinary binary gate: the package builds, its tests run, and
# the demo path stays offline (every test either uses faux or an httptest
# listener on loopback).
set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/lib.sh"
cd "$TREE_ROOT"

step "cli: only cmd/atlas names a concrete adapter"
adapters="$(go list -f '{{.ImportPath}} {{join .Imports " "}}' ./... |
	awk -v o="pi-go/internal/provider/openai" -v a="pi-go/internal/provider/anthropic" \
		-v r="pi-go/internal/provider/responses" -v g="pi-go/internal/provider/google" \
		'{for (i = 2; i <= NF; i++) if ($i == o || $i == a || $i == r || $i == g) { print $1; break }}' |
	sort -u)"
if [[ $adapters != "pi-go/cmd/atlas" ]]; then
	fail "these packages import a concrete adapter: ${adapters//$'\n'/, }"
	fail "choosing a vendor belongs in pi-go/cmd/atlas alone"
	exit 1
fi
ok "cmd/atlas is the only package that imports an adapter"

step "cli: the binary builds"
go build -o /dev/null ./cmd/atlas
ok "go build"

run_package cli ./cmd/atlas
