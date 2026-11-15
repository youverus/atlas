package sse_test

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pi-go/internal/sse"
)

func readFixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "sse", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return string(data)
}

func parseAll(t *testing.T, stream string) []sse.Event {
	t.Helper()
	reader := sse.NewReader(strings.NewReader(stream))
	var events []sse.Event
	for {
		event, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return events
		}
		if err != nil {
			t.Fatalf("next: %v", err)
		}
		events = append(events, event)
	}
}

// chatChunk is the subset of an OpenAI chat completions chunk needed to rebuild
// a response from a stream.
type chatChunk struct {
	Choices []struct {
		Delta struct {
			Content   *string `json:"content"`
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

func decodeChunk(t *testing.T, payload string) chatChunk {
	t.Helper()
	var chunk chatChunk
	if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
		t.Fatalf("decode chunk %q: %v", payload, err)
	}
	return chunk
}

func TestOpenAITextFixture(t *testing.T) {
	events := parseAll(t, readFixture(t, "openai_text.sse"))
	if len(events) != 6 {
		t.Fatalf("events: got %d", len(events))
	}
	if last := events[len(events)-1].Data; last != "[DONE]" {
		t.Fatalf("terminator: got %q", last)
	}

	var text strings.Builder
	var finish string
	var usage *chatChunk
	for _, event := range events[:len(events)-1] {
		if event.Name != "" {
			t.Fatalf("completions chunks carry no event field, got %q", event.Name)
		}
		chunk := decodeChunk(t, event.Data)
		for _, choice := range chunk.Choices {
			if choice.Delta.Content != nil {
				text.WriteString(*choice.Delta.Content)
			}
			if choice.FinishReason != nil {
				finish = *choice.FinishReason
			}
		}
		if chunk.Usage != nil {
			usage = &chunk
		}
	}

	if got := text.String(); got != "Hello world" {
		t.Fatalf("text: got %q", got)
	}
	if finish != "stop" {
		t.Fatalf("finish reason: got %q", finish)
	}
	if usage == nil || usage.Usage.PromptTokens != 9 || usage.Usage.CompletionTokens != 2 {
		t.Fatalf("usage: got %+v", usage)
	}
}

func TestOpenAIToolCallsFixture(t *testing.T) {
	events := parseAll(t, readFixture(t, "openai_tool_calls.sse"))
	if len(events) != 8 {
		t.Fatalf("events: got %d", len(events))
	}
	if last := events[len(events)-1].Data; last != "[DONE]" {
		t.Fatalf("terminator: got %q", last)
	}

	type call struct {
		id   string
		name string
		args strings.Builder
	}
	var calls []*call
	namesOnlyInFirstChunk := true
	finish := ""
	for _, event := range events[:len(events)-1] {
		chunk := decodeChunk(t, event.Data)
		for _, choice := range chunk.Choices {
			if choice.FinishReason != nil {
				finish = *choice.FinishReason
			}
			for _, fragment := range choice.Delta.ToolCalls {
				for len(calls) <= fragment.Index {
					calls = append(calls, &call{})
				}
				target := calls[fragment.Index]
				if fragment.ID != "" {
					if target.id != "" {
						t.Fatalf("index %d: id repeated", fragment.Index)
					}
					target.id = fragment.ID
					target.name = fragment.Function.Name
					continue
				}
				// The pitfall this fixture locks in: id and name arrive only on
				// the first fragment, later fragments carry arguments alone.
				if fragment.Function.Name != "" {
					namesOnlyInFirstChunk = false
				}
				target.args.WriteString(fragment.Function.Arguments)
			}
		}
	}

	if len(calls) != 2 {
		t.Fatalf("calls: got %d", len(calls))
	}
	if calls[0].id != "call_a" || calls[0].name != "read" || calls[0].args.String() != `{"path":"a.txt"}` {
		t.Fatalf("call 0: got %q %q %q", calls[0].id, calls[0].name, calls[0].args.String())
	}
	if calls[1].id != "call_b" || calls[1].name != "grep" || calls[1].args.String() != `{"pattern":"x"}` {
		t.Fatalf("call 1: got %q %q %q", calls[1].id, calls[1].name, calls[1].args.String())
	}
	if !namesOnlyInFirstChunk {
		t.Fatal("expected name only on the first fragment of each call")
	}
	if finish != "tool_calls" {
		t.Fatalf("finish reason: got %q", finish)
	}
}

func TestAnthropicFixture(t *testing.T) {
	events := parseAll(t, readFixture(t, "anthropic_text.sse"))
	want := []string{
		"message_start",
		"content_block_start",
		"ping",
		"content_block_delta",
		"content_block_delta",
		"content_block_stop",
		"message_delta",
		"message_stop",
	}
	if len(events) != len(want) {
		t.Fatalf("events: got %d", len(events))
	}

	var text strings.Builder
	for i, event := range events {
		if event.Name != want[i] {
			t.Fatalf("event %d name: got %q want %q", i, event.Name, want[i])
		}
		// Anthropic repeats the event name inside the payload; a reader that
		// mis-frames the stream breaks this invariant.
		var payload struct {
			Type  string `json:"type"`
			Delta struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"delta"`
		}
		if err := json.Unmarshal([]byte(event.Data), &payload); err != nil {
			t.Fatalf("event %d payload: %v", i, err)
		}
		if payload.Type != event.Name {
			t.Fatalf("event %d: name %q but payload type %q", i, event.Name, payload.Type)
		}
		if payload.Delta.Type == "text_delta" {
			text.WriteString(payload.Delta.Text)
		}
	}
	if got := text.String(); got != "Hi there" {
		t.Fatalf("text: got %q", got)
	}
}

func TestEdgeCaseFixture(t *testing.T) {
	reader := sse.NewReader(strings.NewReader(readFixture(t, "edge_cases.sse")))
	want := []sse.Event{
		{Name: "alpha", Data: "first\nsecond"},
		{Data: "nospace"},
		{ID: "42", Data: "with-meta", Retry: 3000},
		{Name: "multi", Data: `{"a":1}` + "\n" + `{"b":2}`},
		{Data: "kept"},
		{Data: "final-unterminated"},
	}
	for i, expected := range want {
		event, err := reader.Next()
		if err != nil {
			t.Fatalf("event %d: %v", i, err)
		}
		if event != expected {
			t.Fatalf("event %d: got %+v want %+v", i, event, expected)
		}
	}
	if _, err := reader.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("after last event: got %v", err)
	}
	if got := reader.LastEventID(); got != "42" {
		t.Fatalf("last event id: got %q", got)
	}
}

func TestReaderRules(t *testing.T) {
	cases := []struct {
		name   string
		stream string
		want   []sse.Event
	}{
		{
			name:   "byte order mark on first line",
			stream: "\ufeffdata: hi\n\n",
			want:   []sse.Event{{Data: "hi"}},
		},
		{
			name:   "crlf line endings",
			stream: "event: e\r\ndata: one\r\ndata: two\r\n\r\n",
			want:   []sse.Event{{Name: "e", Data: "one\ntwo"}},
		},
		{
			name:   "bare cr is not a line break",
			stream: "data: a\rdata: b\n\n",
			want:   []sse.Event{{Data: "a\rdata: b"}},
		},
		{
			name:   "field without colon takes an empty value",
			stream: "data\n\n",
			want:   []sse.Event{{Data: ""}},
		},
		{
			name:   "only one leading space is stripped",
			stream: "data:   padded\n\n",
			want:   []sse.Event{{Data: "  padded"}},
		},
		{
			name:   "comments dispatch nothing",
			stream: ": hello\n\n",
			want:   nil,
		},
		{
			name:   "blank line resets a pending event",
			stream: "event: dropped\n\ndata: kept\n\n",
			want:   []sse.Event{{Data: "kept"}},
		},
		{
			name:   "id with nul is ignored",
			stream: "id: a\x00b\ndata: x\n\n",
			want:   []sse.Event{{Data: "x"}},
		},
		{
			name:   "retry is parsed as milliseconds",
			stream: "retry: 1500\ndata: x\n\n",
			want:   []sse.Event{{Data: "x", Retry: 1500}},
		},
		{
			name:   "negative retry is ignored",
			stream: "retry: -1\ndata: x\n\n",
			want:   []sse.Event{{Data: "x"}},
		},
		{
			name:   "non numeric retry is ignored",
			stream: "retry: soon\ndata: x\n\n",
			want:   []sse.Event{{Data: "x"}},
		},
		{
			name:   "unterminated event at eof is dispatched",
			stream: "data: last",
			want:   []sse.Event{{Data: "last"}},
		},
		{
			name:   "unterminated event keeps every data line",
			stream: "data: a\ndata: b\n",
			want:   []sse.Event{{Data: "a\nb"}},
		},
		{
			name:   "empty stream",
			stream: "",
			want:   nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseAll(t, tc.stream)
			if len(got) != len(tc.want) {
				t.Fatalf("events: got %d want %d (%+v)", len(got), len(tc.want), got)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("event %d: got %+v want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestLastEventIDPersistsAcrossEvents(t *testing.T) {
	reader := sse.NewReader(strings.NewReader("id: 7\ndata: a\n\ndata: b\n\n"))
	first, err := reader.Next()
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if first.ID != "7" {
		t.Fatalf("first id: got %q", first.ID)
	}
	second, err := reader.Next()
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if second.ID != "" {
		t.Fatalf("second id: got %q, the field is absent", second.ID)
	}
	if got := reader.LastEventID(); got != "7" {
		t.Fatalf("last event id: got %q", got)
	}
}

func TestEOFIsSticky(t *testing.T) {
	reader := sse.NewReader(strings.NewReader("data: a\n\n"))
	if _, err := reader.Next(); err != nil {
		t.Fatalf("first: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := reader.Next(); !errors.Is(err, io.EOF) {
			t.Fatalf("call %d: got %v", i, err)
		}
	}
}

func TestLineTooLong(t *testing.T) {
	stream := "data: " + strings.Repeat("x", 128) + "\n\n"
	reader := sse.NewReaderSize(strings.NewReader(stream), 32)
	_, err := reader.Next()
	if !errors.Is(err, sse.ErrLineTooLong) {
		t.Fatalf("got %v", err)
	}
	if _, err := reader.Next(); !errors.Is(err, sse.ErrLineTooLong) {
		t.Fatalf("the error must stay sticky, got %v", err)
	}
}

func TestNonPositiveLimitUsesDefault(t *testing.T) {
	for _, limit := range []int{0, -1} {
		reader := sse.NewReaderSize(strings.NewReader("data: ok\n\n"), limit)
		event, err := reader.Next()
		if err != nil {
			t.Fatalf("limit %d: %v", limit, err)
		}
		if event.Data != "ok" {
			t.Fatalf("limit %d data: got %q", limit, event.Data)
		}
	}
}
