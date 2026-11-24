package assembly

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"pi-go/internal/config"
)

func TestMCPToggleUpdatesExistingToolFactory(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	target := newMCPSwitchServer(t, "b__ping", func(r *http.Request) bool {
		started <- struct{}{}
		select {
		case <-release:
			return true
		case <-r.Context().Done():
			return false
		}
	})
	other := newMCPSwitchServer(t, "ping", nil)
	home := writeMCPSwitchConfig(t, fmt.Sprintf(`{"mcpServers":{
	  "a":{"url":%q}, "a__b":{"url":%q,"enabled":false}
	}}`, target.url, other.url))
	if err := config.SaveMCPEnabled(home, "a", false); err != nil {
		t.Fatal(err)
	}

	state, notes, err := NewMCP(ctx, t.TempDir(), nil, "test")
	if err != nil || len(notes) != 0 {
		t.Fatalf("NewMCP: notes = %v, err = %v", notes, err)
	}
	t.Cleanup(state.Close)
	copyState := state
	tools := copyState.Tools
	if state.set == nil || len(tools()) != 0 || len(state.Specs()) != 0 {
		t.Fatal("an all-disabled configuration should have a live set with no tools or login specs")
	}
	initial := state.Describe().Servers
	if len(initial) != 2 || initial[0].Name != "a" || initial[0].State != "disabled" || initial[0].Transport != "http" || initial[1].State != "disabled" {
		t.Fatalf("initial servers = %+v, want both configured servers disabled", initial)
	}
	if target.opens.Load() != 0 || other.opens.Load() != 0 {
		t.Fatal("a disabled server received an initialize request at startup")
	}

	if err := state.SetEnabled(ctx, "a__b", true); err != nil {
		t.Fatal(err)
	}
	const otherName = "mcp__a__b__ping"
	if got := tools(); len(got) != 1 || got[0].Name() != otherName {
		t.Fatalf("tools after first enable = %v, want %s", got, otherName)
	}

	enabled := make(chan error, 1)
	go func() { enabled <- state.SetEnabled(ctx, "a", true) }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("target never started its handshake")
	}
	read := make(chan bool, 1)
	go func() {
		got := tools()
		servers, specs := copyState.Describe().Servers, copyState.Specs()
		read <- len(got) == 1 && got[0].Name() == otherName &&
			len(servers) == 2 && servers[0].State == "disabled" && servers[1].State == "connected" &&
			len(specs) == 1 && specs[0].Name == "a__b"
	}()
	select {
	case ok := <-read:
		if !ok {
			t.Fatal("readers did not see the previous snapshot during the target handshake")
		}
	case <-ctx.Done():
		t.Fatal("the target handshake blocked MCP readers")
	}
	close(release)
	if err := <-enabled; err != nil {
		t.Fatal(err)
	}
	if got := tools(); len(got) != 2 || got[0].Name() == otherName || got[1].Name() != otherName {
		t.Fatalf("tools after second enable = %v, want both tools with the existing wire name preserved", got)
	}
	if err := state.SetEnabled(ctx, "a", false); err != nil {
		t.Fatal(err)
	}
	if got := tools(); len(got) != 1 || got[0].Name() != otherName {
		t.Fatalf("tools after disabling target = %v, want only %s", got, otherName)
	}
	if got := state.Describe().Servers[0]; got.State != "disabled" || len(got.Tools) != 0 {
		t.Fatalf("target after disable = %+v", got)
	}
	if specs := state.Specs(); len(specs) != 1 || specs[0].Name != "a__b" {
		t.Fatalf("specs after disable = %+v, want only a__b", specs)
	}
	if target.closes.Load() != 1 || other.closes.Load() != 0 || other.opens.Load() != 1 {
		t.Fatalf("connection counts after disable = target closes %d, other opens %d, other closes %d",
			target.closes.Load(), other.opens.Load(), other.closes.Load())
	}

	restarted, notes, err := NewMCP(ctx, t.TempDir(), nil, "test")
	if err != nil || len(notes) != 0 {
		t.Fatalf("restart: notes = %v, err = %v", notes, err)
	}
	t.Cleanup(restarted.Close)
	if got := restarted.Describe().Servers[0]; got.State != "disabled" || target.opens.Load() != 1 {
		t.Fatalf("target after restart = %+v, initialize requests = %d", got, target.opens.Load())
	}
}

func TestMCPEnableKeepsPreferenceWhenConnectionFails(t *testing.T) {
	var available atomic.Bool
	server := newMCPSwitchServer(t, "ping", func(*http.Request) bool { return available.Load() })
	home := writeMCPSwitchConfig(t, fmt.Sprintf(`{"mcpServers":{"remote":{"url":%q,"enabled":false}}}`, server.url))
	ctx := context.Background()
	state, _, err := NewMCP(ctx, t.TempDir(), nil, "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(state.Close)
	if err := state.SetEnabled(ctx, "remote", true); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	if got := state.Describe().Servers[0]; got.State != "failed" || got.Error == "" || len(state.Tools()) != 0 {
		t.Fatalf("failed server = %+v, want a visible connection failure with no tools", got)
	}
	prefs, err := config.LoadCapabilities(home)
	if err != nil || !prefs.MCP["remote"] || len(state.Specs()) != 1 {
		t.Fatalf("failed enable should retain the enabled preference and spec, preferences = %+v, err = %v", prefs, err)
	}

	available.Store(true)
	if err := state.SetEnabled(ctx, "remote", true); err != nil {
		t.Fatalf("retry SetEnabled: %v", err)
	}
	if got := state.Describe().Servers[0]; got.State != "connected" || len(got.Tools) != 1 || server.opens.Load() != 2 {
		t.Fatalf("server after retry = %+v, initialize requests = %d", got, server.opens.Load())
	}
	copyState := state
	copyState.Close()
	if err := state.SetEnabled(ctx, "remote", false); err == nil {
		t.Fatal("SetEnabled succeeded after another copy closed the set")
	}
}

func writeMCPSwitchConfig(t *testing.T, content string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("ATLAS_HOME", home)
	if err := os.WriteFile(filepath.Join(home, config.MCPFileName), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

type mcpSwitchServer struct {
	url    string
	opens  atomic.Int32
	closes atomic.Int32
}

func newMCPSwitchServer(t *testing.T, toolName string, accept func(*http.Request) bool) *mcpSwitchServer {
	t.Helper()
	server := &mcpSwitchServer{}
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		case http.MethodDelete:
			server.closes.Add(1)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var request struct {
			ID     json.RawMessage
			Method string
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode MCP request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch request.Method {
		case "initialize":
			server.opens.Add(1)
			if accept != nil && !accept(r) {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Mcp-Session-Id", "test-session")
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-06-18","serverInfo":{"name":"test","version":"1"}}}`, request.ID)
		case "tools/list":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"tools":[{"name":%q,"inputSchema":{"type":"object"}}]}}`, request.ID, toolName)
		default:
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	t.Cleanup(httpServer.Close)
	server.url = httpServer.URL
	return server
}
