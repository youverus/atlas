package agent

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"pi-go/internal/provider"
	"pi-go/internal/wire"
)

// emitter serializes event delivery. Parallel tool execution emits from
// goroutines, so the sink must be guarded.
type emitter struct {
	mu  sync.Mutex
	fn  func(wire.AgentEvent) error
	err error
}

func (e *emitter) emit(event wire.AgentEvent) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.err != nil {
		return
	}
	if err := e.fn(event); err != nil {
		e.err = err
	}
}

// Run appends prompts, drives turns until the agent stops, and returns every
// message produced by this run. A nil emit is allowed.
func (a *Agent) Run(ctx context.Context, prompts []wire.Message, emit func(wire.AgentEvent) error) ([]wire.Message, error) {
	if emit == nil {
		emit = func(wire.AgentEvent) error { return nil }
	}
	em := &emitter{fn: emit}

	a.messages = append(a.messages, prompts...)
	produced := append([]wire.Message(nil), prompts...)

	em.emit(wire.AgentEvent{Type: wire.AgentEventStart})
	for _, prompt := range prompts {
		em.emit(messageEvent(wire.AgentEventMessageStart, prompt))
		em.emit(messageEvent(wire.AgentEventMessageEnd, prompt))
	}

	pending := a.steering()
	firstRequest := true
	for {
		hasMoreToolCalls := true
		for hasMoreToolCalls || len(pending) > 0 {
			if !firstRequest {
				em.emit(wire.AgentEvent{Type: wire.AgentEventTurnStart})
			}
			firstRequest = false

			for _, message := range pending {
				em.emit(messageEvent(wire.AgentEventMessageStart, message))
				em.emit(messageEvent(wire.AgentEventMessageEnd, message))
				a.messages = append(a.messages, message)
				produced = append(produced, message)
			}
			pending = nil

			assistant := a.streamTurn(ctx, em)
			a.messages = append(a.messages, assistant)
			produced = append(produced, assistant)

			if assistant.StopReason == wire.StopError || assistant.StopReason == wire.StopAborted {
				em.emit(turnEndEvent(assistant, nil))
				em.emit(wire.AgentEvent{Type: wire.AgentEventEnd, Messages: produced})
				return produced, finish(ctx, em)
			}

			calls := assistant.ToolCalls()
			var results []wire.Message
			terminate := false
			hasMoreToolCalls = false
			if len(calls) > 0 {
				results, terminate = a.dispatch(ctx, calls, assistant.StopReason, em)
				hasMoreToolCalls = !terminate
				for _, result := range results {
					a.messages = append(a.messages, result)
					produced = append(produced, result)
				}
			}

			em.emit(turnEndEvent(assistant, results))
			if ctx.Err() == nil {
				pending = a.steering()
			}
		}

		if ctx.Err() != nil {
			break
		}
		if followUp := a.followUp(); len(followUp) > 0 {
			pending = followUp
			continue
		}
		break
	}

	em.emit(wire.AgentEvent{Type: wire.AgentEventEnd, Messages: produced})
	return produced, finish(ctx, em)
}

// streamTurn performs one provider request and emits the message lifecycle.
func (a *Agent) streamTurn(ctx context.Context, em *emitter) wire.Message {
	turnStarted := time.Now()
	request := provider.Request{
		Model:    a.cfg.Model,
		Messages: a.requestMessages(),
		Options:  provider.Options{ThinkingLevel: a.cfg.ThinkingLevel},
	}
	var stream provider.Stream
	err := ctx.Err()
	if err == nil {
		stream, err = a.cfg.Provider.Stream(ctx, request)
	}
	if err != nil {
		// Nothing was assembled: the request never became a stream.
		message := a.failure(nil, failureReason(ctx), err.Error())
		stampAssistantTiming(&message, turnStarted)
		em.emit(messageEvent(wire.AgentEventMessageStart, message))
		em.emit(messageEvent(wire.AgentEventMessageEnd, message))
		return message
	}
	defer stream.Close()

	started := false
	var final *wire.Message
	// assembled is the last message the provider had put together. It is what a
	// stream that ends without a terminal event leaves behind, and it is the only
	// record of an answer that was halfway said when the reader stopped it.
	var assembled *wire.Message
	for {
		event, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			message := a.failure(assembled, failureReason(ctx), err.Error())
			stampAssistantTiming(&message, turnStarted)
			if !started {
				em.emit(messageEvent(wire.AgentEventMessageStart, message))
			}
			em.emit(messageEvent(wire.AgentEventMessageEnd, message))
			return message
		}

		switch event.Type {
		case wire.EventStart:
			started = true
			partial := a.emptyAssistant()
			if event.Partial != nil {
				partial = *event.Partial
			}
			assembled = &partial
			em.emit(messageEvent(wire.AgentEventMessageStart, partial))
		case wire.EventDone:
			final = event.Message
		case wire.EventError:
			final = event.Error
		default:
			partial := a.emptyAssistant()
			if event.Partial != nil {
				partial = *event.Partial
			}
			assembled = &partial
			assistantEvent := event
			em.emit(wire.AgentEvent{
				Type:           wire.AgentEventMessageUpdate,
				Message:        &partial,
				AssistantEvent: &assistantEvent,
			})
		}
		if final != nil {
			break
		}
	}

	if final == nil {
		message := a.failure(assembled, wire.StopError, "provider stream ended without a terminal event")
		stampAssistantTiming(&message, turnStarted)
		if !started {
			em.emit(messageEvent(wire.AgentEventMessageStart, message))
		}
		em.emit(messageEvent(wire.AgentEventMessageEnd, message))
		return message
	}
	stampAssistantTiming(final, turnStarted)
	if !started {
		em.emit(messageEvent(wire.AgentEventMessageStart, *final))
	}
	em.emit(messageEvent(wire.AgentEventMessageEnd, *final))
	return *final
}

