package assembly

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"pi-go/internal/permission"
)

func writeConfigFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config %s: %v", path, err)
	}
}

func TestKernelReloadUpdatesModelsDefaultModelAndPermissionWhenIdle(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ATLAS_HOME", home)
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	writeConfigFile(t, cfgPath, `{
		"defaultModel": "p1/m1",
		"permission": {"mode": "default"},
		"providers": {
			"p1": {
				"api": "openai-completions",
				"apiKey": "k1",
				"models": [
					{"id": "m1", "name": "Model One", "contextWindow": 8000}
				]
			}
		}
	}`)

	kernel, _, err := New(context.Background(), Options{
		ConfigPath: cfgPath,
		Workdir:    t.TempDir(),
		NoMCP:      true,
	})
	if err != nil {
		t.Fatalf("assembly.New: %v", err)
	}
	defer kernel.Close()

	// 1. Add m2 and update m1 contextWindow while keeping defaultModel p1/m1.
	writeConfigFile(t, cfgPath, `{
		"defaultModel": "p1/m1",
		"permission": {"mode": "default"},
		"providers": {
			"p1": {
				"api": "openai-completions",
				"apiKey": "k1",
				"models": [
					{"id": "m1", "name": "Model One Updated", "contextWindow": 16000},
					{"id": "m2", "name": "Model Two", "contextWindow": 32000}
				]
			}
		}
	}`)
	if err := kernel.Reload(); err != nil {
		t.Fatalf("Reload 1: %v", err)
	}
	if models := kernel.App.Models(); len(models) != 2 {
		t.Fatalf("models count = %d, want 2", len(models))
	}
	if got := kernel.App.Model(); got.ID != "m1" || got.Name != "Model One Updated" || got.ContextWindow != 16000 {
		t.Fatalf("model = %+v, want updated m1", got)
	}

	// 2. Choose m2 and mode=off in runtime, reload with unchanged defaultModel and permission.mode -> keep runtime choices.
	if _, err := kernel.App.SetModel("p1", "m2"); err != nil {
		t.Fatalf("SetModel: %v", err)
	}
	if err := kernel.Gate.Engine().SetMode(permission.ModeOff); err != nil {
		t.Fatalf("SetMode: %v", err)
	}
	if err := kernel.Reload(); err != nil {
		t.Fatalf("Reload 2: %v", err)
	}
	if got := kernel.App.Model(); got.ID != "m2" {
		t.Fatalf("model = %q, want runtime selection m2 preserved when defaultModel did not change", got.ID)
	}
	if got := kernel.Gate.Engine().Mode(); got != permission.ModeOff {
		t.Fatalf("mode = %q, want runtime selection off preserved", got)
	}

	// 3. Move runtime back to m1, then change config defaultModel to p1/m2 and permission.mode to auto -> overrides both.
	if _, err := kernel.App.SetModel("p1", "m1"); err != nil {
		t.Fatalf("SetModel back to m1: %v", err)
	}
	writeConfigFile(t, cfgPath, `{
		"defaultModel": "p1/m2",
		"permission": {"mode": "auto"},
		"providers": {
			"p1": {
				"api": "openai-completions",
				"apiKey": "k1",
				"models": [
					{"id": "m1", "name": "Model One Updated", "contextWindow": 16000},
					{"id": "m2", "name": "Model Two", "contextWindow": 32000}
				]
			}
		}
	}`)
	if err := kernel.Reload(); err != nil {
		t.Fatalf("Reload 3: %v", err)
	}
	if got := kernel.App.Model(); got.ID != "m2" {
		t.Fatalf("model = %q, want m2 after defaultModel changed in config", got.ID)
	}
	if got := kernel.Gate.Engine().Mode(); got != permission.ModeAuto {
		t.Fatalf("mode = %q, want auto after permission.mode changed in config", got)
	}

	// 4. Broken JSON returns error and leaves working state untouched.
	writeConfigFile(t, cfgPath, `{invalid json`)
	if err := kernel.Reload(); err == nil {
		t.Fatal("Reload succeeded on invalid JSON")
	}
	if got := kernel.App.Model(); got.ID != "m2" || len(kernel.App.Models()) != 2 {
		t.Fatalf("broken reload corrupted kernel state: model=%+v len=%d", got, len(kernel.App.Models()))
	}
}
