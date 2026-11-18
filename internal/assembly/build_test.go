package assembly

// The tests for choosing a vendor and describing a model. They live here because
// this is the one package that names a concrete adapter, and the gate in
// tests/cli keeps it that way: a second package reaching for one would make
// "which endpoint answered" depend on which front end you opened.

import (
	"strings"
	"testing"

	"pi-go/internal/config"
	"pi-go/internal/provider/anthropic"
	"pi-go/internal/provider/openai"
)

func selection() config.Selection {
	return config.Selection{
		ProviderName: "test",
		Provider: config.Provider{
			API:       openai.APIName,
			BaseURL:   "http://provider.example/v1",
			APIKey:    "key",
			APIKeyEnv: "",
			Models:    []config.Model{{ID: "m1", Name: "Model One", ContextWindow: 32000, MaxTokens: 1024, Reasoning: true, Input: []string{"text"}}},
		},
		Model: config.Model{ID: "m1", Name: "Model One", ContextWindow: 32000, MaxTokens: 1024, Reasoning: true, Input: []string{"text"}},
	}
}

func TestModelOfCopiesTheDeclaration(t *testing.T) {
	model := ModelOf(selection())
	if model.ID != "m1" || model.Name != "Model One" || model.API != openai.APIName || model.Provider != "test" {
		t.Fatalf("model = %+v", model)
	}
	if model.BaseURL != "http://provider.example/v1" || model.ContextWindow != 32000 || model.MaxTokens != 1024 || !model.Reasoning {
		t.Fatalf("model = %+v", model)
	}
	if len(model.Input) != 1 || model.Input[0] != "text" {
		t.Fatalf("input = %v", model.Input)
	}
}

func TestModelOfFallsBackToTheIDAsName(t *testing.T) {
	choice := selection()
	choice.Model.Name = ""
	if name := ModelOf(choice).Name; name != "m1" {
		t.Fatalf("name = %q, want the id", name)
	}
}

func TestModelOfPrefersTheModelBaseURL(t *testing.T) {
	// A gateway that serves one model family under a different path must not
	// need a second provider entry.
	choice := selection()
	choice.Model.BaseURL = "http://override.example/v1"
	if baseURL := ModelOf(choice).BaseURL; baseURL != "http://override.example/v1" {
		t.Fatalf("baseUrl = %q", baseURL)
	}
}

// TestModelOfFoldsTheUserAgent covers the one endpoint that serves several
// vendors under a single provider: the provider answers what the endpoint is
// called, and a model declaration answers what its client is called.
func TestModelOfFoldsTheUserAgent(t *testing.T) {
	choice := selection()
	if ua := ModelOf(choice).UserAgent; ua != "" {
		t.Fatalf("userAgent with nothing declared = %q; want empty", ua)
	}

	choice.Provider.UserAgent = "endpoint/1.0"
	if ua := ModelOf(choice).UserAgent; ua != "endpoint/1.0" {
		t.Fatalf("provider userAgent = %q", ua)
	}

	choice.Model.UserAgent = "vendor/2.0"
	if ua := ModelOf(choice).UserAgent; ua != "vendor/2.0" {
		t.Fatalf("model userAgent = %q; want the model's override", ua)
	}
}

func TestAdapterOfRoutesByAPI(t *testing.T) {
	openaiSelection := selection()
	adapter, err := adapterOf(openaiSelection)
	if err != nil {
		t.Fatalf("adapterOf: %v", err)
	}
	if adapter.API() != openai.APIName {
		t.Fatalf("api = %q", adapter.API())
	}

	anthropicSelection := selection()
	anthropicSelection.Provider.API = anthropic.APIName
	adapter, err = adapterOf(anthropicSelection)
	if err != nil {
		t.Fatalf("adapterOf: %v", err)
	}
	if adapter.API() != anthropic.APIName {
		t.Fatalf("api = %q", adapter.API())
	}

	unknown := selection()
	unknown.Provider.API = "gemini-generate-content"
	_, err = adapterOf(unknown)
	if err == nil || !strings.Contains(err.Error(), "gemini-generate-content") {
		t.Fatalf("error = %v, want it to name the unsupported api", err)
	}
}

func TestBuildReturnsBothHalves(t *testing.T) {
	model, adapter, err := Build(selection())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if model.Provider != "test" || adapter.API() != openai.APIName {
		t.Fatalf("model = %+v, api = %q", model, adapter.API())
	}
}

// TestAdapterOfPassesTheThinkingDialect pins the wiring: the dialect is the
// only thing that tells an adapter to send a vendor's thinking fields, and a
// field dropped here would disable it without any error.
func TestAdapterOfPassesTheThinkingDialect(t *testing.T) {
	// An empty credential leaves the adapter unwrapped, which is what lets the
	// test look at the value it received.
	openaiChoice := selection()
	openaiChoice.Provider.APIKey = ""
	openaiChoice.Provider.Compat.ThinkingDialect = "deepseek"
	adapter, err := adapterOf(openaiChoice)
	if err != nil {
		t.Fatalf("adapterOf: %v", err)
	}
	chat, ok := adapter.(*openai.Provider)
	if !ok {
		t.Fatalf("adapter = %T, want the chat completions provider", adapter)
	}
	if chat.Compat.ThinkingDialect != "deepseek" {
		t.Fatalf("openai compat = %+v", chat.Compat)
	}

	anthropicChoice := openaiChoice
	anthropicChoice.Provider.API = anthropic.APIName
	adapter, err = adapterOf(anthropicChoice)
	if err != nil {
		t.Fatalf("adapterOf: %v", err)
	}
	messages, ok := adapter.(*anthropic.Provider)
	if !ok {
		t.Fatalf("adapter = %T, want the messages provider", adapter)
	}
	if messages.Compat.ThinkingDialect != "deepseek" {
		t.Fatalf("anthropic compat = %+v", messages.Compat)
	}
}

func TestResolverLooksUpAndBuilds(t *testing.T) {
	cfg := &config.Config{Providers: map[string]config.Provider{
		"test": selection().Provider,
	}}
	resolve := ResolverFor(cfg)

	model, adapter, err := resolve("test", "m1")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if model.ID != "m1" || adapter.API() != openai.APIName {
		t.Fatalf("model = %+v, api = %q", model, adapter.API())
	}

	if _, _, err := resolve("test", "missing"); err == nil {
		t.Fatal("resolve accepted an undeclared model")
	}
	if _, _, err := resolve("nope", "m1"); err == nil {
		t.Fatal("resolve accepted an undeclared provider")
	}
}

func TestCredentialNotice(t *testing.T) {
	choice := selection()
	choice.Provider.APIKey = ""
	choice.Provider.APIKeyEnv = "TEST_API_KEY"
	t.Setenv("TEST_API_KEY", "")
	notice := CredentialNotice(choice)
	if !strings.Contains(notice, "TEST_API_KEY") || !strings.Contains(notice, "providers.test") {
		t.Fatalf("notice = %q", notice)
	}

	t.Setenv("TEST_API_KEY", "from-env")
	if notice := CredentialNotice(choice); notice != "" {
		t.Fatalf("notice = %q, want none when the environment provides the key", notice)
	}

	// A local endpoint needs no key at all, and the notice is the only thing
	// that would say otherwise.
	choice.Provider.APIKeyEnv = ""
	if notice := CredentialNotice(choice); notice == "" {
		t.Fatal("a provider without any credential must be reported")
	}
}
