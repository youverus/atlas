#!/usr/bin/env bash
# Module gate: the full-screen interface.
#
# internal/tui is the one package allowed to depend on a third-party module, so
# the guards here are about the shape of that dependency rather than about its
# existence. Two things have to stay true or the boundary the deps gate draws
# becomes decorative:
#
#   1. Everything it pulls in comes from the family tests/deps pins, so a stray
#      dependency cannot enter the binary through the interface.
#   2. It talks to the kernel through wire and theme only. The interface must not
#      import app or agent: cmd/atlas adapts between them, and that is what keeps
#      the program testable headless and the kernel unaware of the screen.
set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/lib.sh"
cd "$TREE_ROOT"

# pinned_family is what the deps gate pins. The interface may use anything those
# modules already bring in, and nothing else: a module that bubbletea does not
# need is a decision, and decisions do not arrive through an import statement.
pinned_family=(
	charm.land/bubbletea/v2
	charm.land/glamour/v2
	charm.land/lipgloss/v2
)

# allowed_local is the whole of what the interface may know about the kernel.
allowed_local=("pi-go/internal/theme" "pi-go/internal/wire")

step "tui: the interface adds no module of its own"
third_party() {
	local dep
	while read -r dep; do
		[[ -z $dep ]] && continue
		case "$dep" in
		pi-go | pi-go/*) continue ;;
		esac
		[[ ${dep%%/*} == *.* ]] || continue # the standard library
		printf '%s\n' "$dep"
	done
}

family_file="$(mktemp)"
tui_file="$(mktemp)"
trap 'rm -f "$family_file" "$tui_file"' EXIT
third_party < <(go list -deps "${pinned_family[@]}") | sort -u >"$family_file"
third_party < <(go list -deps ./internal/tui) | sort -u >"$tui_file"

stray="$(comm -13 "$family_file" "$tui_file" || true)"
if [[ -n $stray ]]; then
	while read -r dep; do
		[[ -z $dep ]] && continue
		fail "internal/tui reaches $dep, which none of ${pinned_family[*]} needs"
	done <<<"$stray"
	exit 1
fi
ok "every module reachable from the interface is reachable from ${pinned_family[*]}"

step "tui: the interface talks to the kernel through wire and theme only"
imported=()
while read -r dep; do
	[[ $dep == pi-go/internal/tui ]] && continue
	case "$dep" in
	pi-go | pi-go/*) ;;
	*) continue ;;
	esac
	permitted=0
	for allowed in "${allowed_local[@]}"; do
		[[ $dep == "$allowed" ]] && permitted=1
	done
	((permitted == 1)) || imported+=("$dep")
done < <(go list -deps ./internal/tui)

if ((${#imported[@]} > 0)); then
	for dep in "${imported[@]}"; do
		fail "internal/tui depends on $dep"
	done
	fail "the interface may depend on: ${allowed_local[*]}"
	exit 1
fi
ok "the interface depends on ${allowed_local[*]} and nothing else of ours"

run_package tui ./internal/tui
