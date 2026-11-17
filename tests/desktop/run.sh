#!/usr/bin/env bash
# Module gate: the windowed shell (a separate Go module).
#
# desktop lives outside the kernel module on purpose: it needs cgo and the
# platform WebView, while the CLI is cross-compiled with CGO_ENABLED=0. Two
# modules means `go vet ./...` inside the kernel never needs a GUI toolchain,
# and a broken Xcode install cannot stop `atlas` from building.
#
# What is checked here is only what a skeleton can promise: it formats, it
# vets, and it links. There is no test step because there is nothing to assert
# yet — the interesting behaviour is in internal/httpapi, which has its own
# gate. When the shell grows real wiring, this is where the first test goes.
#
# CGO_ENABLED is set explicitly: with it unset, a machine whose default is 0
# reports "build constraints exclude all Go files" from the WebView package
# rather than "you need cgo", and that message has cost hours before.
set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/lib.sh"

cd "$TREE_ROOT/desktop"

if [[ ! -f go.mod ]]; then
    fail "desktop/go.mod is missing; the shell must stay a separate module"
    exit 1
fi

step "desktop: gofmt"
unformatted="$(gofmt -l .)"
if [[ -n $unformatted ]]; then
    fail "gofmt would rewrite: ${unformatted//$'\n'/ }"
    exit 1
fi
ok "gofmt"

step "desktop: go vet"
CGO_ENABLED=1 go vet ./...
ok "go vet"

# -o /dev/null rather than a plain build: the module root is a main package, so
# `go build` would drop a 17 MB executable next to the sources and the gate
# would recreate it on every run. The cli gate discards its binary the same way.
step "desktop: go build"
CGO_ENABLED=1 go build -o /dev/null .
ok "go build"

# The replace directive is what keeps the shell on the working tree instead of a
# published kernel; without it the build silently tests an old tagged version.
step "desktop: replace points at the kernel next door"
if ! grep -q '^replace pi-go => \.\.$' go.mod; then
    fail "desktop/go.mod must replace pi-go with the parent directory"
    exit 1
fi
ok "replace pi-go => .."
