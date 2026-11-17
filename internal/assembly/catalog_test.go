package assembly

// The tests for what a declared model is taken to mean: the facts filled in for
// a provider that says nothing, the thinking gears the table offers, and the two
// kernel hooks that answer from the same merge.

import (
	"strings"
	"testing"

	"pi-go/internal/config"
	"pi-go/internal/provider/openai"
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
	facts := FactsOf(declared("openai", config.Model{ID: "gpt-5", Reasoning: true}))
	if facts.Name != "GPT-5" {
		t.Fatalf("name = %q, want the table's display name", facts.Name)
	}
	if facts.ContextWindow != 400000 {
		t.Fatalf("contextWindow = %d, want 400000", facts.ContextWindow)
	}
	if facts.MaxTokens != 128000 {
		t.Fatalf("maxTokens = %d, want 128000", facts.MaxTokens)
	}
	if got := strings.Join(facts.Input, ","); got != "text,image" {
		t.Fatalf("input = %q, want text,image", got)
	}
	// The table says gpt-5 thinks in three gears, so the axis is those three
	// rather than the protocol's seven.
	if got := strings.Join(facts.Gears, " "); got != "off low medium high" {
		t.Fatalf("gears = %q, want the table's axis", got)
	}
}

// TestFactsKeepWhatTheDeclarationSays: a declared value is a statement about
// this endpoint — a proxy with a smaller window is not lying about gpt-5 — so
// it wins over the table field by field.
func TestFactsKeepWhatTheDeclarationSays(t *testing.T) {
	facts := FactsOf(declared("cf", config.Model{
		ID:             "gpt-5",
		Name:           "my tunnel",
		ContextWindow:  100000,
		MaxTokens:      4096,
		Reasoning:      true,
		Input:          []string{"text"},
		ThinkingLevels: []string{"low", "max"},
	}))
	if facts.Name != "my tunnel" || facts.ContextWindow != 100000 || facts.MaxTokens != 4096 {
		t.Fatalf("facts = %+v, want the declared name and budgets", facts)
	}
	if got := strings.Join(facts.Input, ","); got != "text" {
		t.Fatalf("input = %q, want the declared list", got)
	}
	// The declared subset is the only place that can describe an endpoint
	// collapsing two gears, so it wins over the table's axis too.
	if got := strings.Join(facts.Gears, " "); got != "off low max" {
		t.Fatalf("gears = %q, want the declared axis", got)
	}
}

// TestFactsInventNothingForAnUnknownID keeps a local endpoint usable: an id the
// table has never seen behaves exactly as it did before the table existed.
func TestFactsInventNothingForAnUnknownID(t *testing.T) {
	facts := FactsOf(declared("ollama", config.Model{ID: "llama3:8b", Reasoning: true}))
	if facts.Name != "llama3:8b" {
		t.Fatalf("name = %q, want the id as the display name", facts.Name)
	}
	if facts.ContextWindow != 0 || facts.MaxTokens != 0 || facts.Input != nil {
		t.Fatalf("facts = %+v, want no metadata invented", facts)
	}
	if got := strings.Join(facts.Gears, " "); got != "off low medium high xhigh max ultra" {
		t.Fatalf("gears = %q, want the whole vocabulary", got)
	}
}

// TestFactsNeverTurnReasoningOn: false is a declaration ("this endpoint does not
// think"), not a gap. Filling it from the table would send thinking fields to a
// gateway that merely reuses a vendor id.
func TestFactsNeverTurnReasoningOn(t *testing.T) {
	facts := FactsOf(declared("openai", config.Model{ID: "gpt-5"}))
	if facts.Gears != nil {
		t.Fatalf("gears = %v, want no axis for a declaration that does not reason", facts.Gears)
	}
	// The numbers still arrive: that is what keeps compaction on for a config
	// that does not repeat the vendor's documentation.
	if facts.ContextWindow != 400000 {
		t.Fatalf("contextWindow = %d, want the table's window", facts.ContextWindow)
	}
}

// TestModelOfCarriesTheTableIntoTheDescriptor: the kernel descriptor is built
// from the merged facts, so the request the adapter builds and the window the
// compactor uses come from one answer.
func TestModelOfCarriesTheTableIntoTheDescriptor(t *testing.T) {
	model := ModelOf(declared("openai", config.Model{ID: "gpt-5"}))
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
		fitted, adjustment, err := FitThinking(gpt5, tc.level)
		if err != nil {
			t.Fatalf("FitThinking(%q): %v", tc.level, err)
		}
		if fitted != tc.want {
			t.Fatalf("FitThinking(%q) = %q, want %q", tc.level, fitted, tc.want)
		}
		if got := adjustment != ""; got != tc.adjustment {
			t.Fatalf("FitThinking(%q) note = %q, want a note: %v", tc.level, adjustment, tc.adjustment)
		}
	}
	// A model the table knows cannot think has no axis to round within, and
	// the request would carry nothing: that stays an error.
	if _, _, err := FitThinking(declared("openai", config.Model{ID: "gpt-4.1"}), "high"); err == nil {
		t.Fatal("FitThinking accepted a gear for a model that cannot think")
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
	table := ModelTable(cfg)
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
	fit := ThinkingFitter(cfg)

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
