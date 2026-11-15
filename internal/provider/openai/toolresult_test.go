package openai_test

import (
	"encoding/json"
	"testing"

	"pi-go/internal/provider"
	"pi-go/internal/provider/openai"
	"pi-go/internal/wire"
)

// modelWithInput builds a model declaration with the given modalities.
func modelWithInput(input ...string) wire.Model {
	return wire.Model{ID: testModel, API: openai.APIName, Provider: "openai", Input: input}
}

// sendChat runs one request against a replayed stream and returns the messages
// the adapter put on the wire.
func sendChat(t *testing.T, compat openai.Compat, model wire.Model, messages ...wire.Message) []map[string]any {
	t.Helper()
	server := sseServer(t, "openai_text.sse")
	compat.BaseURL = server.URL
	collect(t, openai.New(compat), provider.Request{Model: model, Messages: messages})
	return requestMessages(t, server.last(t).body)
}

// contentParts decodes a part array so a mismatch prints readably.
func contentParts(t *testing.T, label string, value any) string {
	t.Helper()
	parts, ok := value.([]any)
	if !ok {
		t.Fatalf("%s is not a part array: %v", label, value)
	}
	return normalizeJSON(t, label, mustJSON(t, parts))
}

func assertContentParts(t *testing.T, label string, value any, want string) {
	t.Helper()
	got := contentParts(t, label, value)
	expected := normalizeJSON(t, "want", []byte(want))
	if got != expected {
		t.Fatalf("%s mismatch\n--- got ---\n%s\n--- want ---\n%s", label, got, expected)
	}
}

// The chat completions tool role carries text only, so images found in tool
// results follow the run as one user message. This is what `read` produces for
// a PNG.
func TestToolResultImageBecomesUserMessage(t *testing.T) {
	result := wire.ToolResultMessage("call_1", "read", false,
		wire.TextContent("Read image file [image/png]"),
		wire.ImageContent("QUJD", "image/png"),
	)
	messages := sendChat(t, openai.Compat{}, modelWithInput(), wire.UserMessage("look"), result)

	if len(messages) != 3 {
		t.Fatalf("messages: got %d (%+v)", len(messages), messages)
	}
	tool := messages[1]
	if tool["role"] != "tool" || tool["tool_call_id"] != "call_1" {
		t.Fatalf("tool message: %+v", tool)
	}
	if tool["content"] != "Read image file [image/png]" {
		t.Fatalf("tool text: got %v", tool["content"])
	}
	if tool["name"] != nil {
		t.Fatalf("tool message carries a name without the compat flag: %+v", tool)
	}
	if messages[2]["role"] != "user" {
		t.Fatalf("trailing message role: got %v", messages[2]["role"])
	}
	assertContentParts(t, "user content", messages[2]["content"], `[
		{"type":"text","text":"Attached image(s) from tool result:"},
		{"type":"image_url","image_url":{"url":"data:image/png;base64,QUJD"}}
	]`)
}

// A result with an image and no text still pairs the call with an output, which
// some servers require.
func TestToolResultImageOnlyGetsPlaceholder(t *testing.T) {
	result := wire.ToolResultMessage("call_1", "read", false, wire.ImageContent("QUJD", "image/png"))
	messages := sendChat(t, openai.Compat{}, modelWithInput(), wire.UserMessage("look"), result)

	if len(messages) != 3 {
		t.Fatalf("messages: got %d (%+v)", len(messages), messages)
	}
	if got := messages[1]["content"]; got != "(see attached image)" {
		t.Fatalf("tool text: got %v", got)
	}
}

// Parallel tool calls produce consecutive results; their images belong to one
// user message after the whole run, not one message per result.
func TestConsecutiveToolResultsAggregateIntoOneUserMessage(t *testing.T) {
	first := wire.ToolResultMessage("call_1", "read", false, wire.ImageContent("QUJD", "image/png"))
	second := wire.ToolResultMessage("call_2", "read", false, wire.ImageContent("REVG", "image/jpeg"))
	messages := sendChat(t, openai.Compat{}, modelWithInput(),
		wire.UserMessage("look"), first, second, wire.UserMessage("thanks"))

	if len(messages) != 5 {
		t.Fatalf("messages: got %d (%+v)", len(messages), messages)
	}
	for i, want := range []string{"user", "tool", "tool", "user", "user"} {
		if got := messages[i]["role"]; got != want {
			t.Fatalf("message %d role: got %v want %v", i, got, want)
		}
	}
	if messages[1]["tool_call_id"] != "call_1" || messages[2]["tool_call_id"] != "call_2" {
		t.Fatalf("tool call ids: %v %v", messages[1]["tool_call_id"], messages[2]["tool_call_id"])
	}
	assertContentParts(t, "images", messages[3]["content"], `[
		{"type":"text","text":"Attached image(s) from tool result:"},
		{"type":"image_url","image_url":{"url":"data:image/png;base64,QUJD"}},
		{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,REVG"}}
	]`)
	if messages[4]["content"] != "thanks" {
		t.Fatalf("following user content: got %v", messages[4]["content"])
	}
}

