package anthropic_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"pi-go/internal/provider"
	"pi-go/internal/provider/anthropic"
	"pi-go/internal/wire"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestDeepSeekAnthropicEndpointNormalization(t *testing.T) {
	cases := []struct {
		name         string
		modelBaseURL string
		compatURL    string
		wantURL      string
	}{
		{
			name:         "model base url /v1 rewrites to /anthropic/v1/messages",
			modelBaseURL: "https://api.deepseek.com/v1",
			wantURL:      "https://api.deepseek.com/anthropic/v1/messages",
		},
		{
			name:         "model base url bare host rewrites to /anthropic/v1/messages",
			modelBaseURL: "https://api.deepseek.com",
			wantURL:      "https://api.deepseek.com/anthropic/v1/messages",
		},
		{
			name:         "official anthropic sdk base url rewrites to /anthropic/v1/messages",
			modelBaseURL: "https://api.deepseek.com/anthropic/",
			wantURL:      "https://api.deepseek.com/anthropic/v1/messages",
		},
		{
			name:         "already normalized /anthropic/v1 stays unchanged",
			modelBaseURL: "https://api.deepseek.com/anthropic/v1",
			wantURL:      "https://api.deepseek.com/anthropic/v1/messages",
		},
		{
			name:      "compat base url normalized when model base url empty",
			compatURL: "https://API.DEEPSEEK.COM/v1/",
			wantURL:   "https://API.DEEPSEEK.COM/anthropic/v1/messages",
		},
		{
			name:         "third party proxy base url stays untouched",
			modelBaseURL: "https://proxy.example.com/v1",
			wantURL:      "https://proxy.example.com/v1/messages",
		},
		{
			name:    "empty base urls keep official anthropic default",
			wantURL: "https://api.anthropic.com/v1/messages",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotURL, gotKey string
			var gotBody map[string]any

			p := anthropic.New(anthropic.Compat{
				BaseURL:         tc.compatURL,
				ThinkingDialect: "deepseek",
			})
			p.Client = &http.Client{
				Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					gotURL = r.URL.String()
					gotKey = r.Header.Get("x-api-key")
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
					API:       anthropic.APIName,
					Provider:  "deepseek",
					BaseURL:   tc.modelBaseURL,
					Reasoning: true,
					MaxTokens: 8192,
				},
				Messages: []wire.Message{wire.UserMessage("hi")},
				Options: provider.Options{
					APIKey:        "sk-deepseek-test",
					ThinkingLevel: "max",
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
			if gotKey != "sk-deepseek-test" {
				t.Fatalf("x-api-key: got %q, want %q", gotKey, "sk-deepseek-test")
			}
			if gotBody["model"] != "deepseek-flash" {
				t.Fatalf("model: got %v, want deepseek-flash", gotBody["model"])
			}
		})
	}
}
