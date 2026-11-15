#!/usr/bin/env bash
# Module gate: the dependency boundary.
#
# atlas was stdlib only. 0.4.0 drops that goal for the presentation layer: the
# TUI is Bubble Tea (charm.land/bubbletea/v2) and Lip Gloss for styles, and the
# binary carries them. The kernel does not get to follow. Everything under internal/
# except internal/tui stays dependency free, because that is what keeps the
# orchestration buildable in an empty module cache and every offline test
# meaning what it says. A gate is the only version of that rule that survives
# the next import statement.
#
# Three checks, in increasing order of how far they see:
#
#   1. The modules we chose are pinned, at the versions we chose, and as tags.
#   2. No package outside the allowlist imports a third-party module.
#   3. The dependency closure of every kernel package holds no third-party
#      module at all. Check 2 reads direct imports only, so it cannot see a
#      kernel package reaching a dependency through one of our own layers; this
#      one asks what would actually get linked.
set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/lib.sh"
cd "$TREE_ROOT"

# pinned_modules is the whole of the presentation layer's allowance: the module
# and the exact version. A tag is a release upstream supports; a pseudo-version
# is whatever main happened to be on a Tuesday, and pinning one makes the build
# unreproducible in a way go.sum cannot fix.
#
# Only what is imported. A module pinned "for later" is a module go mod tidy
# deletes, and a gate that fails the first time anyone runs tidy is a gate
# people learn to bypass: charm.land/bubbles was on this list until 0.4.0
# shipped without it, and glamour came back with the answers at 0.12.4.
pinned_modules=(
	"charm.land/bubbletea/v2 v2.1.0"
	"charm.land/glamour/v2 v2.0.1"
	"charm.land/lipgloss/v2 v2.0.6"
)

# ui_prefixes are the import path prefixes allowed to pull any of them in.
# cmd/atlas is on the list because it owns the program's construction; nothing
# under internal/ is, except the TUI itself.
ui_prefixes=(pi-go/internal/tui pi-go/cmd/atlas)

# kernel_prefix is what check 3 guards: every package under internal/ that is
# not part of the TUI. Deriving it instead of listing it means a package added
# later is guarded the moment it exists, which is the point of the gate.
kernel_prefix=pi-go/internal/

step "deps: the pinned modules are still pinned"
for entry in "${pinned_modules[@]}"; do
	module="${entry% *}"
	version="${entry##* }"
	pinned="$(awk -v m="$module" '$1 == m { print $2; exit }' go.mod)"
	if [[ -z $pinned ]]; then
		fail "go.mod does not require $module; run: go get $module@$version"
		exit 1
	fi
	if [[ $pinned != "$version" ]]; then
		fail "go.mod pins $module $pinned; this gate pins $version"
		exit 1
	fi
	case "$pinned" in
	v0.0.0-* | *+incompatible)
		fail "$module is pinned to $pinned; pin a release tag instead"
		exit 1
		;;
	esac
	ok "$module $pinned"
done

step "deps: only the UI packages import a third-party module"
allowed() {
	local pkg="$1" prefix
	for prefix in "${ui_prefixes[@]}"; do
		case "$pkg" in
		"$prefix" | "$prefix"/*) return 0 ;;
		esac
	done
	return 1
}

# Test imports are scanned too: a third-party assertion library is a dependency
# of the repository even though it never reaches the binary.
violations=()
while read -r pkg dep; do
	[[ -z ${pkg:-} ]] && continue
	if ! allowed "$pkg"; then
		violations+=("$pkg imports $dep")
	fi
done < <(go list -f '{{.ImportPath}}{{range .Imports}} {{.}}{{end}}{{range .TestImports}} {{.}}{{end}}{{range .XTestImports}} {{.}}{{end}}' ./... |
	awk '{
		pkg = $1
		for (i = 2; i <= NF; i++) {
			if ($i ~ /^pi-go\//) continue
			split($i, part, "/")
			if (part[1] ~ /\./) print pkg, $i
		}
	}')

if ((${#violations[@]} > 0)); then
	for line in "${violations[@]}"; do
		fail "$line"
	done
	fail "third-party imports are allowed in: ${ui_prefixes[*]}"
	exit 1
fi
ok "every third-party import comes from ${ui_prefixes[*]}"

step "deps: the kernel closure is third-party free"
kernel=()
while read -r pkg; do
	[[ -z $pkg ]] && continue
	case "$pkg" in
	"${kernel_prefix}tui" | "${kernel_prefix}tui"/*) continue ;;
	esac
	kernel+=("$pkg")
done < <(go list ./internal/... 2>/dev/null || true)

if ((${#kernel[@]} == 0)); then
	fail "no kernel packages found under internal/"
	exit 1
fi

leaks=()
for pkg in "${kernel[@]}"; do
	while read -r dep; do
		[[ -z $dep ]] && continue
		[[ $dep == pi-go || $dep == pi-go/* ]] && continue
		first="${dep%%/*}"
		[[ $first == *.* ]] || continue
		leaks+=("$pkg reaches $dep")
	done < <(go list -deps "$pkg")
done

if ((${#leaks[@]} > 0)); then
	for line in "${leaks[@]}"; do
		fail "$line"
	done
	fail "the kernel must build in an empty module cache; only ${ui_prefixes[*]} may depend on a module"
	exit 1
fi
ok "${#kernel[@]} kernel packages depend on the standard library only"
