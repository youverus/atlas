package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"pi-go/internal/app"
	"pi-go/internal/config"
	"pi-go/internal/provider"
	"pi-go/internal/provider/faux"
	"pi-go/internal/provider/openai"
	"pi-go/internal/rpc"
	"pi-go/internal/session"
	"pi-go/internal/wire"
)

// declared builds one selection without going through a config file: these
// tests are about the merge, and a file would only add the loader's rules to
// them.
func declared(name string, model config.Model) config.Selection {
	return config.Selection{
		ProviderName: name,
		Provider:     config.Provider{API: openai.APIName, Models: []config.Model{model}},
		Model:        model,
	}
}

// TestFactsFillWhatTheDeclarationLeavesSilent is the point of the table: a
// config that names a model by id alone still gets the documented window, the
// output limit, the modalities and the narrow axis.
func TestFactsFillWhatTheDeclarationLeavesSilent(t *testing.T) {
	facts := factsOf(declared("openai", config.Model{ID: "gpt-5", Reasoning: true}))
	if facts.name != "GPT-5" {
		t.Fatalf("name = %q, want the table's display name", facts.name)
	}
	if facts.contextWindow != 400000 {
		t.Fatalf("contextWindow = %d, want 400000", facts.contextWindow)
	}
	if facts.maxTokens != 128000 {
		t.Fatalf("maxTokens = %d, want 128000", facts.maxTokens)
	}
	if got := strings.Join(facts.input, ","); got != "text,image" {
		t.Fatalf("input = %q, want text,image", got)
	}
	// The table says gpt-5 thinks in three gears, so the axis is those three
	// rather than the protocol's seven.
	if got := strings.Join(facts.gears, " "); got != "off low medium high" {
		t.Fatalf("gears = %q, want the table's axis", got)
	}
}

// TestFactsKeepWhatTheDeclarationSays: a declared value is a statement about
// this endpoint — a proxy with a smaller window is not lying about gpt-5 — so
// it wins over the table field by field.
func TestFactsKeepWhatTheDeclarationSays(t *testing.T) {
	facts := factsOf(declared("cf", config.Model{
		ID:             "gpt-5",
		Name:           "my tunnel",
		ContextWindow:  100000,
		MaxTokens:      4096,
		Reasoning:      true,
		Input:          []string{"text"},
		ThinkingLevels: []string{"low", "max"},
	}))
	if facts.name != "my tunnel" || facts.contextWindow != 100000 || facts.maxTokens != 4096 {
		t.Fatalf("facts = %+v, want the declared name and budgets", facts)
	}
	if got := strings.Join(facts.input, ","); got != "text" {
		t.Fatalf("input = %q, want the declared list", got)
	}
	// The declared subset is the only place that can describe an endpoint
	// collapsing two gears, so it wins over the table's axis too.
	if got := strings.Join(facts.gears, " "); got != "off low max" {
		t.Fatalf("gears = %q, want the declared axis", got)
	}
}

// TestFactsInventNothingForAnUnknownID keeps a local endpoint usable: an id the
// table has never seen behaves exactly as it did before the table existed.
func TestFactsInventNothingForAnUnknownID(t *testing.T) {
	facts := factsOf(declared("ollama", config.Model{ID: "llama3:8b", Reasoning: true}))
	if facts.name != "llama3:8b" {
		t.Fatalf("name = %q, want the id as the display name", facts.name)
	}
	if facts.contextWindow != 0 || facts.maxTokens != 0 || facts.input != nil {
		t.Fatalf("facts = %+v, want no metadata invented", facts)
	}
	if got := strings.Join(facts.gears, " "); got != "off low medium high xhigh max ultra" {
		t.Fatalf("gears = %q, want the whole vocabulary", got)
	}
}

// TestFactsNeverTurnReasoningOn: false is a declaration ("this endpoint does not
// think"), not a gap. Filling it from the table would send thinking fields to a
// gateway that merely reuses a vendor id.
func TestFactsNeverTurnReasoningOn(t *testing.T) {
	facts := factsOf(declared("openai", config.Model{ID: "gpt-5"}))
	if facts.gears != nil {
		t.Fatalf("gears = %v, want no axis for a declaration that does not reason", facts.gears)
	}
	// The numbers still arrive: that is what keeps compaction on for a config
	// that does not repeat the vendor's documentation.
	if facts.contextWindow != 400000 {
		t.Fatalf("contextWindow = %d, want the table's window", facts.contextWindow)
	}
}

// TestModelOfCarriesTheTableIntoTheDescriptor: the kernel descriptor is built
// from the merged facts, so the request the adapter builds and the window the
// compactor uses come from one answer.
func TestModelOfCarriesTheTableIntoTheDescriptor(t *testing.T) {
	model := modelOf(declared("openai", config.Model{ID: "gpt-5"}))
	if model.Name != "GPT-5" || model.ContextWindow != 400000 || model.MaxTokens != 128000 {
		t.Fatalf("descriptor = %+v, want the table's name and budgets", model)
	}
	if got := strings.Join(model.Input, ","); got != "text,image" {
		t.Fatalf("input = %q, want text,image", got)
	}
	if model.Provider != "openai" || model.API != openai.APIName {
		t.Fatalf("descriptor = %+v, want the declared provider and api", model)
	}
}

