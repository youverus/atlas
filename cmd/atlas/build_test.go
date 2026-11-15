package main

import (
	"bytes"
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
	model := modelOf(selection())
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
	if name := modelOf(choice).Name; name != "m1" {
		t.Fatalf("name = %q, want the id", name)
	}
}

func TestModelOfPrefersTheModelBaseURL(t *testing.T) {
	// A gateway that serves one model family under a different path must not
	// need a second provider entry.
	choice := selection()
	choice.Model.BaseURL = "http://override.example/v1"
	if baseURL := modelOf(choice).BaseURL; baseURL != "http://override.example/v1" {
		t.Fatalf("baseUrl = %q", baseURL)
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
	model, adapter, err := build(selection())
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
	resolve := resolverFor(cfg)

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
	notice := credentialNotice(choice)
	if !strings.Contains(notice, "TEST_API_KEY") || !strings.Contains(notice, "providers.test") {
		t.Fatalf("notice = %q", notice)
	}

	t.Setenv("TEST_API_KEY", "from-env")
	if notice := credentialNotice(choice); notice != "" {
		t.Fatalf("notice = %q, want none when the environment provides the key", notice)
	}

	// A local endpoint needs no key at all, and the notice is the only thing
	// that would say otherwise.
	choice.Provider.APIKeyEnv = ""
	if notice := credentialNotice(choice); notice == "" {
		t.Fatal("a provider without any credential must be reported")
	}
}

// TestSandboxIsOffUnlessConfigured covers the answers the wiring has to give: no
// block is no sandbox and nothing to warn about, a block unconfines nothing — the
// tool gets a real sandbox — and a block on a machine that cannot apply a profile
// is reported instead of being quietly dropped, because returning nil there would
// be the silent grant the whole package exists to avoid.
func TestSandboxIsOffUnlessConfigured(t *testing.T) {
	applied, note := sandboxNote(nil)
	if applied || note != "" {
		t.Fatalf("sandboxNote(nil) = %v, %q; want no sandbox and no note", applied, note)
	}
	if box := confine(nil, t.TempDir()); box != nil {
		t.Fatalf("confine(nil) = %v, want no confinement", box)
	}
	if tool := narrow(nil); tool != nil {
		t.Fatalf("narrow(nil) = %v, want the untyped nil the shell tool tests for", tool)
	}

	cwd := t.TempDir()
	box := confine(&config.Sandbox{}, cwd)
	if box == nil {
		t.Fatal("an empty sandbox block produced no sandbox")
	}
	// The writable root is the workspace the tools run in, not the one the
	// process started in: that is the whole reason the confinement is built per
	// directory.
	if err := box.Available(); err != nil {
		// The executor is a fact about this machine. When it is missing the
		// warning has to say so, and no sandbox may be reported as applied.
		applied, note = sandboxNote(&config.Sandbox{})
		if applied || note == "" {
			t.Fatalf("sandboxNote on a machine without an executor = %v, %q; want a warning", applied, note)
		}
		return
	}
	if applied, note := sandboxNote(&config.Sandbox{}); !applied || note != "" {
		t.Fatalf("sandboxNote = %v, %q; want the sandbox reported as in effect", applied, note)
	}
}

func TestWriteModelList(t *testing.T) {
	cfg := &config.Config{
		DefaultModel: "a/one",
		Providers: map[string]config.Provider{
			"a": {API: openai.APIName, BaseURL: "http://a.example/v1", Models: []config.Model{{ID: "one"}}},
			"b": {API: anthropic.APIName, Models: []config.Model{{ID: "two", MaxTokens: 4096}}},
		},
	}
	var out bytes.Buffer
	if err := writeModelList(&out, cfg); err != nil {
		t.Fatalf("writeModelList: %v", err)
	}
	printed := out.String()
	if !strings.Contains(printed, "one (default)") {
		t.Fatalf("output = %q, want the default marked", printed)
	}
	if !strings.Contains(printed, "two") || !strings.Contains(printed, "(adapter default)") {
		t.Fatalf("output = %q", printed)
	}
	// A zero budget is "not declared", not "no budget".
	if !strings.Contains(printed, "-") {
		t.Fatalf("output = %q, want a dash for an undeclared count", printed)
	}
}

func TestParseFlags(t *testing.T) {
	cases := []struct {
		name   string
		args   []string
		expect func(t *testing.T, opts options)
		fails  string
	}{
		{
			name: "positional prompt",
			args: []string{"fix", "the", "build"},
			expect: func(t *testing.T, opts options) {
				if opts.prompt != "fix the build" {
					t.Fatalf("prompt = %q", opts.prompt)
				}
			},
		},
		{
			name: "flag prompt",
			args: []string{"-prompt", "hi", "-model", "a/m1", "-thinking", "high"},
			expect: func(t *testing.T, opts options) {
				if opts.prompt != "hi" || opts.model != "a/m1" || opts.thinking != "high" {
					t.Fatalf("options = %+v", opts)
				}
			},
		},
		{
			name:  "prompt twice",
			args:  []string{"-prompt", "hi", "there"},
			fails: "twice",
		},
		{
			name:  "session and continue",
			args:  []string{"-session", "/tmp/s.jsonl", "-continue"},
			fails: "choose one",
		},
		{
			name:  "rpc with a prompt",
			args:  []string{"-rpc", "-prompt", "hi"},
			fails: "reads commands from stdin",
		},
		{
			name:  "demo with config flags",
			args:  []string{"-demo", "-model", "a/m1"},
			fails: "ignores",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			opts, err := parseFlags(testCase.args)
			if testCase.fails != "" {
				if err == nil || !strings.Contains(err.Error(), testCase.fails) {
					t.Fatalf("error = %v, want %q", err, testCase.fails)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseFlags: %v", err)
			}
			testCase.expect(t, opts)
		})
	}
}
