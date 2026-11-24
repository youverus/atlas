#!/usr/bin/env bash
# Module gate: GUI frontend unit tests, TypeScript check, embedded bundle check,
# and end-to-end DeepSeek model configuration/reload regression tests.
set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/lib.sh"

command -v node >/dev/null 2>&1 || {
	fail "node not found in PATH"
	exit 1
}
command -v npm >/dev/null 2>&1 || {
	fail "npm not found in PATH"
	exit 1
}

cd "$TREE_ROOT/web"

if [[ ! -d node_modules ]]; then
	fail "go/web/node_modules is missing; run npm install inside go/web"
	exit 1
fi

step "gui: web unit tests"
npm test
ok "web unit tests"

step "gui: tsc --noEmit"
npm run check
ok "tsc --noEmit"

cd "$TREE_ROOT"

if [[ ! -f internal/httpapi/web/dist/index.html ]]; then
	fail "internal/httpapi/web/dist/index.html missing; run npm run build in go/web"
	exit 1
fi
ok "embedded dist present"

step "gui: DeepSeek & Anthropic URL & model config Go integration tests (-race)"
go test -race -run "DeepSeek|Anthropic|EffectiveBaseURL|NormalizeConfiguredBaseURL|ModelConfig|ReloadConfig|Key" \
	./internal/config \
	./internal/provider/openai \
	./internal/provider/anthropic \
	./internal/assembly \
	./internal/app \
	./internal/httpapi \
	./internal/webhost
ok "DeepSeek & GUI backend tests (-race)"
