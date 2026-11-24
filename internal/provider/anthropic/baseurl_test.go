package anthropic_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"pi-go/internal/provider"
	"pi-go/internal/provider/anthropic"
	"pi-go/internal/wire"
)

func TestAnthropicEndpointNormalizationAcrossHosts(t *testing.T) {
	cases := []struct {
		name         string
		modelBaseURL string
		compatURL    string
		wantURL      string
	}{
		{
			name:         "official bare anthropic host appends /v1/messages",
			modelBaseURL: "https://api.anthropic.com",
			wantURL:      "https://api.anthropic.com/v1/messages",
		},
		{
			name:         "generic relay bare host appends /v1/messages",
			modelBaseURL: "https://claude-relay.example.com/",
			wantURL:      "https://claude-relay.example.com/v1/messages",
		},
		{
			name:         "minimax /anthropic appends /v1/messages",
			modelBaseURL: "https://api.minimaxi.com/anthropic",
			wantURL:      "https://api.minimaxi.com/anthropic/v1/messages",
		},
		{
			name:         "zhipu /api/anthropic appends /v1/messages",
			modelBaseURL: "https://open.bigmodel.cn/api/anthropic/",
			wantURL:      "https://open.bigmodel.cn/api/anthropic/v1/messages",
		},
		{
			name:         "dashscope /apps/anthropic appends /v1/messages",
			modelBaseURL: "https://dashscope.aliyuncs.com/apps/anthropic",
			wantURL:      "https://dashscope.aliyuncs.com/apps/anthropic/v1/messages",
		},
		{
			name:         "kimi /coding appends /v1/messages",
			modelBaseURL: "https://api.kimi.com/coding",
			wantURL:      "https://api.kimi.com/coding/v1/messages",
		},
		{
			name:         "openrouter /api appends /v1/messages",
			modelBaseURL: "https://openrouter.ai/api",
			wantURL:      "https://openrouter.ai/api/v1/messages",
		},
		{
			name:         "cloudflare ai gateway /anthropic appends /v1/messages",
			modelBaseURL: "https://gateway.ai.cloudflare.com/v1/acc/gw/anthropic",
			wantURL:      "https://gateway.ai.cloudflare.com/v1/acc/gw/anthropic/v1/messages",
		},
		{
			name:         "full /v1/messages URL strips duplicate /messages",
			modelBaseURL: "https://relay.example.com/v1/messages/",
			wantURL:      "https://relay.example.com/v1/messages",
		},
		{
			name:         "full /anthropic/messages URL resolves to /anthropic/v1/messages",
			modelBaseURL: "https://api.moonshot.cn/anthropic/messages",
			wantURL:      "https://api.moonshot.cn/anthropic/v1/messages",
		},
		{
			name:      "compat URL normalized when model BaseURL empty",
			compatURL: " https://api.z.ai/api/anthropic ",
			wantURL:   "https://api.z.ai/api/anthropic/v1/messages",
		},
		{
			name:         "versioned /v2 path preserved without adding /v1",
			modelBaseURL: "https://custom.example.com/v2",
			wantURL:      "https://custom.example.com/v2/messages",
		},
		{
			name:         "custom non-SDK subpath preserved",
			modelBaseURL: "https://proxy.example.com/custom-claude",
			wantURL:      "https://proxy.example.com/custom-claude/messages",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotURL string
			p := anthropic.New(anthropic.Compat{BaseURL: tc.compatURL})
			p.Client = &http.Client{
				Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					gotURL = r.URL.String()
					return &http.Response{
						StatusCode: http.StatusBadRequest,
						Header:     make(http.Header),
						Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"stub"}}`)),
					}, nil
				}),
			}

			req := provider.Request{
				Model: wire.Model{
					ID:        "claude-sonnet-4-5",
					API:       anthropic.APIName,
					Provider:  "custom",
					BaseURL:   tc.modelBaseURL,
					MaxTokens: 8192,
				},
				Messages: []wire.Message{wire.UserMessage("hi")},
				Options:  provider.Options{APIKey: "sk-test"},
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
		})
	}
}

func TestNormalizeConfiguredBaseURLHandlesLoopback(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{raw: "http://localhost:4000", want: "http://localhost:4000/v1"},
		{raw: "http://127.0.0.1:4000/", want: "http://127.0.0.1:4000/v1"},
		{raw: "http://[::1]:4000", want: "http://[::1]:4000/v1"},
		{raw: "http://localhost:4000/anthropic", want: "http://localhost:4000/anthropic/v1"},
		{raw: "http://localhost:4000/v1/messages", want: "http://localhost:4000/v1"},
		{raw: "", want: ""},
	}
	for _, tc := range cases {
		if got := anthropic.NormalizeConfiguredBaseURL(tc.raw); got != tc.want {
			t.Fatalf("NormalizeConfiguredBaseURL(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}
