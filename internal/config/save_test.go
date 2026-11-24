package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveProvidersPreservesOtherSectionsAndRoundTrips(t *testing.T) {
	dir := t.TempDir()
	tuiPath := filepath.Join(dir, "tui.json")
	if err := os.WriteFile(tuiPath, []byte(`{"theme":"dark"}`), 0o600); err != nil {
		t.Fatalf("write tui: %v", err)
	}
	cfgPath := filepath.Join(dir, "config.json")
	original := `{
  "defaultModel": "p1/m1",
  "tui": "tui.json",
  "sandbox": {"network": true},
  "goal": {"tokenBudget": 50000},
  "providers": {
    "p1": {
      "api": "openai-completions",
      "baseUrl": "https://example.com/v1",
      "apiKey": "secret-1",
      "models": [{"id": "m1", "name": "Model One", "contextWindow": 32000}]
    }
  }
}
`
	if err := os.WriteFile(cfgPath, []byte(original), 0o600); err != nil {
		t.Fatalf("write original: %v", err)
	}

	err := SaveProviders(cfgPath, "p2/m2", "medium", map[string]Provider{
		"p2": {
			API:     "anthropic-messages",
			BaseURL: "https://api.anthropic.com/v1",
			APIKey:  "secret-2",
			Models: []Model{
				{
					ID:             "m2",
					Name:           "Model Two",
					ContextWindow:  200000,
					MaxTokens:      8192,
					Reasoning:      true,
					ThinkingLevels: []string{"low", "medium", "high"},
					Input:          []string{"text", "image"},
				},
			},
		},
	}, nil)
	if err != nil {
		t.Fatalf("SaveProviders: %v", err)
	}

	loaded, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load after save: %v", err)
	}
	if loaded.DefaultModel != "p2/m2" || loaded.ThinkingLevel != "medium" {
		t.Fatalf("defaults = (%q, %q), want (p2/m2, medium)", loaded.DefaultModel, loaded.ThinkingLevel)
	}
	if loaded.Sandbox == nil || !loaded.Sandbox.AllowsNetwork() {
		t.Fatalf("sandbox lost across SaveProviders: %+v", loaded.Sandbox)
	}
	if loaded.Goal == nil || loaded.Goal.TokenBudget != 50000 {
		t.Fatalf("goal lost across SaveProviders: %+v", loaded.Goal)
	}
	if loaded.UI.Theme != "dark" {
		t.Fatalf("tui link lost across SaveProviders: %+v", loaded.UI)
	}
	if _, hasOld := loaded.Providers["p1"]; hasOld {
		t.Fatal("deleted provider p1 still present")
	}
	p2, ok := loaded.Providers["p2"]
	if !ok || len(p2.Models) != 1 || p2.Models[0].ID != "m2" || !p2.Models[0].Reasoning {
		t.Fatalf("saved provider mismatch: %+v", p2)
	}
}

func TestSaveProvidersRejectsInvalidConfigWithoutOverwritingDisk(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	original := "{\n  \"defaultModel\": \"p1/m1\",\n  \"providers\": {\n    \"p1\": {\n      \"api\": \"openai-completions\",\n      \"apiKey\": \"k\",\n      \"models\": [{\"id\": \"m1\"}]\n    }\n  }\n}\n"
	if err := os.WriteFile(cfgPath, []byte(original), 0o600); err != nil {
		t.Fatalf("write original: %v", err)
	}

	err := SaveProviders(cfgPath, "missing/model", "", map[string]Provider{
		"p1": {
			API:    "openai-completions",
			APIKey: "k",
			Models: []Model{{ID: "m1"}},
		},
	}, nil)
	if err == nil {
		t.Fatal("expected SaveProviders to reject invalid defaultModel")
	}
	if !strings.Contains(err.Error(), "defaultModel") {
		t.Fatalf("unexpected error: %v", err)
	}

	err = SaveProviders(cfgPath, "p1/m1", "", map[string]Provider{
		"p1": {
			API:    "openai-completions",
			APIKey: "k",
			Models: []Model{{ID: "m1"}},
		},
	}, func(*Config) error {
		return errors.New("unsupported api")
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported api") {
		t.Fatalf("expected extraValidate error, got %v", err)
	}

	after, readErr := os.ReadFile(cfgPath)
	if readErr != nil {
		t.Fatalf("read after failed save: %v", readErr)
	}
	if string(after) != original {
		t.Fatalf("failed save modified config file:\ngot:\n%s\nwant:\n%s", string(after), original)
	}
}

func TestSaveProvidersTrimsAPIKeyWhitespace(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfgPath, []byte(`{"defaultModel":"deepseek/deepseek-flash","providers":{}}`), 0o600); err != nil {
		t.Fatalf("write initial: %v", err)
	}

	err := SaveProviders(cfgPath, "deepseek/deepseek-flash", "", map[string]Provider{
		"deepseek": {
			API:       "openai-completions",
			BaseURL:   " https://api.deepseek.com/v1 ",
			APIKey:    "  sk-deepseek-123\n ",
			APIKeyEnv: " DEEPSEEK_API_KEY ",
			Models:    []Model{{ID: "deepseek-flash"}},
		},
	}, nil)
	if err != nil {
		t.Fatalf("SaveProviders: %v", err)
	}

	loaded, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	ds := loaded.Providers["deepseek"]
	if ds.APIKey != "sk-deepseek-123" {
		t.Fatalf("APIKey = %q, want sk-deepseek-123", ds.APIKey)
	}
	if ds.BaseURL != "https://api.deepseek.com/v1" {
		t.Fatalf("BaseURL = %q", ds.BaseURL)
	}
	if ds.APIKeyEnv != "DEEPSEEK_API_KEY" {
		t.Fatalf("APIKeyEnv = %q", ds.APIKeyEnv)
	}
}
