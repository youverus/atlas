package webhost

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pi-go/internal/assembly"
)

func TestHostStatsReportsPromptAgentsAndSkillsForActiveSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ATLAS_HOME", home)
	t.Setenv("HOME", t.TempDir())

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
	if err := os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte("# repo instructions\nFollow strict rules.\n"), 0o600); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}
	skillPath := filepath.Join(cwd, ".atlas", "skills", "inspector", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillPath), 0o755); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	if err := os.WriteFile(skillPath, []byte("---\nname: inspector\ndescription: Inspect context state\n---\nBody.\n"), 0o600); err != nil {
		t.Fatalf("write skill: %v", err)
	}

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
	stats, err := host.Stats()
	if err != nil {
		t.Fatalf("host.Stats: %v", err)
	}
	if !stats.AgentsMDLoaded || len(stats.ContextFiles) != 1 || stats.ContextFilesTokens <= 0 {
		t.Fatalf("AGENTS.md stats = (loaded=%v, files=%v, tokens=%d)", stats.AgentsMDLoaded, stats.ContextFiles, stats.ContextFilesTokens)
	}
	if stats.Skills != 1 || stats.SkillsTokens <= 0 {
		t.Fatalf("skills stats = (%d, %d tokens), want 1 skill with >0 tokens", stats.Skills, stats.SkillsTokens)
	}
	if stats.SystemPrompt == "" || !strings.Contains(stats.SystemPrompt, "Follow strict rules.") || stats.SystemPromptTokens <= 0 {
		t.Fatalf("system prompt stats invalid: len=%d tokens=%d", len(stats.SystemPrompt), stats.SystemPromptTokens)
	}
}
