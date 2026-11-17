#!/usr/bin/env bash
# Module gate: internal/assembly, the composition root.
#
# This package is what makes the CLI and the window one product instead of two:
# both call assembly.New and get the same kernel, so a resumed conversation in a
# window sends the same request the command line would have. The invariant the
# gate protects is that it stays importable and self-contained — it builds and
# tests on its own, holds no third-party import (tests/deps proves that), and is
# the only package allowed to name a concrete adapter (tests/cli proves that).
#
# It also runs with -race because the kernel is shared with a window: two
# goroutines read the gate, the session directory and the MCP state while a turn
# is writing them.
set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/lib.sh"
cd "$TREE_ROOT"

step "assembly: the package builds on its own"
go build ./internal/assembly
ok "go build"

run_package assembly ./internal/assembly

step "assembly: nothing in it links a front end"
frontends="$(go list -f '{{.ImportPath}} {{join .Imports " "}}' ./internal/assembly |
	awk '{for (i = 2; i <= NF; i++) if ($i == "pi-go/cmd/atlas" || $i ~ /^pi-go\/desktop/) { print $i; break }}')"
if [[ -n $frontends ]]; then
	fail "internal/assembly imports a front end: $frontends"
	fail "the dependency points the other way: a front end calls assembly.New"
	exit 1
fi
ok "it depends on no front end"
