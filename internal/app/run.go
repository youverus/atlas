package app

import (
	"context"
	"fmt"
	"sync"

	"pi-go/internal/agent"
	"pi-go/internal/session"
	"pi-go/internal/wire"
)

// Result describes what one prompt produced.
type Result struct {
	// Messages holds every message this call appended, in order: the prompt,
	// assistant messages, tool results, and any steering messages delivered
	// while it ran.
	Messages []wire.Message
	// Assistant is the last assistant message, or nil when the run produced
	// none, for example when it was cancelled before the first response.
	Assistant *wire.Message
	// Usage sums the usage of every assistant message in this call.
	Usage wire.Usage
	// StopReason is the last assistant message's stop reason.
	StopReason wire.StopReason
	// ErrorMessage is the provider or kernel error behind a failed turn.
	ErrorMessage string
}

// Text returns the assistant text, or "" when there is none.
func (r Result) Text() string {
	if r.Assistant == nil {
		return ""
	}
	return r.Assistant.Text()
}

// QueueMode selects how many queued messages the agent receives per poll.
type QueueMode string

const (
	// QueueModeOneAtATime delivers one message per poll, which is pi's default:
	// delivering everything at once would collapse several steering messages
	// into one prompt and hide their order from the model.
	QueueModeOneAtATime QueueMode = "one-at-a-time"
	// QueueModeAll delivers every queued message in one poll.
	QueueModeAll QueueMode = "all"
)

// ParseQueueMode validates a mode name. Empty means the default.
func ParseQueueMode(mode string) (QueueMode, error) {
	switch QueueMode(mode) {
	case QueueModeOneAtATime, QueueModeAll:
		return QueueMode(mode), nil
	case "":
		return QueueModeOneAtATime, nil
	}
	return "", fmt.Errorf("app: unknown queue mode %q", mode)
}

// messageQueue buffers user messages that arrive while the agent is busy.
//
// The queue outlives a single turn, matching pi: a message queued while idle is
// delivered when the next turn starts, and one that arrives during the final
// turn is still delivered before the agent stops polling. Clearing is therefore
// explicit, not a side effect of a turn ending.
type messageQueue struct {
	mu       sync.Mutex
	mode     QueueMode
	messages []wire.Message
}

func (q *messageQueue) enqueue(message wire.Message) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.messages = append(q.messages, message)
}

// drain removes and returns the messages the agent should see next.
func (q *messageQueue) drain() []wire.Message {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.messages) == 0 {
		return nil
	}
	count := len(q.messages)
	if q.mode != QueueModeAll {
		count = 1
	}
	drained := append([]wire.Message(nil), q.messages[:count]...)
	q.messages = append([]wire.Message(nil), q.messages[count:]...)
	return drained
}

// clear drops everything queued and returns it, so a caller can report what was
// thrown away.
func (q *messageQueue) clear() []wire.Message {
	q.mu.Lock()
	defer q.mu.Unlock()
	cleared := q.messages
	q.messages = nil
	return cleared
}

func (q *messageQueue) pending() []wire.Message {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]wire.Message(nil), q.messages...)
}

func (q *messageQueue) len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.messages)
}

func (q *messageQueue) setMode(mode QueueMode) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.mode = mode
}

func (q *messageQueue) currentMode() QueueMode {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.mode == "" {
		return QueueModeOneAtATime
	}
	return q.mode
}

// Ask runs one prompt on the main conversation and returns when the agent stops.
//
// The prompt and every message produced afterwards are written to the session as
// the kernel finishes them, so an interrupted turn keeps what it completed.
// Cancelling ctx aborts the turn; the result still carries what was produced, and
// err reports the cancellation.
func (a *App) Ask(ctx context.Context, prompt wire.Message, emit func(wire.AgentEvent) error) (Result, error) {
	a.mu.Lock()
	if a.busy {
		a.mu.Unlock()
		return Result{}, ErrBusy
	}
	// An archived conversation takes no new turns: its file is a record the
	// user put away, and a prompt would append to it behind the picker's back.
	if err := a.guardArchived(); err != nil {
		a.mu.Unlock()
		return Result{}, err
	}
	a.busy = true
	a.mu.Unlock()

	defer func() {
		a.mu.Lock()
		a.busy = false
		a.mu.Unlock()
	}()

	// The instructions are read again for every turn, so a project file that
	// changed or a skill that was installed takes effect on this question
	// rather than on the next restart of the session. A file that cannot be
	// read stops the turn here: the model would otherwise answer without the
	// instructions the reader believes it was given.
	if err := a.refreshPrompt(); err != nil {
		return Result{}, err
	}

	// The prompt must not be sent into a context that no longer holds it. This
	// also catches the turn that was interrupted before its trailing compaction
	// could run.
	if err := a.admitContext(ctx, []wire.Message{prompt}, emit); err != nil {
		return Result{}, err
	}

	a.mu.Lock()
	a.refreshTools(a.ag)
	ag := a.ag
	a.mu.Unlock()

	result, err := runTurn(ctx, ag, []wire.Message{prompt}, a.persist, emit)
	if err != nil {
		return result, err
	}
	if overflowed(result) {
		// The request did not fit. Summarize and ask once more, with the failed
		// attempt set aside.
		result, err = a.recoverOverflow(ctx, result, emit)
		if err != nil || result.ErrorMessage != "" {
			// A recovery that ended in its own sentence has already compacted as
			// far as it can; a second threshold pass would report the same thing.
			return result, err
		}
	}
	// The context may now be past the threshold; compacting here keeps the next
	// turn from starting one request away from the window.
	a.autoCompact(ctx, emit)
	return result, err
}

