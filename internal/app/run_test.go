package app

import (
	"context"
	"errors"
	"testing"

	"pi-go/internal/agent"
	"pi-go/internal/provider/faux"
	"pi-go/internal/wire"
)

func TestRunTurnStopsOnPersistFailure(t *testing.T) {
	prov := faux.New(faux.Response{Text: "answer"})
	ag := agent.New(agent.Config{Model: testModel(), Provider: prov})

	failure := errors.New("disk gone")
	recorded := 0
	persist := func(message wire.Message) error {
		if message.Role == wire.RoleSystem {
			t.Errorf("system message reached persistence")
		}
		recorded++
		return failure
	}

	result, err := runTurn(context.Background(), ag, []wire.Message{wire.UserMessage("hi")}, persist, nil)
	if !errors.Is(err, failure) {
		t.Fatalf("err = %v, want the persist failure", err)
	}
	if recorded == 0 {
		t.Fatal("persistence was never attempted")
	}
	if len(result.Messages) == 0 || result.Messages[0].Role != wire.RoleUser {
		t.Fatalf("messages = %+v, want the prompt first", result.Messages)
	}
	if result.Text() != "" {
		t.Fatalf("text = %q, want no answer after the cancel", result.Text())
	}
}

func TestRunTurnReportsEmitError(t *testing.T) {
	prov := faux.New(faux.Response{Text: "answer"})
	ag := agent.New(agent.Config{Model: testModel(), Provider: prov})

	failure := errors.New("consumer gone")
	emit := func(event wire.AgentEvent) error {
		if event.Type == wire.AgentEventMessageStart {
			return failure
		}
		return nil
	}

	_, err := runTurn(context.Background(), ag, []wire.Message{wire.UserMessage("hi")}, func(wire.Message) error { return nil }, emit)
	if !errors.Is(err, failure) {
		t.Fatalf("err = %v, want the emit failure", err)
	}
}

func TestMessageQueueDeliversOneMessagePerPoll(t *testing.T) {
	var queue messageQueue
	if got := queue.currentMode(); got != QueueModeOneAtATime {
		t.Fatalf("default mode = %q, want %q", got, QueueModeOneAtATime)
	}

	queue.enqueue(wire.UserMessage("one"))
	queue.enqueue(wire.UserMessage("two"))
	if got := queue.len(); got != 2 {
		t.Fatalf("len = %d, want 2", got)
	}

	first := queue.drain()
	if len(first) != 1 || first[0].Text() != "one" {
		t.Fatalf("first drain = %+v, want one message %q", first, "one")
	}
	second := queue.drain()
	if len(second) != 1 || second[0].Text() != "two" {
		t.Fatalf("second drain = %+v, want one message %q", second, "two")
	}
	if rest := queue.drain(); rest != nil {
		t.Fatalf("third drain = %+v, want nil", rest)
	}
	if got := queue.len(); got != 0 {
		t.Fatalf("len = %d, want 0", got)
	}
}

func TestMessageQueueDeliversAllWhenConfigured(t *testing.T) {
	var queue messageQueue
	queue.setMode(QueueModeAll)
	queue.enqueue(wire.UserMessage("one"))
	queue.enqueue(wire.UserMessage("two"))

	drained := queue.drain()
	if len(drained) != 2 {
		t.Fatalf("drained %d messages, want 2", len(drained))
	}
	if drained[0].Text() != "one" || drained[1].Text() != "two" {
		t.Fatalf("drained %q, want one then two", []string{drained[0].Text(), drained[1].Text()})
	}

	queue.setMode(QueueModeOneAtATime)
	if got := queue.currentMode(); got != QueueModeOneAtATime {
		t.Fatalf("mode = %q, want %q", got, QueueModeOneAtATime)
	}
}

func TestMessageQueuePendingIsACopy(t *testing.T) {
	var queue messageQueue
	queue.enqueue(wire.UserMessage("only"))

	pending := queue.pending()
	if len(pending) != 1 {
		t.Fatalf("pending = %+v, want one message", pending)
	}
	pending[0] = wire.UserMessage("mutated")
	if got := queue.pending()[0].Text(); got != "only" {
		t.Fatalf("queue text = %q, want the queue unchanged", got)
	}
}

func TestMessageQueueClearReturnsContent(t *testing.T) {
	var queue messageQueue
	queue.enqueue(wire.UserMessage("one"))
	queue.enqueue(wire.UserMessage("two"))

	cleared := queue.clear()
	if len(cleared) != 2 {
		t.Fatalf("cleared %d messages, want 2", len(cleared))
	}
	if got := queue.len(); got != 0 {
		t.Fatalf("len after clear = %d, want 0", got)
	}
	if again := queue.clear(); again != nil {
		t.Fatalf("second clear = %+v, want nil", again)
	}
}

func TestParseQueueMode(t *testing.T) {
	cases := []struct {
		in      string
		want    QueueMode
		wantErr bool
	}{
		{"", QueueModeOneAtATime, false},
		{"one-at-a-time", QueueModeOneAtATime, false},
		{"all", QueueModeAll, false},
		{"sometimes", "", true},
	}
	for _, tc := range cases {
		got, err := ParseQueueMode(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("ParseQueueMode(%q) = %q, want an error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Fatalf("ParseQueueMode(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("ParseQueueMode(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSummarizePicksTheLastAssistantMessage(t *testing.T) {
	usage := func(input, output int) *wire.Usage {
		return &wire.Usage{Input: input, Output: output, TotalTokens: input + output}
	}
	messages := []wire.Message{
		wire.UserMessage("hi"),
		{Role: wire.RoleAssistant, Content: wire.NewTextList("first"), Usage: usage(10, 1), StopReason: wire.StopToolUse},
		wire.ToolResultMessage("call-1", "echo", false, wire.TextContent("echo")),
		{Role: wire.RoleAssistant, Content: wire.NewTextList("second"), Usage: usage(20, 2), StopReason: wire.StopStop},
	}

	result := summarize(messages)
	if result.Text() != "second" {
		t.Fatalf("text = %q, want second", result.Text())
	}
	if result.StopReason != wire.StopStop {
		t.Fatalf("stop reason = %q, want stop", result.StopReason)
	}
	if result.Usage.Input != 30 || result.Usage.Output != 3 || result.Usage.TotalTokens != 33 {
		t.Fatalf("usage = %+v, want the sum of both turns", result.Usage)
	}
	if len(result.Messages) != len(messages) {
		t.Fatalf("messages = %d, want %d", len(result.Messages), len(messages))
	}
}

func TestSummarizeWithoutAssistant(t *testing.T) {
	result := summarize([]wire.Message{wire.UserMessage("hi")})
	if result.Assistant != nil {
		t.Fatalf("assistant = %+v, want nil", result.Assistant)
	}
	if result.Text() != "" {
		t.Fatalf("text = %q, want empty", result.Text())
	}
}
