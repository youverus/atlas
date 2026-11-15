package openai

import (
	"testing"

	"pi-go/internal/wire"
)

// maxTokensField picks the output-limit field from the host, an unexported
// decision that an httptest server cannot reach because the api.openai.com
// branch needs that exact hostname. The black-box tests cover the override and
// the compatible-host default; this table covers the rest.
func TestMaxTokensFieldByHost(t *testing.T) {
	tests := []struct {
		name        string
		compatField string
		baseURL     string
		want        string
	}{
		{
			name: "no base url keeps the openai default",
			want: "max_completion_tokens",
		},
		{
			name:    "openai host",
			baseURL: "https://api.openai.com/v1",
			want:    "max_completion_tokens",
		},
		{
			name:    "openai host with an explicit port",
			baseURL: "https://api.openai.com:443/v1",
			want:    "max_completion_tokens",
		},
		{
			name:    "compatible host",
			baseURL: "https://api.deepseek.com/v1",
			want:    "max_tokens",
		},
		{
			// A gateway that proxies OpenAI under a path must keep max_tokens:
			// only the hostname decides.
			name:    "path that mentions openai",
			baseURL: "https://proxy.example.com/api.openai.com",
			want:    "max_tokens",
		},
		{
			name:    "base url without a scheme",
			baseURL: "api.openai.com/v1",
			want:    "max_tokens",
		},
		{
			name:        "override on the openai host",
			compatField: "max_tokens",
			baseURL:     "https://api.openai.com/v1",
			want:        "max_tokens",
		},
		{
			name:        "override on a compatible host",
			compatField: "max_completion_tokens",
			baseURL:     "https://api.deepseek.com/v1",
			want:        "max_completion_tokens",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			p := New(Compat{MaxTokensField: test.compatField})
			if got := p.maxTokensField(wire.Model{BaseURL: test.baseURL}); got != test.want {
				t.Fatalf("field: got %q want %q", got, test.want)
			}
		})
	}
}
