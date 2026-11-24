package permission

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pi-go/internal/tool"
)

func bashCall(command string) tool.Call {
	raw, _ := json.Marshal(map[string]string{"command": command})
	return tool.Call{ID: "c1", Name: "bash", Arguments: raw}
}

func namedCall(name string) tool.Call {
	return tool.Call{ID: "c1", Name: name, Arguments: json.RawMessage(`{}`)}
}

func mustParse(t *testing.T, body string) *Store {
	t.Helper()
	store, err := Parse([]byte(body))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return store
}

// TestDenyBeatsEverything verifies step 1a: a deny rule wins over a matching
// allow rule and over bypassPermissions mode.
func TestDenyBeatsEverything(t *testing.T) {
	store := mustParse(t, `{
		"version": 2,
		"allow": [{"tool_name": "bash"}],
		"deny":  [{"tool_name": "bash", "matcher": {"kind": "command", "pattern": "rm:*"}}],
		"ask":   []
	}`)
	engine := New(Options{
		Mode:  ModeBypass,
		Store: store,
	})

	if got := engine.Decide(bashCall("rm -rf build")); got.Behavior != Deny {
		t.Fatalf("Decide(rm -rf build) = %+v, want %q", got, Deny)
	}

	wholeDeny := mustParse(t, `{
		"version": 2,
		"allow": [{"tool_name": "write"}],
		"deny":  [{"tool_name": "write"}]
	}`)
	engineWhole := New(Options{
		Mode:  ModeBypass,
		Store: wholeDeny,
	})
	if got := engineWhole.Decide(namedCall("write")); got.Behavior != Deny {
		t.Fatalf("Decide(write) = %+v, want %q", got, Deny)
	}
}

// TestBypassAllowsUnmatched verifies step 2a: under bypassPermissions, a tool
// with no matching rule (and an ordinary checker ask) is allowed, while a
// bypassImmune checker ask still asks.
func TestBypassAllowsUnmatched(t *testing.T) {
	engine := New(Options{
		Mode: ModeBypass,
		Checkers: map[string]Checker{
			"write": func(tool.Call) (CheckResult, error) {
				return CheckResult{Behavior: Ask, Reason: "ordinary checker ask"}, nil
			},
			"secret": func(tool.Call) (CheckResult, error) {
				return CheckResult{Behavior: Ask, Reason: "keychain access", BypassImmune: true}, nil
			},
		},
	})

	if got := engine.Decide(namedCall("write")); got.Behavior != Allow {
		t.Fatalf("Decide(write) under bypass = %+v, want %q", got, Allow)
	}
	if got := engine.Decide(namedCall("secret")); got.Behavior != Ask {
		t.Fatalf("Decide(secret) with BypassImmune = %+v, want %q", got, Ask)
	}
}

// TestBypassKeepsAskRules verifies that bypassPermissions sits below explicit
// ask rules in the ladder: a whole-tool or content-specific ask rule still
// prompts even in bypassPermissions.
func TestBypassKeepsAskRules(t *testing.T) {
	store := mustParse(t, `{
		"version": 2,
		"ask": [
			{"tool_name": "write"},
			{"tool_name": "bash", "matcher": {"kind": "command", "pattern": "git push:*"}}
		]
	}`)
	engine := New(Options{
		Mode:  ModeBypass,
		Store: store,
	})

	if got := engine.Decide(namedCall("write")); got.Behavior != Ask {
		t.Fatalf("Decide(write) with whole-tool ask rule under bypass = %+v, want %q", got, Ask)
	}
	if got := engine.Decide(bashCall("git push origin main")); got.Behavior != Ask {
		t.Fatalf("Decide(git push) with content ask rule under bypass = %+v, want %q", got, Ask)
	}
	if got := engine.Decide(bashCall("git status")); got.Behavior != Allow {
		t.Fatalf("Decide(git status) under bypass = %+v, want %q", got, Allow)
	}
}

// TestDefaultFallsBack pins the asymmetry at step 3: in default mode a tool
// with no checker is allowed, while a tool whose checker returns Undecided
// asks.
func TestDefaultFallsBack(t *testing.T) {
	engine := New(Options{
		Mode: ModeDefault,
	})

	if got := engine.Decide(namedCall("read")); got.Behavior != Allow {
		t.Fatalf("Decide(read) with no checker = %+v, want %q", got, Allow)
	}
	if got := engine.Decide(namedCall("write")); got.Behavior != Ask {
		t.Fatalf("Decide(write) with undecided checker = %+v, want %q", got, Ask)
	}
}