// A model that declares text-only input gets the tool text but no image part,
// and no trailing user message either.
func TestTextOnlyModelDropsToolResultImages(t *testing.T) {
	result := wire.ToolResultMessage("call_1", "read", false,
		wire.TextContent("Read image file [image/png]"),
		wire.ImageContent("QUJD", "image/png"),
	)
	messages := sendChat(t, openai.Compat{}, modelWithInput("text"), wire.UserMessage("look"), result)

	if len(messages) != 2 {
		t.Fatalf("messages: got %d (%+v)", len(messages), messages)
	}
	if got := messages[1]["content"]; got != "Read image file [image/png]" {
		t.Fatalf("tool text: got %v", got)
	}
}

// Without a declared modality list an image is not silently discarded; the flag
// exists for catalogs that say otherwise.
func TestUndeclaredModalitiesAcceptImages(t *testing.T) {
	result := wire.ToolResultMessage("call_1", "read", false, wire.ImageContent("QUJD", "image/png"))
	messages := sendChat(t, openai.Compat{}, wire.Model{ID: testModel, API: openai.APIName},
		wire.UserMessage("look"), result)

	if len(messages) != 3 {
		t.Fatalf("messages: got %d (%+v)", len(messages), messages)
	}
	if messages[2]["role"] != "user" {
		t.Fatalf("trailing message role: got %v", messages[2]["role"])
	}
}

func TestRequireToolResultName(t *testing.T) {
	compat := openai.Compat{RequireToolResultName: true}
	result := wire.ToolResultMessage("call_1", "echo", false, wire.TextContent("hi"))
	messages := sendChat(t, compat, modelWithInput(), wire.UserMessage("go"), result)

	if len(messages) != 2 {
		t.Fatalf("messages: got %d (%+v)", len(messages), messages)
	}
	if got := messages[1]["name"]; got != "echo" {
		t.Fatalf("tool name: got %v", got)
	}
}

// Providers with this flag refuse a user message directly after tool results,
// so a synthetic assistant turn bridges the gap. With images the bridge goes
// before the trailing image message.
func TestRequireAssistantAfterToolResult(t *testing.T) {
	compat := openai.Compat{RequireAssistantAfterToolResult: true}

	t.Run("text result then user", func(t *testing.T) {
		result := wire.ToolResultMessage("call_1", "echo", false, wire.TextContent("hi"))
		messages := sendChat(t, compat, modelWithInput(),
			wire.UserMessage("go"), result, wire.UserMessage("next"))

		if len(messages) != 4 {
			t.Fatalf("messages: got %d (%+v)", len(messages), messages)
		}
		for i, want := range []string{"user", "tool", "assistant", "user"} {
			if got := messages[i]["role"]; got != want {
				t.Fatalf("message %d role: got %v want %v", i, got, want)
			}
		}
		if got := messages[2]["content"]; got != "I have processed the tool results." {
			t.Fatalf("bridge content: got %v", got)
		}
	})

	t.Run("image result", func(t *testing.T) {
		result := wire.ToolResultMessage("call_1", "read", false, wire.ImageContent("QUJD", "image/png"))
		messages := sendChat(t, compat, modelWithInput(), wire.UserMessage("look"), result)

		if len(messages) != 4 {
			t.Fatalf("messages: got %d (%+v)", len(messages), messages)
		}
		for i, want := range []string{"user", "tool", "assistant", "user"} {
			if got := messages[i]["role"]; got != want {
				t.Fatalf("message %d role: got %v want %v", i, got, want)
			}
		}
		if got := messages[2]["content"]; got != "I have processed the tool results." {
			t.Fatalf("bridge content: got %v", got)
		}
		if _, ok := messages[3]["content"].([]any); !ok {
			t.Fatalf("trailing message is not a part array: %v", messages[3]["content"])
		}
	})

	t.Run("assistant tool call carries empty content", func(t *testing.T) {
		assistant := wire.AssistantMessage(wire.ToolCallContent("call_1", "echo", json.RawMessage(`{"text":"hi"}`)))
		assistant.StopReason = wire.StopToolUse
		result := wire.ToolResultMessage("call_1", "echo", false, wire.TextContent("hi"))
		messages := sendChat(t, compat, modelWithInput(), wire.UserMessage("go"), assistant, result)

		if len(messages) != 3 {
			t.Fatalf("messages: got %d (%+v)", len(messages), messages)
		}
		if got := messages[1]["content"]; got != "" {
			t.Fatalf("assistant content: got %v", got)
		}
	})
}
