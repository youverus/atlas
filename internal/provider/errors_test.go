package provider

import (
	"testing"

	"pi-go/internal/wire"
)

// TestClassifyText checks one message per dialect, in the wording each vendor
// actually uses. The exclusion cases matter as much as the matches: a rate limit
// read as an overflow would compact a healthy branch and retry a turn that can
// only fail again.
func TestClassifyText(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{
			name: "anthropic token overflow",
			text: `400 Bad Request: prompt is too long: 213462 tokens > 200000 maximum`,
			want: wire.ErrorCodeContextOverflow,
		},
		{
			name: "anthropic byte size overflow",
			text: `413 Request Entity Too Large: Request exceeds the maximum size`,
			want: wire.ErrorCodeContextOverflow,
		},
		{
			name: "anthropic request_too_large type",
			text: `413 Request Entity Too Large: {"type":"error","error":{"type":"request_too_large"}}`,
			want: wire.ErrorCodeContextOverflow,
		},
		{
			name: "openai context window",
			text: `400 Bad Request: Your input exceeds the context window of this model`,
			want: wire.ErrorCodeContextOverflow,
		},
		{
			name: "openai compatible proxy",
			text: `400 Bad Request: Requested token count exceeds the model's maximum context length of 131072 tokens`,
			want: wire.ErrorCodeContextOverflow,
		},
		{
			name: "openai code forwarded as text",
			text: `400 Bad Request: {"code":"context_length_exceeded"}`,
			want: wire.ErrorCodeContextOverflow,
		},
		{
			name: "google token count",
			text: `400 Bad Request: The input token count (1196265) exceeds the maximum number of tokens allowed (1048575)`,
			want: wire.ErrorCodeContextOverflow,
		},
		{
			name: "groq reduce the length",
			text: `400 Bad Request: Please reduce the length of the messages or completion`,
			want: wire.ErrorCodeContextOverflow,
		},
		{
			name: "llama cpp context size",
			text: `400 Bad Request: the request exceeds the available context size, try increasing it`,
			want: wire.ErrorCodeContextOverflow,
		},
		{
			name: "zai prompt too long",
			text: `400 Bad Request: prompt exceeds max length`,
			want: wire.ErrorCodeContextOverflow,
		},
		{
			name: "rate limit borrows the same words",
			text: `429 Too Many Requests: ThrottlingException: Too many tokens, please wait before trying again`,
		},
		{
			name: "plain rate limit",
			text: `429 Too Many Requests: rate limit reached for requests`,
		},
		{
			name: "cancellation",
			text: `Get "https://api.example.com/v1/messages": context deadline exceeded`,
		},
		{
			name: "auth failure",
			text: `401 Unauthorized: bad key`,
		},
		{
			name: "empty",
			text: ``,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ClassifyText(test.text); got != test.want {
				t.Fatalf("ClassifyText(%q) = %q, want %q", test.text, got, test.want)
			}
		})
	}
}

// TestFrameStampsTheErrorCode pins where the code is written: every adapter
// failure funnels through Frame.Error, so a code set there cannot be forgotten by
// one of the four.
func TestFrameStampsTheErrorCode(t *testing.T) {
	model := wire.Model{API: "openai-completions", Provider: "openai", ID: "gpt"}

	frame := NewFrame(model)
	event := frame.Error(wire.StopError, "400 Bad Request: prompt is too long: 10 tokens > 5 maximum")
	if event.Error == nil || event.Error.ErrorCode != wire.ErrorCodeContextOverflow {
		t.Fatalf("error code: %+v", event.Error)
	}
	if !IsContextOverflow(*event.Error) {
		t.Fatal("IsContextOverflow said no")
	}
}

// TestFrameLeavesOtherFailuresUnnamed keeps the field honest: empty means "no
// diagnosis", not "some diagnosis".
func TestFrameLeavesOtherFailuresUnnamed(t *testing.T) {
	model := wire.Model{API: "openai-completions", Provider: "openai", ID: "gpt"}

	for _, test := range []struct {
		name   string
		reason wire.StopReason
		text   string
	}{
		{name: "unclassified error", reason: wire.StopError, text: "502 Bad Gateway: bad gateway"},
		{name: "abort", reason: wire.StopAborted, text: "prompt is too long: 10 tokens > 5 maximum"},
	} {
		t.Run(test.name, func(t *testing.T) {
			frame := NewFrame(model)
			event := frame.Error(test.reason, test.text)
			if event.Error == nil {
				t.Fatal("no error message")
			}
			if event.Error.ErrorCode != "" {
				t.Fatalf("error code: got %q want none", event.Error.ErrorCode)
			}
			if IsContextOverflow(*event.Error) {
				t.Fatal("IsContextOverflow said yes")
			}
		})
	}
}
