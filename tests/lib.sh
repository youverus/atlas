#!/usr/bin/env bash
# Shared helpers for the module gates in tests/.
#
# Source it, never execute it:
#   source "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/lib.sh"
#
# Every script runs from the module root ($TREE_ROOT), so the paths inside the
# scripts are relative to go/.

set -euo pipefail

TREE_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

command -v go >/dev/null 2>&1 || {
	echo "tests: go not found in PATH" >&2
	exit 1
}

if [[ -t 1 ]]; then
	C_BOLD=$'\033[1m'
	C_GREEN=$'\033[32m'
	C_RED=$'\033[31m'
	C_RESET=$'\033[0m'
else
	C_BOLD=""
	C_GREEN=""
	C_RED=""
	C_RESET=""
fi

step() { printf '\n%s==> %s%s\n' "$C_BOLD" "$1" "$C_RESET"; }
ok() { printf '    %sok%s %s\n' "$C_GREEN" "$C_RESET" "$1"; }
fail() {
	printf '    %sFAIL%s %s\n' "$C_RED" "$C_RESET" "$1" >&2
	return 1
}

# check_format dir - fails when gofmt would rewrite a file under dir.
check_format() {
	local dir="$1" unformatted
	if ! unformatted="$(gofmt -l "$dir")"; then
		fail "gofmt could not run on $dir"
		return 1
	fi
	if [[ -n $unformatted ]]; then
		fail "gofmt would rewrite: ${unformatted//$'\n'/ }"
		return 1
	fi
	ok "gofmt $dir"
}

# guard_stdlib_only target - fails when a package imports anything outside the
# standard library. Standard library paths carry no dot in their first element;
# module packages are pi-go/..., third-party modules are example.com/....
guard_stdlib_only() {
	local target="$1" dep
	for dep in $(go list -f '{{join .Imports " "}}' "$target"); do
		case "$dep" in
		*.* | pi-go/*)
			fail "$target imports $dep; this package must stay dependency free"
			return 1
			;;
		esac
	done
	ok "$target depends on the standard library only"
}

# guard_no_imports target pattern... - fails when a pattern appears anywhere in
# the dependency closure of target. Layering violations are usually indirect
# (provider -> tool -> agent), so the package's own Import list is not enough.
guard_no_imports() {
	local target="$1"
	shift
	local dep pattern
	for dep in $(go list -deps "$target"); do
		for pattern in "$@"; do
			if [[ $dep == "$pattern" ]]; then
				fail "$target depends on $dep; that layer must not be reachable from here"
				return 1
			fi
		done
	done
	ok "$target does not depend on ${*// /, }"
}

# guard_deps_allowed target module-prefix... - fails when the dependency closure
# of target contains a third-party package or a pi-go package outside the given
# prefixes. Use it for a package that legitimately imports some of our own
# packages but must not reach into the rest of the tree.
guard_deps_allowed() {
	local target="$1"
	shift
	local self dep first prefix permitted
	self="$(go list -f '{{.ImportPath}}' "$target")"
	for dep in $(go list -deps "$target"); do
		# go list -deps includes the package under test itself.
		[[ $dep == "$self" ]] && continue
		case "$dep" in
		pi-go | pi-go/*) ;;
		*)
			first="${dep%%/*}"
			case "$first" in
			*.*)
				fail "$target depends on third-party package $dep"
				return 1
				;;
			esac
			# Standard library, nothing to check.
			continue
			;;
		esac
		permitted=0
		for prefix in "$@"; do
			case "$dep" in
			"$prefix" | "$prefix"/*) permitted=1 ;;
			esac
		done
		if ((permitted == 0)); then
			fail "$target depends on $dep; allowed module packages: ${*// /, }"
			return 1
		fi
	done
	ok "$target depends only on the standard library and ${*// /, }"
}

# run_package label target... - format, vet, test and race-test one module.
# Extra targets are allowed when a module spans a package and its subpackages.
run_package() {
	local label="$1"
	shift
	local targets=("$@") target dir

	for target in "${targets[@]}"; do
		dir="${target#./}"
		dir="${dir%/...}"
		check_format "$dir"
	done

	step "$label: go vet"
	go vet "${targets[@]}"
	ok "go vet"

	step "$label: go test"
	go test -count=1 "${targets[@]}"
	ok "go test"

	step "$label: go test -race"
	go test -race -count=1 "${targets[@]}"
	ok "go test -race"
}
