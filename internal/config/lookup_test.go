package config

import (
	"strings"
	"testing"
)

func testConfig() *Config {
	return &Config{
		DefaultModel: "beta/b1",
		Providers: map[string]Provider{
			"beta": {
				API: "openai-completions",
				Models: []Model{
					{ID: "b1"},
					{ID: "shared"},
				},
			},
			"alpha": {
				API:    "anthropic-messages",
				Models: []Model{{ID: "a1"}, {ID: "shared"}},
			},
		},
	}
}

func TestResolveQualified(t *testing.T) {
	cfg := testConfig()
	selection, err := cfg.Resolve("alpha/a1")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if selection.ProviderName != "alpha" || selection.Model.ID != "a1" || selection.Name() != "alpha/a1" {
		t.Fatalf("selection = %+v", selection)
	}
}

func TestResolveBareUniqueID(t *testing.T) {
	selection, err := testConfig().Resolve("a1")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if selection.ProviderName != "alpha" {
		t.Fatalf("provider = %q", selection.ProviderName)
	}
}

func TestResolveBareIDMustBeUnique(t *testing.T) {
	// Two providers declaring one id: guessing would send the request to
	// whichever name sorted first.
	_, err := testConfig().Resolve("shared")
	if err == nil {
		t.Fatal("Resolve picked one of two providers")
	}
	if !strings.Contains(err.Error(), "use \"provider/model\"") || !strings.Contains(err.Error(), "alpha, beta") {
		t.Fatalf("error = %v", err)
	}
}

func TestResolveUnknown(t *testing.T) {
	_, err := testConfig().Resolve("nope")
	if err == nil || !strings.Contains(err.Error(), "no model") {
		t.Fatalf("error = %v", err)
	}
	_, err = testConfig().Resolve("missing/a1")
	if err == nil || !strings.Contains(err.Error(), "no provider") {
		t.Fatalf("error = %v", err)
	}
	_, err = testConfig().Resolve("alpha/nope")
	if err == nil || !strings.Contains(err.Error(), "has no model") {
		t.Fatalf("error = %v", err)
	}
}

func TestResolveSlashInModelID(t *testing.T) {
	// Gateways name models after their upstream path; a slash only means
	// "provider/model" when the prefix is a declared provider.
	cfg := &Config{Providers: map[string]Provider{
		"together": {API: "openai-completions", Models: []Model{{ID: "meta-llama/Llama-3-70B"}}},
	}}
	selection, err := cfg.Resolve("meta-llama/Llama-3-70B")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if selection.Model.ID != "meta-llama/Llama-3-70B" {
		t.Fatalf("model = %q", selection.Model.ID)
	}
}

func TestResolveFallsBackToDefaultModel(t *testing.T) {
	selection, err := testConfig().Resolve("")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if selection.Name() != "beta/b1" {
		t.Fatalf("selection = %q", selection.Name())
	}
}

func TestResolveFallsBackToTheOnlyModel(t *testing.T) {
	// The template config declares one model and no defaultModel, so a fresh
	// install must run without editing more than the credential.
	cfg := &Config{Providers: map[string]Provider{
		"solo": {API: "openai-completions", Models: []Model{{ID: "only"}}},
	}}
	selection, err := cfg.Resolve("")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if selection.Name() != "solo/only" {
		t.Fatalf("selection = %q", selection.Name())
	}
}

func TestResolveWithoutDefaultExplains(t *testing.T) {
	cfg := testConfig()
	cfg.DefaultModel = ""
	_, err := cfg.Resolve("")
	if err == nil {
		t.Fatal("Resolve invented a model")
	}
	if !strings.Contains(err.Error(), "defaultModel is empty") || !strings.Contains(err.Error(), "alpha/a1") {
		t.Fatalf("error = %v, want the declared names", err)
	}
}

func TestSelectionsAreOrdered(t *testing.T) {
	selections := testConfig().Selections()
	if len(selections) != 4 {
		t.Fatalf("len = %d", len(selections))
	}
	want := []string{"alpha/a1", "alpha/shared", "beta/b1", "beta/shared"}
	for index, selection := range selections {
		if selection.Name() != want[index] {
			t.Fatalf("selections[%d] = %q, want %q", index, selection.Name(), want[index])
		}
	}
}

func TestLookupKeepsProviderData(t *testing.T) {
	// The selection carries the whole provider because the credential and
	// endpoint live there, not on the model.
	selection, err := testConfig().Lookup("beta", "b1")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if selection.Provider.API != "openai-completions" || len(selection.Provider.Models) != 2 {
		t.Fatalf("provider = %+v", selection.Provider)
	}
}
