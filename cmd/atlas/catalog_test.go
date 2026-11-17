package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"pi-go/internal/app"
	"pi-go/internal/assembly"
	"pi-go/internal/config"
	"pi-go/internal/provider"
	"pi-go/internal/provider/faux"
	"pi-go/internal/provider/openai"
	"pi-go/internal/rpc"
	"pi-go/internal/session"
	"pi-go/internal/wire"
)

// TestModelListPrintsTheTablesBudgets pins the surface a reader checks first: a
// config that declares no window still lists one, because a dash there reads as
// "this model has no budget".
func TestModelListPrintsTheTablesBudgets(t *testing.T) {
	cfg := &config.Config{
		Providers: map[string]config.Provider{
			"openai": {API: openai.APIName, Models: []config.Model{{ID: "gpt-5"}}},
		},
	}
	out := &strings.Builder{}
	if err := writeModelList(out, cfg); err != nil {
		t.Fatalf("writeModelList: %v", err)
	}
	// The model column shows the display name, which the catalog supplies when
	// the config only declares an id.
	for _, want := range []string{"GPT-5", "400000", "128000"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("list = %q, want %q", out.String(), want)
		}
	}
}

// TestSessionStatsUsesFactsOfWindow pins the A2 crossing between catalog's
// FactsOf and rpc's get_session_stats: a model declared by id alone still
// carries the documented context window into the stats percentage, and
// switching models updates the window from the same table.
func TestSessionStatsUsesFactsOfWindow(t *testing.T) {
	cfg := &config.Config{
		Providers: map[string]config.Provider{
			"openai": {
				API: openai.APIName,
				Models: []config.Model{
					{ID: "gpt-5"},
					{ID: "custom", ContextWindow: 100000},
				},
			},
		},
	}
	table := assembly.ModelTable(cfg)
	prov := faux.New()
	resolver := func(providerName, modelID string) (wire.Model, provider.Provider, error) {
		sel, err := cfg.Lookup(providerName, modelID)
		if err != nil {
			return wire.Model{}, nil, err
		}
		return assembly.ModelOf(sel), prov, nil
	}

	manager, err := session.InMemory(t.TempDir(), session.Options{})
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	if _, err := manager.AppendMessage(wire.UserMessage("hello")); err != nil {
		t.Fatalf("append user: %v", err)
	}
	assistant := wire.AssistantMessage(wire.TextContent("hi"))
	assistant.Usage = &wire.Usage{Input: 1500, Output: 500, TotalTokens: 2000}
	assistant.StopReason = wire.StopStop
	if _, err := manager.AppendMessage(assistant); err != nil {
		t.Fatalf("append assistant: %v", err)
	}

	application, err := app.New(app.Config{
		Model: assembly.ModelOf(config.Selection{
			ProviderName: "openai",
			Provider:     config.Provider{API: openai.APIName, Models: []config.Model{{ID: "gpt-5"}}},
			Model:        config.Model{ID: "gpt-5"},
		}),
		Provider:     prov,
		SystemPrompt: app.StaticPrompt("base"),
		Resolver:     resolver,
		Models:       table,
	}, manager)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}

	in := strings.NewReader(
		`{"id":"1","type":"get_session_stats"}` + "\n" +
			`{"id":"2","type":"set_model","provider":"openai","modelId":"custom"}` + "\n" +
			`{"id":"3","type":"get_session_stats"}` + "\n",
	)
	var out bytes.Buffer
	if err := rpc.New(application).Serve(context.Background(), in, &out); err != nil {
		t.Fatalf("serve: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("responses = %d (%q), want 3", len(lines), out.String())
	}

	readContext := func(line string) (tokens int, window int, percent float64) {
		t.Helper()
		var resp struct {
			Success bool `json:"success"`
			Data    struct {
				ContextUsage struct {
					Tokens        int     `json:"tokens"`
					ContextWindow int     `json:"contextWindow"`
					Percent       float64 `json:"percent"`
				} `json:"contextUsage"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			t.Fatalf("decode %q: %v", line, err)
		}
		if !resp.Success {
			t.Fatalf("response failed: %s", line)
		}
		return resp.Data.ContextUsage.Tokens, resp.Data.ContextUsage.ContextWindow, resp.Data.ContextUsage.Percent
	}

	tokens1, win1, pct1 := readContext(lines[0])
	if tokens1 != 2000 || win1 != 400000 || pct1 != 0.5 {
		t.Fatalf("gpt-5 contextUsage = tokens:%d window:%d percent:%v, want 2000 / 400000 / 0.5", tokens1, win1, pct1)
	}
	tokens2, win2, pct2 := readContext(lines[2])
	if tokens2 != 2000 || win2 != 100000 || pct2 != 2.0 {
		t.Fatalf("custom contextUsage = tokens:%d window:%d percent:%v, want 2000 / 100000 / 2", tokens2, win2, pct2)
	}
}