// TestContentRuleMatchesOneCommand verifies that a command matcher targets only
// the matching command (including inside a compound pipeline) and leaves other
// commands on the same tool alone.
func TestContentRuleMatchesOneCommand(t *testing.T) {
	store := mustParse(t, `{
		"version": 2,
		"allow": [{"tool_name": "bash", "matcher": {"kind": "command", "pattern": "go test:*"}}],
		"ask":   [{"tool_name": "bash", "matcher": {"kind": "command", "pattern": "curl:*"}}]
	}`)
	engine := New(Options{
		Mode:  ModeBypass,
		Store: store,
	})

	if got := engine.Decide(bashCall("curl https://example.com")); got.Behavior != Ask {
		t.Fatalf("Decide(curl) = %+v, want %q", got, Ask)
	}
	if got := engine.Decide(bashCall("echo ready && ENV=1 curl -I https://example.com")); got.Behavior != Ask {
		t.Fatalf("Decide(compound curl) = %+v, want %q", got, Ask)
	}
	if got := engine.Decide(bashCall("go test ./...")); got.Behavior != Allow {
		t.Fatalf("Decide(go test) = %+v, want %q", got, Allow)
	}
}

// TestSandboxSkipsOnlyWholeToolAsk verifies step 1b: when both SandboxEnabled
// and AutoAllowBashIfSandboxed are true, a whole-tool bash ask rule (and the
// default bash checker prompt) is skipped, while a content-specific ask rule
// and a deny rule still fire.
func TestSandboxSkipsOnlyWholeToolAsk(t *testing.T) {
	store := mustParse(t, `{
		"version": 2,
		"deny": [{"tool_name": "bash", "matcher": {"kind": "command", "pattern": "rm -rf /"}}],
		"ask": [
			{"tool_name": "bash"},
			{"tool_name": "bash", "matcher": {"kind": "command", "pattern": "curl:*"}}
		]
	}`)
	engine := New(Options{
		Mode:                     ModeDefault,
		Store:                    store,
		SandboxEnabled:           true,
		AutoAllowBashIfSandboxed: true,
	})

	if got := engine.Decide(bashCall("ls -la")); got.Behavior != Allow {
		t.Fatalf("Decide(ls -la) with sandbox auto-allow = %+v, want %q", got, Allow)
	}
	if got := engine.Decide(bashCall("curl https://example.com")); got.Behavior != Ask {
		t.Fatalf("Decide(curl) with content ask rule = %+v, want %q", got, Ask)
	}
	if got := engine.Decide(bashCall("rm -rf /")); got.Behavior != Deny {
		t.Fatalf("Decide(rm -rf /) with deny rule = %+v, want %q", got, Deny)
	}
}

// TestSandboxSwitchRequiresASandbox verifies that AutoAllowBashIfSandboxed does
// nothing when SandboxEnabled is false.
func TestSandboxSwitchRequiresASandbox(t *testing.T) {
	store := mustParse(t, `{
		"version": 2,
		"ask": [{"tool_name": "bash"}]
	}`)
	engine := New(Options{
		Mode:                     ModeDefault,
		Store:                    store,
		SandboxEnabled:           false,
		AutoAllowBashIfSandboxed: true,
	})

	if got := engine.Decide(bashCall("ls -la")); got.Behavior != Ask {
		t.Fatalf("Decide(ls -la) without an active sandbox = %+v, want %q", got, Ask)
	}
}

// TestBrokenRuleStoreFailsClosed verifies that an unreadable or malformed
// permission.json forces Ask in default mode and Deny in bypassPermissions.
func TestBrokenRuleStoreFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(path, []byte(`{"version": 2, broken`), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := LoadFile(path)
	if err == nil {
		t.Fatal("LoadFile accepted broken JSON")
	}

	defaultEngine := New(Options{Mode: ModeDefault, Store: store})
	if got := defaultEngine.Decide(namedCall("read")); got.Behavior != Ask {
		t.Fatalf("default mode with broken store = %+v, want %q", got, Ask)
	}

	bypassEngine := New(Options{Mode: ModeBypass, Store: store})
	if got := bypassEngine.Decide(namedCall("read")); got.Behavior != Deny {
		t.Fatalf("bypassPermissions with broken store = %+v, want %q", got, Deny)
	}
}

