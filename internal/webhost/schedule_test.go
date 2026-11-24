package webhost

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"pi-go/internal/assembly"
)

func TestRunScheduledTaskSessionResolutionAndValidation(t *testing.T) {
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

	cwd := t.TempDir()
	kernel, _, err := assembly.New(context.Background(), assembly.Options{
		ConfigPath: cfgPath,
		Workdir:    cwd,
		NoMCP:      true,
	})
	if err != nil {
		t.Fatalf("assembly.New: %v", err)
	}
	defer kernel.Close()

	host := NewWith(kernel, Options{Projects: filepath.Join(t.TempDir(), "projects.json")})

	// Empty prompt rejected before touching session.
	if _, _, err := host.RunScheduledTask(context.Background(), "  ", "", cwd, "new", ""); err == nil {
		t.Fatal("expected error for empty prompt")
	}

	// Unknown model rejected cleanly before executing prompt.
	if _, _, err := host.RunScheduledTask(context.Background(), "hello", "missing/model", cwd, "new", ""); err == nil {
		t.Fatal("expected error for unknown model")
	}

	// Missing continue session falls back to opening a new session in cwd; passing an
	// already-cancelled context aborts Ask cleanly after creating the live session.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, askErr := host.RunScheduledTask(ctx, "hello", "p1/m1", cwd, "continue", filepath.Join(dirNotExist(t), "gone.jsonl"))
	if askErr == nil {
		t.Fatal("expected cancelled context error from Ask")
	}
}

func dirNotExist(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "missing")
}
