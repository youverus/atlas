package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"pi-go/internal/provider/faux"
	"pi-go/internal/tool"
	"pi-go/internal/wire"
)

func TestSideInheritsSnapshot(t *testing.T) {
	manager := newSession(t)
	prov := faux.New(faux.Response{Text: "main answer"}, faux.Response{Text: "side answer"})
	application := newTestApp(t, manager, prov)

	if _, err := application.Ask(context.Background(), wire.UserMessage("main question"), nil); err != nil {
		t.Fatalf("main ask: %v", err)
	}

	side, err := application.BeginSide()
	if err != nil {
		t.Fatalf("begin side: %v", err)
	}
	messages := side.Messages()
	if len(messages) != 2 {
		t.Fatalf("side snapshot has %d messages, want 2", len(messages))
	}
	if messages[0].Text() != "main question" || messages[1].Text() != "main answer" {
		t.Fatalf("snapshot = %+v, want the main exchange", messages)
	}
	leaf := manager.LeafID()
	if leaf == nil || side.ForkEntry() != *leaf {
		t.Fatalf("fork entry = %q, want %v", side.ForkEntry(), leaf)
	}

	result, err := side.Ask(context.Background(), wire.UserMessage("side question"), nil)
	if err != nil {
		t.Fatalf("side ask: %v", err)
	}
	if result.Text() != "side answer" {
		t.Fatalf("text = %q, want side answer", result.Text())
	}

	calls := prov.Calls()
	if len(calls) != 2 {
		t.Fatalf("requests = %d, want 2", len(calls))
	}
	request := calls[1].Messages
	if len(request) != 4 {
		t.Fatalf("side request has %d messages, want 4", len(request))
	}
	if request[0].Role != wire.RoleSystem {
		t.Fatalf("first message role = %q, want system", request[0].Role)
	}
	if !strings.Contains(request[0].Text(), "base prompt") || !strings.Contains(request[0].Text(), "Side conversation") {
		t.Fatalf("side prompt = %q, want the base prompt plus the side rules", request[0].Text())
	}
	if request[3].Text() != "side question" {
		t.Fatalf("last message = %q, want the side question", request[3].Text())
	}
}

func TestSideKeepsMainConversationUntouched(t *testing.T) {
	manager := newSession(t)
	prov := faux.New(faux.Response{Text: "main answer"}, faux.Response{Text: "side answer"})
	application := newTestApp(t, manager, prov)
	if _, err := application.Ask(context.Background(), wire.UserMessage("main question"), nil); err != nil {
		t.Fatalf("main ask: %v", err)
	}
	beforeEntries := len(manager.Entries())
	beforeMessages := len(application.Messages())

	side, err := application.BeginSide()
	if err != nil {
		t.Fatalf("begin side: %v", err)
	}
	if _, err := side.Ask(context.Background(), wire.UserMessage("side question"), nil); err != nil {
		t.Fatalf("side ask: %v", err)
	}

	if got := len(manager.Entries()); got != beforeEntries {
		t.Fatalf("main session has %d entries, want %d", got, beforeEntries)
	}
	if got := len(application.Messages()); got != beforeMessages {
		t.Fatalf("main transcript has %d messages, want %d", got, beforeMessages)
	}
	if got := len(side.Messages()); got != beforeMessages+2 {
		t.Fatalf("side transcript has %d messages, want %d", got, beforeMessages+2)
	}
	if side.Session().Persisted() {
		t.Fatal("side session is persisted")
	}
}

func TestBeginSideReplacesPrevious(t *testing.T) {
	application := newTestApp(t, newSession(t), faux.New())

	first, err := application.BeginSide()
	if err != nil {
		t.Fatalf("first side: %v", err)
	}
	second, err := application.BeginSide()
	if err != nil {
		t.Fatalf("second side: %v", err)
	}
	if !first.Closed() {
		t.Fatal("previous side conversation is still open")
	}
	if application.Side() != second {
		t.Fatal("app does not expose the newest side conversation")
	}
	if _, err := first.Ask(context.Background(), wire.UserMessage("hi"), nil); !errors.Is(err, ErrSideClosed) {
		t.Fatalf("ask on the replaced side err = %v, want ErrSideClosed", err)
	}
}

