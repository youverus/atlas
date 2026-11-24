package assembly

// The tests for choosing a vendor and describing a model. They live here because
// this is the one package that names a concrete adapter, and the gate in
// tests/cli keeps it that way: a second package reaching for one would make
// "which endpoint answered" depend on which front end you opened.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"pi-go/internal/config"
	"pi-go/internal/provider"
	"pi-go/internal/provider/anthropic"
	"pi-go/internal/provider/openai"
	"pi-go/internal/wire"
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

func TestModelRequestTimesOutWaitingForHeaders(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-release
	}))
	defer server.Close()
	defer close(release)

	choice := selection()
	choice.Provider.BaseURL = server.URL
	choice.Provider.APIKey = ""
	adapter, err := adapterOf(choice)
	if err != nil {
		t.Fatal(err)
	}
	chat := adapter.(*openai.Provider)
	model := ModelOf(choice)
	transport := chat.Client.Transport.(*http.Transport).Clone()
	if transport.ResponseHeaderTimeout <= 0 || chat.Client.Timeout != 0 {
		t.Fatal("expected a timeout for response headers only")
	}
	transport.ResponseHeaderTimeout = 25 * time.Millisecond
	chat.Client = &http.Client{Transport: transport}
	stream, err := chat.Stream(t.Context(), provider.Request{
		Model:    model,
		Messages: []wire.Message{wire.UserMessage("hello")},
	})
	if err != nil {
		t.Fatal(err)
	}
	message, _, err := provider.Collect(stream)
	if err != nil {
		t.Fatal(err)
	}
	if message.StopReason != wire.StopError || !strings.Contains(message.ErrorMessage, "timeout awaiting response headers") {
		t.Fatalf("expected a model timeout, got %s: %s", message.StopReason, message.ErrorMessage)
	}
}

func TestModelStreamIdleTimeoutAfterHeaders(t *testing.T) {
	for _, test := range []struct {
		name     string
		text     string
		userStop bool
	}{
		{name: "headers and heartbeats only"},
		{name: "stalls after partial reply", text: "partial reply"},
		{name: "user stops the stream", userStop: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			disconnected := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(disconnected)
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				if test.text != "" {
					fmt.Fprintf(w, "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":%q}}\n\n", test.text)
				}
				flusher := w.(http.Flusher)
				flusher.Flush()
				heartbeats := time.NewTicker(10 * time.Millisecond)
				defer heartbeats.Stop()
				for {
					select {
					case <-r.Context().Done():
						return
					case <-heartbeats.C:
						fmt.Fprint(w, "event: ping\ndata: {\"type\":\"ping\"}\n\n")
						flusher.Flush()
					}
				}
			}))
			defer server.Close()

			choice := selection()
			choice.Provider.API = anthropic.APIName
			choice.Provider.BaseURL = server.URL
			adapter, err := adapterOf(choice)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			stream, err := provider.WithStreamIdleTimeout(adapter, 100*time.Millisecond).Stream(ctx, provider.Request{
				Model:    ModelOf(choice),
				Messages: []wire.Message{wire.UserMessage("hello")},
			})
			if err != nil {
				t.Fatal(err)
			}
			if test.userStop {
				cancel()
			}
			message, _, err := provider.Collect(stream)
			if err != nil {
				t.Fatal(err)
			}
			if test.userStop {
				if message.StopReason != wire.StopAborted || message.ErrorCode != "" {
					t.Fatalf("user cancellation = %+v", message)
				}
			} else if ctx.Err() != nil || message.StopReason != wire.StopError || message.ErrorCode != wire.ErrorCodeStreamIdleTimeout {
				t.Fatalf("expected a stream idle timeout, got %+v", message)
			}
			if message.Text() != test.text {
				t.Fatalf("reply = %q, want %q", message.Text(), test.text)
			}
			select {
			case <-disconnected:
			case <-time.After(2 * time.Second):
				t.Fatal("model HTTP request remained open")
			}
		})
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

func TestEffectiveBaseURLNormalizesAnthropicCompatibleEndpoints(t *testing.T) {
	cases := []struct {
		name    string
		api     string
		provURL string
		modURL  string
		want    string
	}{
		{
			name:    "bare anthropic host adds /v1",
			api:     anthropic.APIName,
			provURL: "https://api.anthropic.com",
			want:    "https://api.anthropic.com/v1",
		},
		{
			name:    "local proxy without /v1 adds /v1",
			api:     anthropic.APIName,
			provURL: "http://localhost:4000",
			want:    "http://localhost:4000/v1",
		},
		{
			name:    "third party /anthropic SDK url adds /v1",
			api:     anthropic.APIName,
			provURL: "https://open.bigmodel.cn/api/anthropic",
			want:    "https://open.bigmodel.cn/api/anthropic/v1",
		},
		{
			name:    "model override ending in /messages strips suffix",
			api:     anthropic.APIName,
			provURL: "https://ignored.example.com/v1",
			modURL:  "https://dashscope.aliyuncs.com/apps/anthropic/v1/messages",
			want:    "https://dashscope.aliyuncs.com/apps/anthropic/v1",
		},
		{
			name:    "openai bare path stays unchanged",
			api:     openai.APIName,
			provURL: "https://relay.example.com",
			want:    "https://relay.example.com",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			choice := selection()
			choice.Provider.API = tc.api
			choice.Provider.BaseURL = tc.provURL
			choice.Model.BaseURL = tc.modURL
			if got := EffectiveBaseURL(choice); got != tc.want {
				t.Fatalf("EffectiveBaseURL = %q, want %q", got, tc.want)
			}
			if got := ModelOf(choice).BaseURL; got != tc.want {
				t.Fatalf("ModelOf.BaseURL = %q, want %q", got, tc.want)
			}
		})
	}
}
