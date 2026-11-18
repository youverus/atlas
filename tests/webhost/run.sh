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
# The last four are the exceptions that are worth naming: a project is a list of
# folders kept beside the sessions rather than inside the kernel (session files
# already carry their own folder, so the kernel has nothing to add), transcript
# and trajectory are the kernel's two folds — a reading of the conversation and a
# reading of what was recorded — so either view has to go through its own fold
# rather than through a second opinion held here, config is the leaf that answers
# where that list lives, and permission is the leaf that holds the four postures
# and the words for them — the window's switch has to reach the same engine the
# terminal's /permission reaches, and the settings sheet has to draw the same four
# names, so naming it here is the point rather than a leak.
#
# wire is the last exception and the smallest: it is the vocabulary httpapi's own
# contract is written in (Turn and Compact both take a `func(wire.AgentEvent)
# error` sink), so a method here that *implements* one of those actions has to
# name the same type. It carries no work — nothing in this package inspects an
# event, it only hands the sink to the kernel and passes the events through — and
# transcript, session and trajectory already reach it, so the name adds no
# knowledge this package did not have.
set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/lib.sh"
cd "$TREE_ROOT"

step "webhost: direct imports"
allowed=(pi-go/internal/assembly pi-go/internal/config pi-go/internal/httpapi pi-go/internal/permission pi-go/internal/project pi-go/internal/session pi-go/internal/trajectory pi-go/internal/transcript pi-go/internal/wire)
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
