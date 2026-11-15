#!/usr/bin/env bash
# Module gate: the config file (models, endpoints, credentials, first-run
# template).
#
# The package reads a file the user hand-edits, so the gate's job is to keep it
# small and dependency free: a JSON parser from a module would be a second
# opinion about the format, and the first-run template is the only thing here
# that writes anything.
set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/lib.sh"
cd "$TREE_ROOT"

guard_stdlib_only ./internal/config

step "config: no network, no adapters"
# The config decides which adapter to build but must not know how to build one:
# that is the composition root's job, and importing an adapter here would put
# the vendor choice one layer below the flag that makes it.
guard_no_imports ./internal/config \
	pi-go/internal/provider \
	pi-go/internal/provider/openai \
	pi-go/internal/provider/anthropic

step "config: a plugin declares a command and never runs one"
# plugin.json may name an MCP server's argv, and that is the whole of its power:
# the config validates the declaration and hands it to cmd/atlas, which is where
# a process is started and where the approvals live. A plugin format that could
# execute here would be a second, unapproved way to run a program on the machine
# (docs/architecture.md item 82).
imports="$(go list -f '{{join .Imports "\n"}}' ./internal/config)"
for forbidden in os/exec net net/http; do
	if grep -qx "$forbidden" <<<"$imports"; then
		fail "./internal/config imports $forbidden; a plugin is declarations, and running one belongs in cmd/atlas"
		exit 1
	fi
done
ok "./internal/config can neither run a command nor reach the network"

run_package config ./internal/config
