package compact_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"pi-go/internal/compact"
	"pi-go/internal/provider"
	"pi-go/internal/provider/faux"
	"pi-go/internal/wire"
)

func TestSummarizeHistorySendsTheInitialPrompt(t *testing.T) {
	script := faux.New(faux.Response{
		Text:  "  ## Goal\nship it  ",
		Usage: wire.Usage{Input: 100, Output: 20, TotalTokens: 120},
	})
	summarizer := &compact.Summarizer{Provider: script, Model: wire.Model{ID: "model", MaxTokens: 4096}}

	text, usage, err := summarizer.SummarizeHistory(context.Background(), []wire.Message{
		wire.UserMessage("hello"),
		wire.AssistantMessage(wire.TextContent("hi")),
	}, compact.DefaultSettings, "", "")
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}
	if text != "## Goal\nship it" {
		t.Fatalf("summary: got %q want the trimmed text", text)
	}
	if usage.TotalTokens != 120 {
		t.Fatalf("usage: got %+v", usage)
	}

	calls := script.Calls()
	if len(calls) != 1 {
		t.Fatalf("calls: got %d want 1", len(calls))
	}
	request := calls[0]
	if len(request.Messages) != 2 {
		t.Fatalf("messages: got %d want the system prompt and one user turn", len(request.Messages))
	}
	if request.Messages[0].Role != wire.RoleSystem {
		t.Fatalf("first message: got %q want system", request.Messages[0].Role)
	}
	if !strings.HasPrefix(request.Messages[0].Content.Text(), "You are a context summarization assistant.") {
		t.Fatalf("system prompt: %q", request.Messages[0].Content.Text())
	}
	prompt := request.Messages[1].Content.Text()
	if !strings.Contains(prompt, "<conversation>\n[User]: hello\n\n[Assistant]: hi\n</conversation>") {
		t.Fatalf("prompt conversation: %q", prompt)
	}
	if !strings.Contains(prompt, "Create a structured context checkpoint summary") {
		t.Fatalf("prompt instructions: %q", prompt)
	}
	if strings.Contains(prompt, "<previous-summary>") {
		t.Fatalf("prompt: a first compaction must not claim a previous summary")
	}
	if request.Options.MaxTokens == nil || *request.Options.MaxTokens != 4096 {
		t.Fatalf("max tokens: got %v want the model cap 4096", request.Options.MaxTokens)
	}
}

// TestSummarizeHistoryBudgetTakesEightyPercentOfTheReserve leaves the budget
// open when the model declares no output cap.
func TestSummarizeHistoryBudgetTakesEightyPercentOfTheReserve(t *testing.T) {
	script := faux.New(faux.Response{Text: "summary"})
	summarizer := &compact.Summarizer{Provider: script, Model: wire.Model{ID: "model"}}

	if _, _, err := summarizer.SummarizeHistory(context.Background(), []wire.Message{
		wire.UserMessage("hello"),
	}, compact.DefaultSettings, "", ""); err != nil {
		t.Fatalf("summarize: %v", err)
	}
	want := compact.DefaultSettings.ReserveTokens * 8 / 10
	if got := script.Calls()[0].Options.MaxTokens; got == nil || *got != want {
		t.Fatalf("max tokens: got %v want %d", got, want)
	}
}

func TestSummarizeHistoryUpdatesAnExistingSummary(t *testing.T) {
	script := faux.New(faux.Response{Text: "updated"})
	summarizer := &compact.Summarizer{Provider: script, Model: wire.Model{ID: "model", MaxTokens: 1000}}

	if _, _, err := summarizer.SummarizeHistory(context.Background(), []wire.Message{
		wire.UserMessage("more work"),
	}, compact.DefaultSettings, "## Goal\nold goal", "focus on tests"); err != nil {
		t.Fatalf("summarize: %v", err)
	}
	prompt := script.Calls()[0].Messages[1].Content.Text()
	if !strings.Contains(prompt, "<previous-summary>\n## Goal\nold goal\n</previous-summary>") {
		t.Fatalf("prompt: %q", prompt)
	}
	if !strings.Contains(prompt, "Update the existing structured summary with new information.") {
		t.Fatalf("prompt: want the update instructions, got %q", prompt)
	}
	if !strings.HasSuffix(prompt, "Additional focus: focus on tests") {
		t.Fatalf("prompt: want the extra instructions appended, got %q", prompt)
	}
}

func TestSummarizeTurnPrefixUsesTheHalfReserveBudget(t *testing.T) {
	script := faux.New(faux.Response{Text: "prefix"})
	summarizer := &compact.Summarizer{Provider: script, Model: wire.Model{ID: "model"}}

	text, _, err := summarizer.SummarizeTurnPrefix(context.Background(), []wire.Message{
		wire.UserMessage("do the thing"),
	}, compact.DefaultSettings)
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}
	if text != "prefix" {
		t.Fatalf("summary: got %q", text)
	}
	request := script.Calls()[0]
	prompt := request.Messages[1].Content.Text()
	if !strings.Contains(prompt, "# Conversation\n[User]: do the thing") {
		t.Fatalf("prompt: %q", prompt)
	}
	if !strings.Contains(prompt, "earlier context from an ongoing conversation") {
		t.Fatalf("prompt: want the turn prefix instructions, got %q", prompt)
	}
	want := compact.DefaultSettings.ReserveTokens / 2
	if request.Options.MaxTokens == nil || *request.Options.MaxTokens != want {
		t.Fatalf("max tokens: got %v want %d", request.Options.MaxTokens, want)
	}
}