func TestEndSideDiscardsConversation(t *testing.T) {
	application := newTestApp(t, newSession(t), faux.New())
	side, err := application.BeginSide()
	if err != nil {
		t.Fatalf("begin side: %v", err)
	}

	application.EndSide()
	if application.Side() != nil {
		t.Fatal("app still exposes a side conversation")
	}
	if _, err := side.Ask(context.Background(), wire.UserMessage("hi"), nil); !errors.Is(err, ErrSideClosed) {
		t.Fatalf("ask err = %v, want ErrSideClosed", err)
	}

	// Ending twice is a no-op, which is what a UI closing on every event needs.
	application.EndSide()
}

func TestSideCloseDropsFromApp(t *testing.T) {
	application := newTestApp(t, newSession(t), faux.New())
	side, err := application.BeginSide()
	if err != nil {
		t.Fatalf("begin side: %v", err)
	}
	side.Close()
	if !side.Closed() {
		t.Fatal("side did not report itself closed")
	}
	if application.Side() != nil {
		t.Fatal("app still returns a closed side conversation")
	}
}

func TestSideRejectsConcurrentPrompt(t *testing.T) {
	prov := newGateProvider()
	application := newTestApp(t, newSession(t), prov)
	side, err := application.BeginSide()
	if err != nil {
		t.Fatalf("begin side: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := side.Ask(context.Background(), wire.UserMessage("first"), nil)
		done <- err
	}()

	<-prov.started
	if _, err := side.Ask(context.Background(), wire.UserMessage("second"), nil); !errors.Is(err, ErrBusy) {
		t.Fatalf("second side ask err = %v, want ErrBusy", err)
	}
	close(prov.release)
	if err := <-done; err != nil {
		t.Fatalf("first side ask: %v", err)
	}
}

func TestSideSharesToolsAndModel(t *testing.T) {
	prov := faux.New(faux.Response{Text: "side answer"})
	application := newTestApp(t, newSession(t), prov, func(cfg *Config) {
		cfg.Tools = StaticTools([]tool.Tool{echoTool{}})
	})
	side, err := application.BeginSide()
	if err != nil {
		t.Fatalf("begin side: %v", err)
	}
	if _, err := side.Ask(context.Background(), wire.UserMessage("hi"), nil); err != nil {
		t.Fatalf("side ask: %v", err)
	}

	calls := prov.Calls()
	if len(calls) != 1 {
		t.Fatalf("requests = %d, want 1", len(calls))
	}
	if calls[0].Model.ID != "faux-1" {
		t.Fatalf("model = %q, want faux-1", calls[0].Model.ID)
	}
	tools := calls[0].Messages[0].ToolsAdded
	if len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("declared tools = %+v, want echo", tools)
	}
}

func TestSideReplacesMainSystemPrompt(t *testing.T) {
	manager := newSession(t)
	mustAppend(t, manager, wire.SystemMessage("prompt from the session"))
	mustAppend(t, manager, wire.UserMessage("earlier"))
	prov := faux.New(faux.Response{Text: "side answer"})
	application := newTestApp(t, manager, prov)
	if got := application.SystemPrompt(); got != "prompt from the session" {
		t.Fatalf("main prompt = %q, want the session prompt", got)
	}

	side, err := application.BeginSide()
	if err != nil {
		t.Fatalf("begin side: %v", err)
	}
	for _, message := range side.Messages() {
		if message.Role == wire.RoleSystem {
			t.Fatalf("side snapshot kept a system message: %+v", message)
		}
	}
	if _, err := side.Ask(context.Background(), wire.UserMessage("hi"), nil); err != nil {
		t.Fatalf("side ask: %v", err)
	}

	request := prov.Calls()[0].Messages
	systems := 0
	for _, message := range request {
		if message.Role == wire.RoleSystem {
			systems++
		}
	}
	if systems != 1 {
		t.Fatalf("request has %d system messages, want 1", systems)
	}
	text := request[0].Text()
	if !strings.Contains(text, "prompt from the session") {
		t.Fatalf("side prompt = %q, want the main prompt", text)
	}
	if !strings.Contains(text, "read-only snapshot") {
		t.Fatalf("side prompt = %q, want the read-only rule", text)
	}
}

