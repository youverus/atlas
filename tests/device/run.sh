#!/usr/bin/env bash
# Module gate: internal/device (remote SSH hosts, tunnels, and event bridge).
#
# Enforces dependency isolation: stdlib plus internal/config, internal/httpapi,
# and internal/project only (zero third-party SSH libraries, no webhost/provider).
set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/lib.sh"
cd "$TREE_ROOT"

step "device: direct imports"
allowed=(pi-go/internal/config pi-go/internal/httpapi pi-go/internal/project)
imports="$(go list -f '{{join .Imports "\n"}}' ./internal/device)"
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
		fail "device imports $dep; allowed: ${allowed[*]}"
		exit 1
	fi
done <<<"$imports"
ok "direct imports"

step "device: no third-party packages"
if go list -deps ./internal/device | grep -qE '^[a-z0-9.-]+\.[a-z]{2,}/'; then
	fail "device depends on a third-party package"
	exit 1
fi
ok "standard library only"

run_package device ./internal/device
