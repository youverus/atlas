#!/usr/bin/env bash
# Tree gate. Formatting and vet over the whole module, then every module gate
# in tests/<module>/run.sh.
#
#   ./tests/run.sh             every module
#   ./tests/run.sh sse agent   only the named modules
#
# Exits non-zero when a check fails, so a milestone is only reached when this
# script is green.
set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
cd "$TREE_ROOT"

selected=("$@")
scripts=()
if ((${#selected[@]} == 0)); then
	shopt -s nullglob
	scripts=(tests/*/run.sh)
	if ((${#scripts[@]} == 0)); then
		fail "no module scripts under tests/"
		exit 1
	fi
else
	for name in "${selected[@]}"; do
		script="tests/${name%/}/run.sh"
		if [[ ! -f $script ]]; then
			fail "unknown module: $name"
			exit 1
		fi
		scripts+=("$script")
	done
fi

step "tree: gofmt"
unformatted="$(gofmt -l .)"
if [[ -n $unformatted ]]; then
	fail "gofmt would rewrite: ${unformatted//$'\n'/ }"
	exit 1
fi
ok "gofmt"

step "tree: go vet ./..."
go vet ./...
ok "go vet"

passed=()
failed=()
for script in "${scripts[@]}"; do
	module="${script#tests/}"
	module="${module%/run.sh}"
	printf '\n%s### %s%s\n' "$C_BOLD" "$module" "$C_RESET"
	if bash "$script"; then
		passed+=("$module")
	else
		failed+=("$module")
	fi
done

printf '\n%s=== summary ===%s\n' "$C_BOLD" "$C_RESET"
if ((${#passed[@]} > 0)); then
	for module in "${passed[@]}"; do
		printf '  %sPASS%s %s\n' "$C_GREEN" "$C_RESET" "$module"
	done
fi
if ((${#failed[@]} > 0)); then
	for module in "${failed[@]}"; do
		printf '  %sFAIL%s %s\n' "$C_RED" "$C_RESET" "$module"
	done
fi

if ((${#failed[@]} > 0)); then
	printf '\n%d of %d module gates failed\n' "${#failed[@]}" "$((${#passed[@]} + ${#failed[@]}))" >&2
	exit 1
fi
printf '\nall %d module gates passed\n' "${#passed[@]}"
