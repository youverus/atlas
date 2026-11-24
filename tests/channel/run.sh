#!/usr/bin/env bash
# Module gate: the IM gateway (a separate Go module, like desktop/).
#
# channel/ lives outside the kernel module on purpose: its Feishu adapter
# imports a third-party SDK, and tests/deps/run.sh keeps internal/ stdlib-only
# so the kernel builds in an empty module cache. This gate guards the same
# three promises for the module it owns: the replace directive points at the
# working tree, the SDK stays pinned to the release tag the gate knows, and
# the package tests pass offline — none of them need network, tokens, or a
# running Feishu app.
set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/lib.sh"
cd "$TREE_ROOT"

if [[ ! -f channel/go.mod ]]; then
    fail "channel/go.mod is missing; the gateway must stay a separate module"
    exit 1
fi

cd "$TREE_ROOT/channel"

step "channel: gofmt"
unformatted="$(gofmt -l .)"
if [[ -n $unformatted ]]; then
    fail "gofmt would rewrite: ${unformatted//$'\n'/ }"
    exit 1
fi
ok "gofmt"

step "channel: replace points at the kernel next door"
if ! grep -q '^replace pi-go => \.\.$' go.mod; then
    fail "channel/go.mod must replace pi-go with the parent directory"
    exit 1
fi
ok "replace pi-go => .."

# The SDK is the module's one third-party require; pinning it here means an
# accidental `go get -u` fails the gate instead of silently changing the
# wire this adapter speaks.
step "channel: the Feishu SDK is still pinned"
pinned="$(awk '$1 == "github.com/larksuite/oapi-sdk-go/v3" { print $2; exit }' go.mod)"
if [[ -z $pinned ]]; then
    fail "go.mod does not require github.com/larksuite/oapi-sdk-go/v3"
    exit 1
fi
expected="v3.13.0"
if [[ $pinned != "$expected" ]]; then
    fail "SDK pinned to $pinned; this gate pins $expected (bump both together)"
    exit 1
fi
ok "github.com/larksuite/oapi-sdk-go/v3 $pinned"

step "channel: go vet"
CGO_ENABLED=0 go vet ./...
ok "go vet"

# -o /dev/null rather than a plain build: the module root would otherwise drop
# the binary next to the sources on every gate run.
step "channel: go build"
CGO_ENABLED=0 go build -o /dev/null ./cmd/atlas-gateway
ok "go build"

step "channel: go test"
go test ./...
ok "go test"