func stampAssistantTiming(message *wire.Message, started time.Time) {
	if message == nil {
		return
	}
	message.Timestamp = wire.Now()
	if message.DurationMs == nil {
		message.DurationMs = wire.Ptr(time.Since(started).Milliseconds())
	}
}

func (a *Agent) requestMessages() []wire.Message {
	return wire.NormalizeContext(a.cfg.SystemPrompt, a.registry.Declarations(), a.messages)
}

func (a *Agent) emptyAssistant() wire.Message {
	return wire.Message{
		Role:       wire.RoleAssistant,
		Content:    wire.NewBlocks(),
		API:        a.cfg.Model.API,
		Provider:   a.cfg.Model.Provider,
		Model:      a.cfg.Model.ID,
		Timestamp:  wire.Now(),
		StopReason: wire.StopPending,
	}
}

// failureReason distinguishes a run the caller stopped from one that failed.
// pi draws the same line, and a client that reports "aborted" as an error would
// show a user who pressed escape a provider outage instead.
func failureReason(ctx context.Context) wire.StopReason {
	if ctx.Err() != nil {
		return wire.StopAborted
	}
	return wire.StopError
}

// failure builds the message that ends a run whose turn did not finish.
//
// What the provider had already assembled is kept, because a reader who watched
// an answer arrive and then pressed escape has that answer on their screen: a
// session that dropped it would disagree with the screen, and the next request
// would ask the model to say the same thing again from the start.
//
// Thinking and tool calls are dropped, and not out of caution. A thinking block
// is only accepted back by the API with the signature that arrives at the end of
// it, and a tool call is only meaningful together with the result that never
// came; keeping either would build a request the endpoint rejects. Text is the
// only part of an answer that stands on its own.
func (a *Agent) failure(partial *wire.Message, reason wire.StopReason, message string) wire.Message {
	failed := a.emptyAssistant()
	if partial != nil {
		for _, block := range partial.Content.Items() {
			if block.Type == wire.ContentText && block.Text != "" {
				failed.Content.Append(block)
			}
		}
	}
	failed.StopReason = reason
	failed.ErrorMessage = message
	return failed
}

func (a *Agent) steering() []wire.Message {
	if a.cfg.GetSteeringMessages == nil {
		return nil
	}
	return a.cfg.GetSteeringMessages()
}

func (a *Agent) followUp() []wire.Message {
	if a.cfg.GetFollowUpMessages == nil {
		return nil
	}
	return a.cfg.GetFollowUpMessages()
}

func messageEvent(eventType string, message wire.Message) wire.AgentEvent {
	copied := message.Clone()
	return wire.AgentEvent{Type: eventType, Message: &copied}
}

func turnEndEvent(message wire.Message, results []wire.Message) wire.AgentEvent {
	copied := message.Clone()
	if results == nil {
		results = []wire.Message{}
	}
	return wire.AgentEvent{Type: wire.AgentEventTurnEnd, Message: &copied, ToolResults: results}
}

func finish(ctx context.Context, em *emitter) error {
	if em.err != nil {
		return em.err
	}
	return ctx.Err()
}
