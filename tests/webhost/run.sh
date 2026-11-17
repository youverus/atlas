#!/usr/bin/env bash
# Module gate: the window's adapter over a built kernel.
#
# This package is the only place that holds both httpapi and assembly. That is
# its whole reason to exist, so the rule here is about its *direct* imports
# rather than its dependency closure: the closure is the whole kernel (assembly
# reaches everything), so asserting it would say nothing at all.
#
# What is asserted instead is the shape of the adapter: it may name assembly,
# httpapi, session, project, transcript and config, and nothing else of ours. A
# new direct import of provider, tool or tui would mean this package had started
# doing work the kernel already does, in a place the kernel's gates cannot see.
# The last three are the exceptions that are worth naming: a project is a list of
# folders kept beside the sessions rather than inside the kernel (session files
# already carry their own folder, so the kernel has nothing to add), transcript
# is the kernel's fold and the replay of a past conversation has to go through it
# rather than through a second opinion held here, and config is the leaf that
# answers where that list lives.
set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/lib.sh"
cd "$TREE_ROOT"

step "webhost: direct imports"
allowed=(pi-go/internal/assembly pi-go/internal/config pi-go/internal/httpapi pi-go/internal/project pi-go/internal/session pi-go/internal/transcript)
imports="$(go list -f '{{join .Imports "\n"}}' ./internal/webhost)"
while IFS= read -r dep; do
	[[ -z $dep ]] && continue
	case "$dep" in
	pi-go | pi-go/*) ;;
	*) continue ;;
	esac
	permitted=0
	for prefix in "${allowed[@]}"; do
		case "$dep" in
		"$prefix" | "$prefix"/*) permitted=1 ;;
		esac
	done
	if ((permitted == 0)); then
		fail "webhost imports $dep; allowed: ${allowed[*]}"
		exit 1
	fi
done <<<"$imports"
ok "direct imports"

step "webhost: no third-party packages"
if go list -deps ./internal/webhost | grep -qE '^[a-z0-9.-]+\.[a-z]{2,}/'; then
	fail "webhost depends on a third-party package"
	exit 1
fi
ok "standard library only"

run_package webhost ./internal/webhost
