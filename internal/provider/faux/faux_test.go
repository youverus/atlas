package faux_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"pi-go/internal/provider"
	"pi-go/internal/provider/faux"
	"pi-go/internal/wire"
)

var model = wire.Model{ID: "faux-1", Name: "Faux", API: "faux", Provider: "faux"}

func collect(t *testing.T, p *faux.Provider) (wire.Message, []wire.AssistantEvent) {
	t.Helper()
	stream, err := p.Stream(context.Background(), provider.Request{Model: model})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	message, events, err := provider.Collect(stream)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	return message, events
}

func TestTextResponse(t *testing.T) {
	p := faux.New(faux.Response{Text: "hello world", ChunkSize: 3})
	message, events := collect(t, p)

	if got := message.Text(); got != "hello world" {
		t.Fatalf("text: got %q", got)
	}
	if message.StopReason != wire.StopStop {
		t.Fatalf("stop reason: got %q", message.StopReason)
	}
	if len(events) < 5 {
		t.Fatalf("expected chunked deltas, got %d events", len(events))
	}
	if events[0].Type != wire.EventStart {
		t.Fatalf("first event: got %q", events[0].Type)
	}
	if last := events[len(events)-1]; last.Type != wire.EventDone {
		t.Fatalf("last event: got %q", last.Type)
	}
}

func TestToolCallResponse(t *testing.T) {
	p := faux.New(faux.Response{
		Text: "working",
		ToolCalls: []wire.Content{
			wire.ToolCallContent("call_1", "echo", json.RawMessage(`{"text":"hi"}`)),
		},
	})
	message, _ := collect(t, p)

	calls := message.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("tool calls: got %d", len(calls))
	}
	if calls[0].ID != "call_1" || calls[0].Name != "echo" {
		t.Fatalf("unexpected call: %+v", calls[0])
	}
	if !json.Valid(calls[0].Arguments) {
		t.Fatalf("arguments are not valid JSON: %s", calls[0].Arguments)
	}
	if message.StopReason != wire.StopToolUse {
		t.Fatalf("stop reason: got %q", message.StopReason)
	}
}

func TestErrorResponse(t *testing.T) {
	p := faux.New(faux.Response{Err: errors.New("boom")})
	message, _ := collect(t, p)

	if message.StopReason != wire.StopError {
		t.Fatalf("stop reason: got %q", message.StopReason)
	}
	if message.ErrorMessage != "boom" {
		t.Fatalf("error message: got %q", message.ErrorMessage)
	}
}

func TestScriptExhaustion(t *testing.T) {
	p := faux.New(faux.Response{Text: "only"})
	if _, _ = collect(t, p); p.Pending() != 0 {
		t.Fatalf("pending: got %d", p.Pending())
	}
	message, _ := collect(t, p)
	if message.StopReason != wire.StopStop || message.Text() != "" {
		t.Fatalf("unexpected fallback response: %+v", message)
	}
	if got := len(p.Calls()); got != 2 {
		t.Fatalf("calls: got %d", got)
	}
}
