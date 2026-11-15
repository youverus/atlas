#!/usr/bin/env bash
# Module gate: internal/skill (discovery, frontmatter, the prompt block).
#
# The package finds skills and formats them, and that is the whole feature: a
# skill grants nothing, so the model loads one with the file tool it already has.
# Two checks hold that shape.
#
# The standard library, because the caller passes in the roots and the read
# limits: the package knows neither where a skill lives nor how big a read may be,
# which is what lets a test build a world inside one temporary directory.
#
# No executor and no network, because "a skill is text" is a claim the prompt
# cannot enforce on its own. A skill package that could run a file would make the
# sentence above false without changing a line of the prompt.
set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/lib.sh"
cd "$TREE_ROOT"

guard_stdlib_only ./internal/skill

step "skill: nothing is executed and nothing is fetched"
imports="$(go list -f '{{join .Imports "\n"}}' ./internal/skill)"
for forbidden in os/exec net net/http; do
	if grep -qx "$forbidden" <<<"$imports"; then
		fail "./internal/skill imports $forbidden; a skill is text the model reads, so this package only finds and formats"
		exit 1
	fi
done
ok "./internal/skill can neither run a file nor reach the network"

run_package skill ./internal/skill/...
