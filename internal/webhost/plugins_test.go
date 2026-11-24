package webhost

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHostPluginsListsDiscoveredPluginAndSkills(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ATLAS_HOME", home)
	t.Setenv("HOME", t.TempDir())

	host, _, _ := sessionHost(t)

	pluginDir := filepath.Join(home, "plugins", "demo-plugin")
	skillFile := filepath.Join(pluginDir, "skills", "lint-check", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillFile), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.json"), []byte(`{"description":"Demo tools","skills":"skills"}`), 0o600); err != nil {
		t.Fatalf("write plugin.json: %v", err)
	}
	if err := os.WriteFile(skillFile, []byte("---\nname: lint-check\ndescription: Run lint check\n---\nCheck lint.\n"), 0o600); err != nil {
		t.Fatalf("write skill: %v", err)
	}

	state, err := host.Plugins()
	if err != nil {
		t.Fatalf("Plugins: %v", err)
	}
	if len(state.Plugins) != 1 {
		t.Fatalf("expected 1 plugin, got %+v", state)
	}
	p := state.Plugins[0]
	if p.Name != "demo-plugin" || p.Description != "Demo tools" {
		t.Fatalf("unexpected plugin metadata: %+v", p)
	}
	if len(p.SkillNames) != 1 || p.SkillNames[0] != "lint-check" {
		t.Fatalf("expected contributed skill lint-check, got %+v", p.SkillNames)
	}
}