// TestConfigRefuses verifies that an unsupported version, an unknown field, an
// empty tool_name, or an unknown matcher.kind is rejected with the offending
// key in the error.
func TestConfigRefuses(t *testing.T) {
	cases := []struct {
		name string
		json string
		want string
	}{
		{
			name: "missing version",
			json: `{"allow": []}`,
			want: "version",
		},
		{
			name: "unsupported version",
			json: `{"version": 1, "allow": []}`,
			want: "version",
		},
		{
			name: "unknown top-level field",
			json: `{"version": 2, "rules": []}`,
			want: `unknown field "rules"`,
		},
		{
			name: "unknown rule field",
			json: `{"version": 2, "allow": [{"toolName": "bash"}]}`,
			want: `allow[0]: json: unknown field "toolName"`,
		},
		{
			name: "empty tool_name",
			json: `{"version": 2, "allow": [{"tool_name": "  "}]}`,
			want: "allow[0].tool_name",
		},
		{
			name: "unknown matcher kind",
			json: `{"version": 2, "allow": [{"tool_name": "bash", "matcher": {"kind": "regex", "pattern": ".*"}}]}`,
			want: "allow[0].matcher.kind",
		},
		{
			name: "command matcher without pattern",
			json: `{"version": 2, "ask": [{"tool_name": "bash", "matcher": {"kind": "command"}}]}`,
			want: "ask[0].matcher.pattern",
		},
		{
			name: "unknown matcher field",
			json: `{"version": 2, "ask": [{"tool_name": "bash", "matcher": {"kind": "command", "pattern": "ls", "extra": true}}]}`,
			want: `ask[0].matcher: json: unknown field "extra"`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.json))
			if err == nil {
				t.Fatalf("Parse accepted invalid input: %s", tc.json)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %q, want it to mention %q", err.Error(), tc.want)
			}
		})
	}
}

// TestAskStopsTheCall verifies that when a write call hits an ask decision and
// the user declines (or no terminal is present), Gate blocks the call and gives
// the model an actionable reason that says not to retry.
func TestAskStopsTheCall(t *testing.T) {
	store := mustParse(t, `{
		"version": 2,
		"ask": [{"tool_name": "write"}]
	}`)
	engine := New(Options{
		Mode:  ModeDefault,
		Store: store,
	})

	ran := false
	call := tool.Call{
		ID:        "c1",
		Name:      "write",
		Arguments: json.RawMessage(`{"path":"out.txt","content":"hello"}`),
	}
	blocked, reason := engine.Gate(context.Background(), call, func(tool.Call) bool {
		return false
	})
	if !blocked {
		ran = true
	}
	if ran {
		t.Fatal("write executed despite declined ask")
	}
	if !strings.Contains(reason, "declined") || !strings.Contains(reason, "do not retry") {
		t.Fatalf("reason = %q, want an actionable refusal", reason)
	}
}

func TestReloadUpdatesRulesAndOptionalMode(t *testing.T) {
	initialStore := mustParse(t, `{
		"version": 2,
		"allow": [{"tool_name": "write"}]
	}`)
	engine := New(Options{
		Mode:  ModeDefault,
		Store: initialStore,
	})
	engine.AllowForSession("s1", "edit")
	if err := engine.SetMode(ModeBypass); err != nil {
		t.Fatalf("set mode: %v", err)
	}

	nextStore := mustParse(t, `{
		"version": 2,
		"deny": [{"tool_name": "write"}]
	}`)
	if err := engine.Reload(Options{Mode: ModeDefault, Store: nextStore}, false); err != nil {
		t.Fatalf("reload without mode override: %v", err)
	}
	if got := engine.Mode(); got != ModeBypass {
		t.Fatalf("mode = %q, want %q preserved when updateMode=false", got, ModeBypass)
	}
	if !engine.AllowedForSession("s1", "edit") {
		t.Fatal("session approval was lost across reload")
	}
	writeCall := tool.Call{ID: "c1", Name: "write", Arguments: json.RawMessage(`{"path":"out.txt","content":"hi"}`)}
	if d := engine.Decide(writeCall); d.Behavior != Deny {
		t.Fatalf("write decision = %+v, want Deny from reloaded store", d)
	}

	if err := engine.Reload(Options{Mode: "invalid", Store: nextStore}, true); err == nil {
		t.Fatal("reload accepted invalid mode when updateMode=true")
	}
	if err := engine.Reload(Options{Mode: ModeOff, Store: nextStore}, true); err != nil {
		t.Fatalf("reload with mode override: %v", err)
	}
	if got := engine.Mode(); got != ModeOff {
		t.Fatalf("mode = %q, want %q", got, ModeOff)
	}

	// Concurrent Decide and Reload must be race-free.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 100 {
			_ = engine.Decide(writeCall)
		}
	}()
	for range 100 {
		_ = engine.Reload(Options{Mode: ModeOff, Store: nextStore}, false)
	}
	<-done
}
