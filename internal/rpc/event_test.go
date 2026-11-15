package rpc

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"pi-go/internal/wire"
)

// decode renders an encoded value as a generic JSON tree, which is what a
// client sees.
func decode(t *testing.T, value any) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var tree map[string]any
	if err := json.Unmarshal(encoded, &tree); err != nil {
		t.Fatalf("unmarshal %s: %v", encoded, err)
	}
	return tree
}

func TestEncodeEventPassesOtherEventsThrough(t *testing.T) {
	message := wire.UserMessage("hi")
	event := wire.AgentEvent{Type: wire.AgentEventMessageEnd, Message: &message}

	encoded, err := encodeEvent(event)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !reflect.DeepEqual(encoded, event) {
		t.Fatalf("encoded = %#v, want the event unchanged", encoded)
	}
}

func TestEncodeMessageUpdateDropsPartial(t *testing.T) {
	partial := wire.AssistantMessage(wire.TextContent("hi"))
	update := wire.AssistantEvent{Type: "text_delta", ContentIndex: wire.Ptr(0), Delta: "hi", Partial: &partial}
	event := wire.AgentEvent{Type: wire.AgentEventMessageUpdate, Message: &partial, AssistantEvent: &update}

	tree := decode(t, mustEncode(t, event))
	if _, ok := tree["message"]; ok {
		t.Fatal("message_update carries the accumulated message")
	}
	if tree["type"] != wire.AgentEventMessageUpdate {
		t.Fatalf("type = %v, want message_update", tree["type"])
	}

	// Usage is required so a client can total a stream without tracking each
	// snapshot; a provider that has not reported it yet leaves zeros.
	usage, ok := tree["usage"].(map[string]any)
	if !ok {
		t.Fatalf("usage = %#v, want an object", tree["usage"])
	}
	for _, key := range []string{"input", "output", "cacheRead", "cacheWrite", "totalTokens", "cost"} {
		if _, ok := usage[key]; !ok {
			t.Fatalf("usage is missing %q: %#v", key, usage)
		}
	}

	assistant, ok := tree["assistantMessageEvent"].(map[string]any)
	if !ok {
		t.Fatalf("assistantMessageEvent = %#v, want an object", tree["assistantMessageEvent"])
	}
	if _, ok := assistant["partial"]; ok {
		t.Fatal("assistant event still carries its partial snapshot")
	}
	if assistant["type"] != "text_delta" || assistant["delta"] != "hi" {
		t.Fatalf("assistant event = %#v, want a text delta", assistant)
	}
	if assistant["contentIndex"] != float64(0) {
		t.Fatalf("contentIndex = %v, want 0", assistant["contentIndex"])
	}
}

func TestEncodeMessageUpdateReportsUsage(t *testing.T) {
	partial := wire.AssistantMessage(wire.TextContent("hi"))
	partial.Usage = &wire.Usage{Input: 3, Output: 5, TotalTokens: 8}
	update := wire.AssistantEvent{Type: "text_end", ContentIndex: wire.Ptr(0), Content: "hi", Partial: &partial}
	event := wire.AgentEvent{Type: wire.AgentEventMessageUpdate, Message: &partial, AssistantEvent: &update}

	tree := decode(t, mustEncode(t, event))
	usage, ok := tree["usage"].(map[string]any)
	if !ok {
		t.Fatalf("usage = %#v, want an object", tree["usage"])
	}
	if usage["input"] != float64(3) || usage["output"] != float64(5) || usage["totalTokens"] != float64(8) {
		t.Fatalf("usage = %#v, want the message's numbers", usage)
	}
}

func TestEncodeToolCallStartAddsIDAndName(t *testing.T) {
	partial := wire.AssistantMessage(
		wire.TextContent("thinking"),
		wire.ToolCallContent("call-1", "echo", json.RawMessage(`{"text":"hi"}`)),
	)
	update := wire.AssistantEvent{Type: "toolcall_start", ContentIndex: wire.Ptr(1), Partial: &partial}
	event := wire.AgentEvent{Type: wire.AgentEventMessageUpdate, Message: &partial, AssistantEvent: &update}

	tree := decode(t, mustEncode(t, event))
	assistant, ok := tree["assistantMessageEvent"].(map[string]any)
	if !ok {
		t.Fatalf("assistantMessageEvent = %#v, want an object", tree["assistantMessageEvent"])
	}
	if assistant["id"] != "call-1" || assistant["toolName"] != "echo" {
		t.Fatalf("assistant event = %#v, want the call id and name", assistant)
	}
	if assistant["contentIndex"] != float64(1) {
		t.Fatalf("contentIndex = %v, want 1", assistant["contentIndex"])
	}
}