// Steer queues a message for the running turn, or for the next one when the app
// is idle.
//
// The agent injects it before its next request, so a turn stuck in a tool still
// sees it.
func (a *App) Steer(message wire.Message) {
	a.steering.enqueue(message)
}

// FollowUp queues a message that is delivered only when the agent would
// otherwise stop, which is what makes it a follow-up rather than a correction.
func (a *App) FollowUp(message wire.Message) {
	a.followUps.enqueue(message)
}

// ClearQueue drops both queues and returns what was waiting, so a caller can
// echo the discarded text back.
func (a *App) ClearQueue() (steering, followUp []wire.Message) {
	return a.steering.clear(), a.followUps.clear()
}

// PendingCount reports how many user messages are waiting in both queues.
func (a *App) PendingCount() int {
	return a.steering.len() + a.followUps.len()
}

// Pending returns the queued steering and follow-up text, in delivery order.
func (a *App) Pending() (steering, followUp []wire.Message) {
	return a.steering.pending(), a.followUps.pending()
}

// SteeringMode returns the current steering queue mode.
func (a *App) SteeringMode() QueueMode { return a.steering.currentMode() }

// FollowUpMode returns the current follow-up queue mode.
func (a *App) FollowUpMode() QueueMode { return a.followUps.currentMode() }

// SetSteeringMode changes how many steering messages the agent receives per
// poll.
func (a *App) SetSteeringMode(mode QueueMode) { a.steering.setMode(mode) }

// SetFollowUpMode changes how many follow-up messages the agent receives per
// poll.
func (a *App) SetFollowUpMode(mode QueueMode) { a.followUps.setMode(mode) }

// persist records one finished message. The system prompt is synthesized per
// request, so it is never stored.
func (a *App) persist(message wire.Message) error {
	if message.Role == wire.RoleSystem {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.nameFromFirstMessage(message)
	if _, err := a.manager.AppendMessage(message); err != nil {
		return err
	}
	a.messages = append(a.messages, message)
	return nil
}

// nameFromFirstMessage names an unnamed session after the first thing the user
// said.
//
// Every user message reaches this function, from the command line and from the
// window alike, which is why the name is decided here: a conversation started
// either way ends up with the same label. It runs only while the session has no
// name, so a label the reader is watching never changes under them, and /rename
// stays the way to override it.
//
// The name is written before the message it describes. A label recorded after
// it would sit between the prompt and its answer, and the session tree would
// then read as though the reader had renamed the conversation mid-exchange.
//
// The write is best effort on purpose. A name is cosmetic, and the turn that
// produced this message must not be cancelled because a label could not be
// recorded.
func (a *App) nameFromFirstMessage(message wire.Message) {
	if message.Role != wire.RoleUser || a.manager.Name() != "" {
		return
	}
	if name := session.DeriveName(message.Text()); name != "" {
		_, _ = a.manager.AppendSessionInfo(name)
	}
}

// runTurn drives prompts through ag and reports what it produced.
//
// An empty prompt list means "continue": the agent performs one request against
// the context it already holds. That is what an overflow recovery needs, because
// the prompt that started the failed turn is still in it — appending it again
// would ask the same question twice.
//
// Persistence happens inside the event sink, which is where pi appends from its
// message_end listener: every message reaches the session exactly once, whenever
// the kernel decides it is finished. A persistence failure cancels the turn,
// because letting tools run whose results cannot be recorded would leave the
// session describing a conversation that never happened.
func runTurn(
	ctx context.Context,
	ag *agent.Agent,
	prompts []wire.Message,
	persist func(wire.Message) error,
	emit func(wire.AgentEvent) error,
) (Result, error) {
	for i := range prompts {
		// A caller that builds a message by hand should still get a usable
		// timestamp in the session instead of the zero value.
		if prompts[i].Role == "" {
			prompts[i].Role = wire.RoleUser
		}
		if prompts[i].Timestamp == 0 {
			prompts[i].Timestamp = wire.Now()
		}
	}

	turnCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	sink := func(event wire.AgentEvent) error {
		if event.Type == wire.AgentEventMessageEnd && event.Message != nil {
			if err := persist(*event.Message); err != nil {
				cancel()
				return err
			}
		}
		if emit == nil {
			return nil
		}
		if err := emit(event); err != nil {
			cancel()
			return err
		}
		return nil
	}

	messages, err := ag.Run(turnCtx, prompts, sink)
	return summarize(messages), err
}

// summarize picks the final assistant message out of a run's output.
func summarize(messages []wire.Message) Result {
	result := Result{Messages: messages}
	for i := range messages {
		message := messages[i]
		if message.Role != wire.RoleAssistant {
			continue
		}
		if message.Usage != nil {
			result.Usage.Add(*message.Usage)
		}
		final := message.Clone()
		result.Assistant = &final
		result.StopReason = final.StopReason
		result.ErrorMessage = final.ErrorMessage
	}
	return result
}
