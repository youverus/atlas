#!/usr/bin/env bash
# Module gate: the model catalog.
#
# The catalog is data plus a clamp, so the tests have to be worth more than the
# code: they pin every number in models.json, every rule validate enforces, and
# the four clamp rules. All of it runs offline — the table is embedded, and
# nothing here opens a socket or reads the filesystem.
#
# The package stays stdlib only. Mapping an Entry onto wire.Model is one struct
# literal in cmd/atlas, and keeping that out of here is what lets the table be
# tested with no kernel, no config file and no adapter.
set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/lib.sh"
cd "$TREE_ROOT"

guard_stdlib_only ./internal/catalog

run_package catalog ./internal/catalog