func TestEncodeToolCallStartRejectsNonToolBlock(t *testing.T) {
	partial := wire.AssistantMessage(wire.TextContent("not a call"))
	update := wire.AssistantEvent{Type: "toolcall_start", ContentIndex: wire.Ptr(0), Partial: &partial}
	event := wire.AgentEvent{Type: wire.AgentEventMessageUpdate, Message: &partial, AssistantEvent: &update}

	_, err := encodeEvent(event)
	if err == nil {
		t.Fatal("expected an error for a toolcall_start over a text block")
	}
	if !strings.Contains(err.Error(), "is not a tool call") {
		t.Fatalf("err = %v, want the pi wording", err)
	}
}

func TestEncodeMessageUpdateRequiresAssistantMessage(t *testing.T) {
	user := wire.UserMessage("hi")
	update := wire.AssistantEvent{Type: "text_delta"}
	event := wire.AgentEvent{Type: wire.AgentEventMessageUpdate, Message: &user, AssistantEvent: &update}
	if _, err := encodeEvent(event); err == nil {
		t.Fatal("expected an error for a non-assistant message")
	}

	assistant := wire.AssistantMessage()
	event = wire.AgentEvent{Type: wire.AgentEventMessageUpdate, Message: &assistant}
	if _, err := encodeEvent(event); err == nil {
		t.Fatal("expected an error for a message_update without an assistant event")
	}
}

func TestEncodeDoneEventKeepsMessage(t *testing.T) {
	final := wire.AssistantMessage(wire.TextContent("done"))
	final.StopReason = wire.StopStop
	event := wire.AgentEvent{
		Type:           wire.AgentEventMessageUpdate,
		Message:        &final,
		AssistantEvent: &wire.AssistantEvent{Type: "done", Reason: "stop", Message: &final},
	}

	tree := decode(t, mustEncode(t, event))
	assistant, ok := tree["assistantMessageEvent"].(map[string]any)
	if !ok {
		t.Fatalf("assistantMessageEvent = %#v, want an object", tree["assistantMessageEvent"])
	}
	if assistant["reason"] != "stop" {
		t.Fatalf("reason = %v, want stop", assistant["reason"])
	}
	message, ok := assistant["message"].(map[string]any)
	if !ok {
		t.Fatalf("message = %#v, want the final message", assistant["message"])
	}
	if message["role"] != "assistant" {
		t.Fatalf("message role = %v, want assistant", message["role"])
	}
}

func mustEncode(t *testing.T, event wire.AgentEvent) any {
	t.Helper()
	value, err := encodeEvent(event)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return value
}

func TestEncodeToolCallStartNeedsAContentIndex(t *testing.T) {
	partial := wire.AssistantMessage(wire.ToolCallContent("call-1", "echo", json.RawMessage(`{}`)))
	update := wire.AssistantEvent{Type: "toolcall_start", Partial: &partial}
	event := wire.AgentEvent{Type: wire.AgentEventMessageUpdate, Message: &partial, AssistantEvent: &update}

	if _, err := encodeEvent(event); err == nil {
		t.Fatal("expected an error for a toolcall_start without a content index")
	}
}

func TestLastAssistantSkipsEmptyAbortedMessages(t *testing.T) {
	aborted := wire.AssistantMessage()
	aborted.StopReason = wire.StopAborted
	answer := wire.AssistantMessage(wire.TextContent("  the answer  "))
	answer.StopReason = wire.StopStop

	messages := []wire.Message{wire.UserMessage("hi"), answer, aborted}
	if got := lastAssistant(messages); got != "the answer" {
		t.Fatalf("text = %q, want the trimmed answer", got)
	}
	if got := lastAssistant([]wire.Message{wire.UserMessage("hi")}); got != "" {
		t.Fatalf("text = %q, want empty without an assistant message", got)
	}
}
