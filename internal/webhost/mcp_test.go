package webhost

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"pi-go/internal/assembly"
)

func TestHostMCPReportsDefaultConfigPathAndEmptyList(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ATLAS_HOME", home)
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(cfgPath, []byte(`{
		"defaultModel": "p1/m1",
		"providers": {
			"p1": {
				"api": "openai-completions",
				"apiKey": "k1",
				"models": [{"id": "m1", "name": "One", "contextWindow": 8000}]
			}
		}
	}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	kernel, _, err := assembly.New(context.Background(), assembly.Options{
		ConfigPath: cfgPath,
		Workdir:    t.TempDir(),
		NoMCP:      true,
	})
	if err != nil {
		t.Fatalf("assembly.New: %v", err)
	}
	defer kernel.Close()

	host := NewWith(kernel, Options{Projects: filepath.Join(t.TempDir(), "projects.json")})
	state, err := host.MCP()
	if err != nil {
		t.Fatalf("host.MCP: %v", err)
	}
	wantPath := filepath.Join(home, "mcp.json")
	if state.ConfigPath != wantPath {
		t.Fatalf("ConfigPath = %q, want %q", state.ConfigPath, wantPath)
	}
	if len(state.Servers) != 0 {
		t.Fatalf("Servers = %+v, want empty", state.Servers)
	}

	refreshed, err := host.RefreshMCP("")
	if err != nil {
		t.Fatalf("host.RefreshMCP: %v", err)
	}
	if refreshed.ConfigPath != wantPath {
		t.Fatalf("refreshed ConfigPath = %q, want %q", refreshed.ConfigPath, wantPath)
	}
}