func TestSideWithoutMainPromptUsesRulesOnly(t *testing.T) {
	manager := newSession(t)
	prov := faux.New(faux.Response{Text: "side answer"})
	application := newTestApp(t, manager, prov, func(cfg *Config) {
		cfg.SystemPrompt = StaticPrompt("")
	})
	side, err := application.BeginSide()
	if err != nil {
		t.Fatalf("begin side: %v", err)
	}
	if _, err := side.Ask(context.Background(), wire.UserMessage("hi"), nil); err != nil {
		t.Fatalf("side ask: %v", err)
	}
	text := prov.Calls()[0].Messages[0].Text()
	if strings.HasPrefix(text, "\n") || strings.HasSuffix(text, "\n") {
		t.Fatalf("side prompt is not trimmed: %q", text)
	}
	if !strings.Contains(text, "Side conversation") {
		t.Fatalf("side prompt = %q, want the side rules", text)
	}
}

func TestSideSteerIsInjectedBetweenTurns(t *testing.T) {
	block := newBlockingTool()
	prov := faux.New(
		faux.Response{ToolCalls: []wire.Content{
			wire.ToolCallContent("call-1", "block", json.RawMessage(`{}`)),
		}},
		faux.Response{Text: "after steering"},
	)
	application := newTestApp(t, newSession(t), prov, func(cfg *Config) {
		cfg.Tools = StaticTools([]tool.Tool{block})
	})
	side, err := application.BeginSide()
	if err != nil {
		t.Fatalf("begin side: %v", err)
	}

	done := make(chan Result, 1)
	go func() {
		result, askErr := side.Ask(context.Background(), wire.UserMessage("start"), nil)
		if askErr != nil {
			t.Errorf("side ask: %v", askErr)
		}
		done <- result
	}()

	<-block.started
	if !side.Busy() {
		t.Error("side reports idle while a turn is running")
	}
	if application.Busy() {
		t.Error("main conversation reports busy while only the side is running")
	}
	side.Steer(wire.UserMessage("steer"))
	close(block.release)

	result := <-done
	if result.Text() != "after steering" {
		t.Fatalf("text = %q, want after steering", result.Text())
	}
	found := false
	for _, message := range side.Messages() {
		if message.Role == wire.RoleUser && message.Text() == "steer" {
			found = true
		}
	}
	if !found {
		t.Fatal("side transcript did not record the steering message")
	}
	calls := prov.Calls()
	if len(calls) != 2 {
		t.Fatalf("requests = %d, want 2", len(calls))
	}
	carried := false
	for _, message := range calls[1].Messages {
		if message.Role == wire.RoleUser && message.Text() == "steer" {
			carried = true
		}
	}
	if !carried {
		t.Fatal("second side request did not carry the steering message")
	}
}

// The side conversation owns its own queues: a follow-up queued on the side
// must extend the side run and leave the main conversation untouched.
func TestSideQueuesAreIndependent(t *testing.T) {
	prov := faux.New(faux.Response{Text: "first"}, faux.Response{Text: "second"})
	application := newTestApp(t, newSession(t), prov)
	side, err := application.BeginSide()
	if err != nil {
		t.Fatalf("begin side: %v", err)
	}

	side.FollowUp(wire.UserMessage("side follow-up"))
	application.Steer(wire.UserMessage("main steer"))

	result, err := side.Ask(context.Background(), wire.UserMessage("start"), nil)
	if err != nil {
		t.Fatalf("side ask: %v", err)
	}
	var texts []string
	for _, message := range result.Messages {
		texts = append(texts, message.Text())
	}
	want := []string{"start", "first", "side follow-up", "second"}
	if len(texts) != len(want) {
		t.Fatalf("side messages = %q, want %q", texts, want)
	}
	for i := range want {
		if texts[i] != want[i] {
			t.Fatalf("side messages = %q, want %q", texts, want)
		}
	}

	if _, followUp := application.Pending(); len(followUp) != 0 {
		t.Fatalf("main follow-up queue = %+v, want empty", followUp)
	}
	if steering, _ := application.Pending(); len(steering) != 1 || steering[0].Text() != "main steer" {
		t.Fatalf("main steering queue = %+v, want the untouched steer", steering)
	}

	sideSteering, sideFollowUp := side.ClearQueue()
	if sideSteering != nil || sideFollowUp != nil {
		t.Fatalf("side queues = %+v/%+v, want both drained", sideSteering, sideFollowUp)
	}
}