// TestSummarizeRejectsAnythingButAWholeSummary pins that every partial outcome
// fails: a checkpoint replaces the history it summarized, so half of one loses
// the other half.
func TestSummarizeRejectsAnythingButAWholeSummary(t *testing.T) {
	cases := []struct {
		name     string
		response faux.Response
		want     string
	}{
		{
			name:     "length stop",
			response: faux.Response{Text: "partial", StopReason: wire.StopLength},
			want:     "token cap",
		},
		{
			name:     "provider error",
			response: faux.Response{Err: errors.New("boom")},
			want:     "boom",
		},
		{
			name:     "tool call",
			response: faux.Response{ToolCalls: []wire.Content{wire.ToolCallContent("call_1", "read", []byte(`{}`))}},
			want:     "attempted to call a tool",
		},
		{
			name:     "empty text",
			response: faux.Response{Text: "   "},
			want:     "no text",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			script := faux.New(test.response)
			summarizer := &compact.Summarizer{Provider: script, Model: wire.Model{ID: "model", MaxTokens: 1000}}
			text, _, err := summarizer.SummarizeHistory(context.Background(), []wire.Message{
				wire.UserMessage("hello"),
			}, compact.DefaultSettings, "", "")
			if err == nil {
				t.Fatalf("summary: got %q, want an error", text)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error: got %q want it to mention %q", err, test.want)
			}
			if text != "" {
				t.Fatalf("summary: got %q, want nothing on failure", text)
			}
		})
	}
}

func TestSummarizeForwardsThinkingOnlyForReasoningModels(t *testing.T) {
	script := faux.New(faux.Response{Text: "one"}, faux.Response{Text: "two"})
	plain := &compact.Summarizer{
		Provider: script,
		Model:    wire.Model{ID: "model", MaxTokens: 1000},
		Options:  providerOptions(),
	}
	reasoning := &compact.Summarizer{
		Provider: script,
		Model:    wire.Model{ID: "model", MaxTokens: 1000, Reasoning: true},
		Options:  providerOptions(),
	}

	if _, _, err := plain.SummarizeHistory(context.Background(), []wire.Message{wire.UserMessage("a")}, compact.DefaultSettings, "", ""); err != nil {
		t.Fatalf("plain: %v", err)
	}
	if _, _, err := reasoning.SummarizeHistory(context.Background(), []wire.Message{wire.UserMessage("b")}, compact.DefaultSettings, "", ""); err != nil {
		t.Fatalf("reasoning: %v", err)
	}

	calls := script.Calls()
	if calls[0].Options.ThinkingLevel != "" {
		t.Fatalf("plain thinking level: got %q want it dropped", calls[0].Options.ThinkingLevel)
	}
	if calls[0].Options.SessionID != "session-1" {
		t.Fatalf("plain session id: got %q want the caller's session forwarded", calls[0].Options.SessionID)
	}
	if calls[0].Options.APIKey != "key" {
		t.Fatalf("plain api key: got %q want the caller's credential", calls[0].Options.APIKey)
	}
	if calls[1].Options.ThinkingLevel != "high" {
		t.Fatalf("reasoning thinking level: got %q want high", calls[1].Options.ThinkingLevel)
	}
}

// TestSerializeConversationShape pins the exact rendering: the summary text is
// the only record of the summarized range, so its shape has to be stable.
func TestSerializeConversationShape(t *testing.T) {
	script := faux.New(faux.Response{Text: "summary"})
	summarizer := &compact.Summarizer{Provider: script, Model: wire.Model{ID: "model", MaxTokens: 1000}}

	assistant := wire.AssistantMessage(
		wire.TextContent("ok"),
		wire.ToolCallContent("call_1", "read", []byte(`{"path":"a.go","limit":5}`)),
	)
	thinkingOnly := wire.AssistantMessage(wire.ThinkingContent("secret reasoning", "sig"))

	if _, _, err := summarizer.SummarizeTurnPrefix(context.Background(), []wire.Message{
		wire.UserMessage("hello"),
		assistant,
		wire.ToolResultMessage("call_1", "read", false, wire.TextContent("content")),
		thinkingOnly,
		wire.ToolResultMessage("call_1", "read", false, wire.TextContent(strings.Repeat("x", 2500))),
	}, compact.DefaultSettings); err != nil {
		t.Fatalf("summarize: %v", err)
	}

	prompt := script.Calls()[0].Messages[1].Content.Text()
	for _, want := range []string{
		"[User]: hello",
		"[Assistant]: ok",
		`[Assistant tool calls]: read(limit=5, path="a.go")`,
		"[Tool result]: content",
		"[... 500 more characters truncated]",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt: missing %q in %q", want, prompt)
		}
	}
	if strings.Contains(prompt, "secret reasoning") {
		t.Fatalf("prompt: thinking must not be serialized")
	}
	if strings.Index(prompt, "[Assistant]: ok") > strings.Index(prompt, "[Assistant tool calls]:") {
		t.Fatalf("prompt: text must precede the tool calls")
	}
}

// providerOptions is the caller-supplied state every summarization call keeps.
func providerOptions() provider.Options {
	return provider.Options{APIKey: "key", SessionID: "session-1", ThinkingLevel: "high"}
}
