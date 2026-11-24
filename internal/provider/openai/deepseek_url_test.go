package openai_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"pi-go/internal/provider"
	"pi-go/internal/provider/openai"
	"pi-go/internal/wire"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestDeepSeekOpenAIEndpointNormalization(t *testing.T) {
	cases := []struct {
		name         string
		modelBaseURL string
		compatURL    string
		wantURL      string
	}{
		{
			name:         "official /v1 stays /v1/chat/completions",
			modelBaseURL: "https://api.deepseek.com/v1",
			wantURL:      "https://api.deepseek.com/v1/chat/completions",
		},
		{
			name:         "bare host stays /chat/completions",
			modelBaseURL: "https://api.deepseek.com",
			wantURL:      "https://api.deepseek.com/chat/completions",
		},
		{
			name:         "copied /anthropic rewrites to /v1/chat/completions",
			modelBaseURL: "https://api.deepseek.com/anthropic",
			wantURL:      "https://api.deepseek.com/v1/chat/completions",
		},
		{
			name:         "copied /anthropic/v1 rewrites to /v1/chat/completions",
			modelBaseURL: "https://api.deepseek.com/anthropic/v1/",
			wantURL:      "https://api.deepseek.com/v1/chat/completions",
		},
		{
			name:      "compat /anthropic normalizes when model base url empty",
			compatURL: "https://api.deepseek.com/anthropic",
			wantURL:   "https://api.deepseek.com/v1/chat/completions",
		},
		{
			name:         "non-deepseek host keeps /anthropic path",
			modelBaseURL: "https://proxy.example.com/anthropic",
			wantURL:      "https://proxy.example.com/anthropic/chat/completions",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotURL, gotAuth string
			var gotBody map[string]any

			p := openai.New(openai.Compat{
				BaseURL:                 tc.compatURL,
				ThinkingDialect:         "deepseek",
				RequireReasoningContent: true,
			})
			p.Client = &http.Client{
				Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					gotURL = r.URL.String()
					gotAuth = r.Header.Get("Authorization")
					raw, err := io.ReadAll(r.Body)
					if err != nil {
						t.Fatalf("read request body: %v", err)
					}
					if err := json.Unmarshal(raw, &gotBody); err != nil {
						t.Fatalf("decode request body: %v", err)
					}
					return &http.Response{
						StatusCode: http.StatusBadRequest,
						Header:     make(http.Header),
						Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"stub"}}`)),
					}, nil
				}),
			}

			req := provider.Request{
				Model: wire.Model{
					ID:        "deepseek-flash",
					API:       openai.APIName,
					Provider:  "deepseek",
					BaseURL:   tc.modelBaseURL,
					Reasoning: true,
					MaxTokens: 8192,
				},
				Messages: []wire.Message{wire.UserMessage("hi")},
				Options: provider.Options{
					APIKey:        "sk-deepseek-test",
					ThinkingLevel: "high",
				},
			}
			stream, err := p.Stream(context.Background(), req)
			if err != nil {
				t.Fatalf("Stream: %v", err)
			}
			if _, _, err := provider.Collect(stream); err != nil {
				t.Fatalf("Collect: %v", err)
			}

			if gotURL != tc.wantURL {
				t.Fatalf("endpoint URL: got %q, want %q", gotURL, tc.wantURL)
			}
			if gotAuth != "Bearer sk-deepseek-test" {
				t.Fatalf("Authorization: got %q, want %q", gotAuth, "Bearer sk-deepseek-test")
			}
			if _, ok := gotBody["thinking"]; !ok {
				t.Fatalf("expected deepseek thinking field in payload: %v", gotBody)
			}
		})
	}
}
