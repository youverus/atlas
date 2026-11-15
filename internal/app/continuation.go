package app

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"pi-go/internal/wire"
)

// ContinuationKind names the shape of an interrupted turn: where it stopped is
// the whole question a continuation answers.
type ContinuationKind string

const (
	// ContinuationNone means the last turn finished. Continuing it would ask
	// the model to keep talking about an answer it already gave.
	ContinuationNone ContinuationKind = "none"
	// ContinuationAnswer means the model had started answering and the turn was
	// cut off. The text it had said is in the session, so continuing picks up
	// after it instead of asking the same question again.
	ContinuationAnswer ContinuationKind = "answer"
	// ContinuationTools means a batch of tool calls was cut off. The results say
	// what each call had produced, so the next request already ends where the
	// answer belongs: no prompt is added.
	ContinuationTools ContinuationKind = "tools"
	// ContinuationPrompt means the prompt never got an answer. The request ends
	// with the question, so it is sent again as the context it already is.
	ContinuationPrompt ContinuationKind = "prompt"
)

// Continuation describes what /continue would resume from.
type Continuation struct {
	Kind ContinuationKind
	// Chars is how much of the interrupted answer the session kept, in runes.
	Chars int
	// Calls is how many tool calls were cut off.
	Calls int
	// Reason says why there is nothing to continue. It is empty when there is.
	Reason string
}

// Resumable reports whether a continuation would send anything.
func (c Continuation) Resumable() bool { return c.Kind != ContinuationNone }

// continuePrompt is the user turn a continuation sends when the model had
// already begun to answer.
//
// The continuation has to be a user turn rather than a bare assistant prefix,
// for two reasons that point the same way. A request ending with an assistant
// message is a prefill: one of the four APIs documents it, another rejects it
// outright, and atlas sends the same shape to all four. And the reader should
// be able to read what was sent: a line saying the turn goes on is a truthful
// record of the command they typed, where an invisible prefill would leave the
// session looking as though the model had spoken twice in a row by itself.
const continuePrompt = "Continue from where you were interrupted."

// ErrNothingToContinue reports a continuation with no interrupted turn to
// resume: the last turn in the conversation finished.
var ErrNothingToContinue = errors.New("app: the last turn finished, so there is nothing to continue")

// Continuation reports what a continuation would resume, without sending
// anything.
//
// It reads the transcript rather than a flag of its own. What is resumable is a
// fact about the last turn — it ended without an answer, or its answer was cut
// off, or its tools were stopped halfway — and a separate marker would be a
// second place for that fact to be wrong, in a file that is already the record
// of it.
func (a *App) Continuation() Continuation {
	a.mu.Lock()
	defer a.mu.Unlock()
	return continuationOf(a.messages)
}

// Continue resumes the turn that was interrupted, and reports what it produced.
//
// Nothing is replayed. The session already holds the interrupted turn: the
// prompt, whatever the model had said, and a result for every call that ran. A
// continuation is therefore one more request against that context — the shape
// the agent loop uses after a tool result, which is what the tail of an
// interrupted turn already is.
func (a *App) Continue(ctx context.Context, emit func(wire.AgentEvent) error) (Result, error) {
	a.mu.Lock()
	if a.busy {
		a.mu.Unlock()
		return Result{}, ErrBusy
	}
	if err := a.guardArchived(); err != nil {
		a.mu.Unlock()
		return Result{}, err
	}
	continuation := continuationOf(a.messages)
	if !continuation.Resumable() {
		a.mu.Unlock()
		return Result{}, ErrNothingToContinue
	}
	a.busy = true
	a.mu.Unlock()

	defer func() {
		a.mu.Lock()
		a.busy = false
		a.mu.Unlock()
	}()

	// Only an interrupted answer needs a prompt of its own. The other two shapes
	// already end where a request has to end: a tool result waits for the answer
	// it was gathered for, and an unanswered prompt is the request.
	var prompts []wire.Message
	if continuation.Kind == ContinuationAnswer {
		prompts = []wire.Message{wire.UserMessage(continuePrompt)}
	}

	if err := a.refreshPrompt(); err != nil {
		return Result{}, err
	}
	// A continuation is a request like any other, so it passes the same gate. An
	// interrupted turn can leave the branch above the threshold — that is the
	// turn this command is picking up — and sending it unchecked would spend the
	// one request that has to fit.
	if err := a.admitContext(ctx, prompts, emit); err != nil {
		return Result{}, err
	}

	a.mu.Lock()
	a.refreshTools(a.ag)
	ag := a.ag
	a.mu.Unlock()

	result, err := runTurn(ctx, ag, prompts, a.persist, emit)
	if err != nil {
		return result, err
	}
	if overflowed(result) {
		result, err = a.recoverOverflow(ctx, result, emit)
		if err != nil || result.ErrorMessage != "" {
			return result, err
		}
	}
	a.autoCompact(ctx, emit)
	return result, err
}

// continuationOf reads the tail of a transcript and says where it was cut off.
func continuationOf(messages []wire.Message) Continuation {
	if len(messages) == 0 {
		return Continuation{Kind: ContinuationNone, Reason: "this conversation has no turn to continue"}
	}
	last := messages[len(messages)-1]
	switch last.Role {
	case wire.RoleAssistant:
		if !unfinished(last.StopReason) {
			return Continuation{Kind: ContinuationNone, Reason: "the last turn finished"}
		}
		if text := last.Text(); strings.TrimSpace(text) != "" {
			return Continuation{Kind: ContinuationAnswer, Chars: utf8.RuneCountInString(text)}
		}
		// The turn stopped before the model said anything, so what it was
		// answering is the message before it.
		return continuationBefore(messages, len(messages)-1)
	case wire.RoleToolResult:
		return interruptedCalls(messages)
	case wire.RoleUser:
		return Continuation{Kind: ContinuationPrompt}
	}
	return Continuation{Kind: ContinuationNone, Reason: "the last turn finished"}
}

// unfinished reports whether a stop reason means the turn was cut off rather
// than completed.
//
// A failed turn counts. The agent loop returns as soon as a request fails, so a
// message carrying an error is never a finished answer: it is the same
// unfinished request as an aborted one, and the command that picks it up sends
// the same continuation.
func unfinished(reason wire.StopReason) bool {
	return reason == wire.StopAborted || reason == wire.StopError
}

// continuationBefore decides what an aborted message with no text was sitting
// on. An aborted message the provider wrote itself has no text even when the
// turn had done work: the calls it asked for are the ones before it.
func continuationBefore(messages []wire.Message, index int) Continuation {
	if index == 0 {
		return Continuation{Kind: ContinuationNone, Reason: "the last turn finished"}
	}
	switch messages[index-1].Role {
	case wire.RoleToolResult:
		return interruptedCalls(messages[:index])
	case wire.RoleUser:
		return Continuation{Kind: ContinuationPrompt}
	}
	return Continuation{Kind: ContinuationNone, Reason: "the last turn finished"}
}

// interruptedCalls counts the run of results at the end that were cut off. It is
// a run rather than a total: a batch is what the model asked for in one message,
// and results further back belong to calls that finished.
func interruptedCalls(messages []wire.Message) Continuation {
	calls := 0
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != wire.RoleToolResult {
			break
		}
		if messages[i].Interrupted {
			calls++
		}
	}
	if calls == 0 {
		return Continuation{Kind: ContinuationNone, Reason: "the last turn finished"}
	}
	return Continuation{Kind: ContinuationTools, Calls: calls}
}