// TestFitThinkingRoundsToTheTablesAxis is the payoff on the -thinking path: a
// gear the vendor does not document becomes the nearest one it does, and the
// adjustment is reported.
func TestFitThinkingRoundsToTheTablesAxis(t *testing.T) {
	gpt5 := declared("openai", config.Model{ID: "gpt-5", Reasoning: true})
	cases := []struct {
		level      string
		want       string
		adjustment bool
	}{
		{"low", "low", false},
		{"medium", "medium", false},
		{"high", "high", false},
		{"off", "off", false},
		// Nothing above high is documented, so all three round down to it.
		{"xhigh", "high", true},
		{"max", "high", true},
		{"ultra", "high", true},
	}
	for _, tc := range cases {
		fitted, adjustment, err := fitThinking(gpt5, tc.level)
		if err != nil {
			t.Fatalf("fitThinking(%q): %v", tc.level, err)
		}
		if fitted != tc.want {
			t.Fatalf("fitThinking(%q) = %q, want %q", tc.level, fitted, tc.want)
		}
		if got := adjustment != ""; got != tc.adjustment {
			t.Fatalf("fitThinking(%q) note = %q, want a note: %v", tc.level, adjustment, tc.adjustment)
		}
	}
	// A model the table knows cannot think has no axis to round within, and
	// the request would carry nothing: that stays an error.
	if _, _, err := fitThinking(declared("openai", config.Model{ID: "gpt-4.1"}), "high"); err == nil {
		t.Fatal("fitThinking accepted a gear for a model that cannot think")
	}
}

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

// TestModelTableIsTheSwitchList pins the crossing: the kernel gets the
// descriptor and the axis in one row, so get_available_models and
// get_available_thinking_levels answer from one source instead of two.
func TestModelTableIsTheSwitchList(t *testing.T) {
	cfg := &config.Config{
		Providers: map[string]config.Provider{
			"openai": {API: openai.APIName, Models: []config.Model{{ID: "gpt-5", Reasoning: true}}},
			"local":  {API: openai.APIName, Models: []config.Model{{ID: "my-model"}}},
		},
	}
	table := modelTable(cfg)
	if len(table) != 2 {
		t.Fatalf("table = %+v, want every declared model", table)
	}
	// Providers sort, so a cycle walks a stable order rather than a map's.
	if table[0].Model.Provider != "local" || table[1].Model.Provider != "openai" {
		t.Fatalf("table = %+v, want the providers in sorted order", table)
	}
	if got := strings.Join(table[1].Levels, " "); got != "off low medium high" {
		t.Fatalf("levels = %q, want the model's axis", got)
	}
	// A model that cannot think still appears: it is a model to switch to, not
	// one to hide. Its empty axis is what the kernel reads as "off only".
	if table[0].Levels != nil {
		t.Fatalf("levels = %v, want none for a model that cannot think", table[0].Levels)
	}
	if table[0].Model.ContextWindow != 0 {
		t.Fatalf("descriptor = %+v, want no window for an id the table has not seen", table[0].Model)
	}
}

// TestThinkingFitterUsesTheConfigAsTheTable: a level set over RPC lands where
// the same level on the command line would, and a model the config no longer
// declares is passed through rather than given an invented limit.
func TestThinkingFitterUsesTheConfigAsTheTable(t *testing.T) {
	cfg := &config.Config{
		Providers: map[string]config.Provider{
			"openai": {API: openai.APIName, Models: []config.Model{{ID: "gpt-5", Reasoning: true}}},
		},
	}
	fit := thinkingFitter(cfg)

	fitted, adjustment, err := fit(wire.Model{ID: "gpt-5", Provider: "openai"}, "ultra")
	if err != nil {
		t.Fatalf("fit ultra: %v", err)
	}
	if fitted != "high" || adjustment == "" {
		t.Fatalf("fitted = %q note = %q, want the nearest gear and a note", fitted, adjustment)
	}

	// Nothing was read about this model, so nothing is claimed about it.
	fitted, adjustment, err = fit(wire.Model{ID: "ghost", Provider: "p"}, "ultra")
	if err != nil || fitted != "ultra" || adjustment != "" {
		t.Fatalf("fitted = %q note = %q err = %v, want the level passed through", fitted, adjustment, err)
	}
}

// TestSessionStatsUsesFactsOfWindow pins the A2 crossing between catalog's
// factsOf and rpc's get_session_stats: a model declared by id alone still
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
	table := modelTable(cfg)
	prov := faux.New()
	resolver := func(providerName, modelID string) (wire.Model, provider.Provider, error) {
		sel, err := cfg.Lookup(providerName, modelID)
		if err != nil {
			return wire.Model{}, nil, err
		}
		return modelOf(sel), prov, nil
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
		Model:        modelOf(declared("openai", config.Model{ID: "gpt-5"})),
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
