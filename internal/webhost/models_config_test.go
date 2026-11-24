package webhost

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"pi-go/internal/assembly"
	"pi-go/internal/config"
	"pi-go/internal/httpapi"
	"pi-go/internal/wire"
)

func TestHostModelConfigSaveOverwritesAndReloads(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ATLAS_HOME", home)
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	initial := `{
		"defaultModel": "p1/m1",
		"sandbox": {"network": true},
		"providers": {
			"p1": {
				"api": "openai-completions",
				"apiKey": "k1",
				"models": [{"id": "m1", "name": "One", "contextWindow": 8000}]
			}
		}
	}`
	if err := os.WriteFile(cfgPath, []byte(initial), 0o600); err != nil {
		t.Fatalf("write initial config: %v", err)
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
	got, err := host.ModelConfig()
	if err != nil {
		t.Fatalf("ModelConfig: %v", err)
	}
	if got.ConfigPath != cfgPath || got.DefaultModel != "p1/m1" || len(got.Providers) != 1 {
		t.Fatalf("unexpected ModelConfig: %+v", got)
	}

	// Reject unsupported adapter API without modifying config.json on disk.
	err = host.SaveModelConfig(httpapi.ModelConfig{
		DefaultModel: "p1/m1",
		Providers: []httpapi.ProviderConfig{
			{
				Name:   "p1",
				API:    "not-an-adapter",
				APIKey: "k1",
				Models: []httpapi.ModelItemConfig{{ID: "m1"}},
			},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "not-an-adapter") {
		t.Fatalf("expected unsupported api rejection, got %v", err)
	}

	// Add a second provider & model, delete m1, switch defaultModel, save & reload.
	err = host.SaveModelConfig(httpapi.ModelConfig{
		DefaultModel: "p2/claude-test",
		Providers: []httpapi.ProviderConfig{
			{
				Name:    "p2",
				API:     "anthropic-messages",
				BaseURL: "https://api.anthropic.com/v1",
				APIKey:  "k2",
				Models: []httpapi.ModelItemConfig{
					{
						ID:            "claude-test",
						Name:          "Claude Test",
						ContextWindow: 200000,
						MaxTokens:     8192,
						Reasoning:     true,
						Input:         []string{"text", "image"},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("SaveModelConfig: %v", err)
	}
	if gotModel := host.SessionModel(); gotModel != "p2/claude-test" {
		t.Fatalf("SessionModel after SaveModelConfig = %q, want p2/claude-test", gotModel)
	}
	list, err := host.Models()
	if err != nil {
		t.Fatalf("Models after SaveModelConfig: %v", err)
	}
	if list.Current != "p2/claude-test" || len(list.Models) != 1 || list.Models[0].ID != "claude-test" {
		t.Fatalf("Models mismatch after SaveModelConfig: %+v", list)
	}
}

func TestDeepSeekConfigSaveReloadAndAskEndToEnd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ATLAS_HOME", home)
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	initial := `{
		"defaultModel": "deepseek/deepseek-flash",
		"thinkingLevel": "high",
		"sandbox": {"network": true},
		"providers": {
			"deepseek": {
				"api": "openai-completions",
				"baseUrl": "https://api.deepseek.com/v1",
				"apiKeyEnv": "DEEPSEEK_API_KEY",
				"compat": {"thinkingDialect": "deepseek", "requireReasoningContent": true},
				"models": [{"id": "deepseek-flash", "name": "DeepSeek Flash", "reasoning": true, "contextWindow": 128000, "maxTokens": 8192}]
			}
		}
	}`
	if err := os.WriteFile(cfgPath, []byte(initial), 0o600); err != nil {
		t.Fatalf("write initial config: %v", err)
	}

	type recordedReq struct {
		path   string
		header http.Header
		body   map[string]any
	}
	var mu sync.Mutex
	var recorded []recordedReq
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read upstream request: %v", err)
		}
		var payload map[string]any
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Fatalf("unmarshal upstream body: %v", err)
		}
		mu.Lock()
		recorded = append(recorded, recordedReq{
			path:   r.URL.Path,
			header: r.Header.Clone(),
			body:   payload,
		})
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		switch r.URL.Path {
		case "/v1/chat/completions":
			io.WriteString(w, "data: {\"id\":\"1\",\"choices\":[{\"delta\":{\"content\":\"openai ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		case "/anthropic/v1/messages":
			io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m1\",\"usage\":{\"input_tokens\":1}}}\n\n"+
				"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"+
				"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"anthropic ok\"}}\n\n"+
				"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"+
				"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n"+
				"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		default:
			http.Error(w, "unexpected path: "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer upstream.Close()

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

	// 1. Report whether apiKeyEnv is currently active without leaking its value.
	t.Setenv("DEEPSEEK_API_KEY", "  sk-env-secret  ")
	cfgView, err := host.ModelConfig()
	if err != nil {
		t.Fatalf("ModelConfig (env set): %v", err)
	}
	if len(cfgView.Providers) != 1 || !cfgView.Providers[0].APIKeyEnvSet || cfgView.Providers[0].APIKey != "" {
		t.Fatalf("unexpected ModelConfig with env set: %+v", cfgView.Providers)
	}
	t.Setenv("DEEPSEEK_API_KEY", "   ")
	cfgView, err = host.ModelConfig()
	if err != nil {
		t.Fatalf("ModelConfig (blank env): %v", err)
	}
	if cfgView.Providers[0].APIKeyEnvSet {
		t.Fatalf("whitespace-only env must report APIKeyEnvSet=false: %+v", cfgView.Providers[0])
	}

	// 2. Save padded API key for DeepSeek OpenAI-compatible mode, run main + side asks.
	err = host.SaveModelConfig(httpapi.ModelConfig{
		DefaultModel:  "deepseek/deepseek-flash",
		ThinkingLevel: "high",
		Providers: []httpapi.ProviderConfig{
			{
				Name:      "deepseek",
				API:       "openai-completions",
				BaseURL:   upstream.URL + "/v1",
				APIKey:    "  sk-gui-openai\n ",
				APIKeyEnv: "DEEPSEEK_API_KEY",
				Compat: &httpapi.CompatConfig{
					ThinkingDialect:         "deepseek",
					RequireReasoningContent: true,
				},
				Models: []httpapi.ModelItemConfig{
					{
						ID:            "deepseek-flash",
						Name:          "DeepSeek Flash",
						Reasoning:     true,
						ContextWindow: 128000,
						MaxTokens:     8192,
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("SaveModelConfig (openai): %v", err)
	}

	loaded, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if got := loaded.Providers["deepseek"].APIKey; got != "sk-gui-openai" {
		t.Fatalf("persisted APIKey = %q, want trimmed sk-gui-openai", got)
	}

	ctx := context.Background()
	if _, err := host.SessionAsk(ctx, "", "hello openai", func(wire.AgentEvent) error { return nil }); err != nil {
		t.Fatalf("SessionAsk (openai): %v", err)
	}
	if _, err := host.activeSlot().live.App.BeginSide(); err != nil {
		t.Fatalf("BeginSide: %v", err)
	}
	defer host.activeSlot().live.App.EndSide()
	if _, err := host.SessionSideAsk(ctx, "", "side openai", func(wire.AgentEvent) error { return nil }); err != nil {
		t.Fatalf("SessionSideAsk (openai): %v", err)
	}

	// 3. Switch protocol to anthropic-messages + new key and verify live main and side reload.
	err = host.SaveModelConfig(httpapi.ModelConfig{
		DefaultModel:  "deepseek/deepseek-flash",
		ThinkingLevel: "max",
		Providers: []httpapi.ProviderConfig{
			{
				Name:      "deepseek",
				API:       "anthropic-messages",
				BaseURL:   upstream.URL + "/anthropic/v1",
				APIKey:    "sk-gui-anthropic",
				APIKeyEnv: "DEEPSEEK_API_KEY",
				Compat: &httpapi.CompatConfig{
					ThinkingDialect:         "deepseek",
					RequireReasoningContent: true,
				},
				Models: []httpapi.ModelItemConfig{
					{
						ID:            "deepseek-flash",
						Name:          "DeepSeek Flash",
						Reasoning:     true,
						ContextWindow: 128000,
						MaxTokens:     8192,
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("SaveModelConfig (anthropic): %v", err)
	}

	if _, err := host.SessionAsk(ctx, "", "hello anthropic", func(wire.AgentEvent) error { return nil }); err != nil {
		t.Fatalf("SessionAsk (anthropic): %v", err)
	}
	if _, err := host.SessionSideAsk(ctx, "", "side anthropic", func(wire.AgentEvent) error { return nil }); err != nil {
		t.Fatalf("SessionSideAsk (anthropic): %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(recorded) != 4 {
		t.Fatalf("recorded upstream calls = %d, want 4", len(recorded))
	}
	for i := 0; i < 2; i++ {
		if recorded[i].path != "/v1/chat/completions" {
			t.Fatalf("call %d path = %q, want /v1/chat/completions", i, recorded[i].path)
		}
		if got := recorded[i].header.Get("Authorization"); got != "Bearer sk-gui-openai" {
			t.Fatalf("call %d Authorization = %q, want Bearer sk-gui-openai", i, got)
		}
	}
	for i := 2; i < 4; i++ {
		if recorded[i].path != "/anthropic/v1/messages" {
			t.Fatalf("call %d path = %q, want /anthropic/v1/messages", i, recorded[i].path)
		}
		if got := recorded[i].header.Get("x-api-key"); got != "sk-gui-anthropic" {
			t.Fatalf("call %d x-api-key = %q, want sk-gui-anthropic", i, got)
		}
	}
}
