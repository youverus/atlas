package assembly

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"pi-go/internal/config"
	"pi-go/internal/tool"
)

func TestWebToolsUsesOfficialDeepSeekProviderKeyByDefault(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "")

	cfg := &config.Config{
		Providers: map[string]config.Provider{
			"deepseek": {
				API:     "openai-completions",
				BaseURL: "https://api.deepseek.com/v1",
				APIKey:  "sk-ds-official",
				Models:  []config.Model{{ID: "deepseek-chat"}},
			},
		},
	}

	tools, note := WebTools(cfg, nil)
	if note != "" {
		t.Fatalf("note = %q, want empty", note)
	}
	if len(tools) != 2 {
		t.Fatalf("tools = %d, want 2", len(tools))
	}
	if tools[0].Name() != "deepseek_web_search" || tools[1].Name() != "deepseek_web_fetch" {
		t.Fatalf("tool names = [%q, %q], want [deepseek_web_search, deepseek_web_fetch]", tools[0].Name(), tools[1].Name())
	}
}

func TestWebToolsDoesNotBorrowThirdPartyProviderKey(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "")

	cfg := &config.Config{
		Providers: map[string]config.Provider{
			"deepseek": {
				API:     "openai-completions",
				BaseURL: "https://proxy.example.com/v1",
				APIKey:  "sk-third-party",
				Models:  []config.Model{{ID: "deepseek-chat"}},
			},
		},
	}

	tools, note := WebTools(cfg, nil)
	if note != "" {
		t.Fatalf("note = %q, want empty", note)
	}
	if len(tools) != 0 {
		t.Fatalf("tools = %d, want 0 when only a third-party baseUrl is configured", len(tools))
	}
}

func TestWebToolsFallsBackToDeepSeekAPIKeyEnv(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "sk-env-deepseek")

	cfg := &config.Config{
		Providers: map[string]config.Provider{
			"anthropic": {
				API:     "anthropic-messages",
				BaseURL: "https://api.anthropic.com/v1",
				APIKey:  "sk-ant",
				Models:  []config.Model{{ID: "claude-sonnet-4-5"}},
			},
		},
	}

	tools, note := WebTools(cfg, nil)
	if note != "" {
		t.Fatalf("note = %q, want empty", note)
	}
	if len(tools) != 2 {
		t.Fatalf("tools = %d, want 2", len(tools))
	}
	if tools[0].Name() != "deepseek_web_search" || tools[1].Name() != "deepseek_web_fetch" {
		t.Fatalf("tool names = [%q, %q]", tools[0].Name(), tools[1].Name())
	}
}

func TestWebToolsRespectsCustomWebBlockWithoutLeakingDeepSeekKey(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "sk-env-deepseek")

	cfg := &config.Config{
		Web: &config.Web{
			BaseURL: "https://custom.search.example.com",
		},
	}

	tools, note := WebTools(cfg, nil)
	if len(tools) != 0 {
		t.Fatalf("tools = %d, want 0 when custom web block has no credential", len(tools))
	}
	if !strings.Contains(note, "web tools are off") {
		t.Fatalf("note = %q, want warning", note)
	}
}

func TestWebToolsOfficialWebBlockFallsBackToProviderKey(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "")

	cfg := &config.Config{
		Web: &config.Web{
			BaseURL:   "https://api.deepseek.com",
			APIKeyEnv: "DEEPSEEK_API_KEY",
		},
		Providers: map[string]config.Provider{
			"deepseek": {
				API:     "openai-completions",
				BaseURL: "https://api.deepseek.com/v1",
				APIKey:  "sk-provider-ds",
				Models:  []config.Model{{ID: "deepseek-chat"}},
			},
		},
	}

	tools, note := WebTools(cfg, nil)
	if note != "" {
		t.Fatalf("note = %q, want empty", note)
	}
	if len(tools) != 2 {
		t.Fatalf("tools = %d, want 2", len(tools))
	}
}

func TestKernelRegistersDeepSeekWebToolsBeforeBrowserTools(t *testing.T) {
	t.Setenv("ATLAS_HOME", t.TempDir())
	t.Setenv("DEEPSEEK_API_KEY", "")

	cfgPath := filepath.Join(t.TempDir(), "config.json")
	writeConfigFile(t, cfgPath, `{
		"defaultModel": "deepseek/deepseek-chat",
		"browser": {"executable": "/tmp/fake-chrome"},
		"providers": {
			"deepseek": {
				"api": "openai-completions",
				"baseUrl": "https://api.deepseek.com/v1",
				"apiKey": "sk-ds-test",
				"models": [{"id": "deepseek-chat"}]
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

	names := toolNames(kernel.set(kernel.Cwd))
	searchIdx := indexOf(names, "deepseek_web_search")
	fetchIdx := indexOf(names, "deepseek_web_fetch")
	browserIdx := indexOf(names, "browser_open")

	if searchIdx < 0 || fetchIdx < 0 {
		t.Fatalf("missing deepseek web tools in %v", names)
	}
	if browserIdx < 0 {
		t.Fatalf("missing browser_open in %v", names)
	}
	if !(searchIdx < fetchIdx && fetchIdx < browserIdx) {
		t.Fatalf("unexpected tool order %v: want deepseek_web_search (%d) < deepseek_web_fetch (%d) < browser_open (%d)",
			names, searchIdx, fetchIdx, browserIdx)
	}
}

func toolNames(set []tool.Tool) []string {
	out := make([]string, len(set))
	for i, item := range set {
		out[i] = item.Name()
	}
	return out
}

func indexOf(items []string, target string) int {
	for i, item := range items {
		if item == target {
			return i
		}
	}
	return -1
}
