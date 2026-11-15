package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"pi-go/internal/compact"
	"pi-go/internal/provider"
	"pi-go/internal/session"
	"pi-go/internal/wire"
)

// errNoFailedAttempt means the branch tip is not a failed assistant message, so
// there is nothing for an overflow recovery to set aside.
var errNoFailedAttempt = errors.New("app: the branch tip is not a failed attempt")

// admitContext decides whether a prompt may be sent at all.
//
// It exists because autoCompact only runs after a turn that returned no error: an
// interrupted turn leaves the branch above the threshold, and the next prompt
// would go out one request away from the window — the failure the interrupted
// turn was in the middle of avoiding. Checking before sending closes that gap.
//
// The two lines it draws are the point. Close to the window compacts and sends
// anyway: the request still fits, and a summary that failed is not a reason to
// refuse work. Over the window compacts, and refuses to send when the request
// cannot be shrunk below it — one that cannot fit wastes a round trip and comes
// back as a vendor error nobody can act on. The estimate is the one the footer
// shows, so the number in the refusal is the number the reader saw.
//
// The pending prompts are counted on both sides of the check. They are not in
// the branch yet, so no compaction can shrink them, and leaving them out would
// let the one request this guard exists for — a prompt larger than the window —
// through unchecked.
func (a *App) admitContext(ctx context.Context, prompts []wire.Message, emit func(wire.AgentEvent) error) error {
	a.mu.Lock()
	settings, window := a.compaction, a.model.ContextWindow
	a.mu.Unlock()
	if !settings.Enabled || window <= 0 {
		return nil
	}
	pending := compact.EstimateMessages(prompts)
	tokens := a.contextTokens() + pending
	if !compact.ShouldCompact(tokens, window, settings) {
		return nil
	}
	over := tokens > window
	reason := wire.CompactionReasonThreshold
	if over {
		reason = wire.CompactionReasonOverflow
	}
	if _, err := a.compact(ctx, reason, "", emit, false); err != nil {
		if !over {
			// Close to the window is not over it: the request fits, and the
			// failure was already reported through compaction_end.
			return nil
		}
		return overflowRefusal(tokens, window, err)
	}
	// A compaction that cannot get below the window has nothing left to trade.
	// Sending anyway would repeat the failure the last turn ended on.
	if over {
		if shrunk := a.contextTokens() + pending; shrunk > window {
			return overflowRefusal(shrunk, window, nil)
		}
	}
	return nil
}

// recoverOverflow sets the failed attempt aside, summarizes, and repeats the turn
// once.
//
// The order matters. The failed attempt is omitted first: a message reading
// "error: prompt is too long" in front of the retried request would be answered,
// not ignored. Then the branch is summarized, which is the only thing that makes
// the retry smaller. Only then is the request repeated — with no new prompt,
// because the prompt that started the turn is still in the context.
//
// One attempt is the whole budget. A second failure means summarizing did not
// help, and trying again would spend another summary to learn the same thing.
func (a *App) recoverOverflow(ctx context.Context, first Result, emit func(wire.AgentEvent) error) (Result, error) {
	if err := a.omitLastAttempt(); err != nil {
		// The failed attempt is not where a retry could set it aside — it was
		// never recorded, or something followed it. Retrying anyway would put the
		// same message in front of the model again, so the turn keeps the vendor's
		// own words and the recovery does not start. Returning the sentinel to the
		// reader would name an internal invariant they cannot act on.
		return first, nil
	}
	if _, err := a.compact(ctx, wire.CompactionReasonOverflow, "", emit, true); err != nil {
		return a.failedOverflow(first, err, emit)
	}

	a.mu.Lock()
	a.refreshTools(a.ag)
	ag := a.ag
	a.mu.Unlock()

	retried, err := runTurn(ctx, ag, nil, a.persist, emit)
	merged := summarize(append(append([]wire.Message(nil), first.Messages...), retried.Messages...))
	if err != nil {
		return merged, err
	}
	if overflowed(merged) {
		return a.failedOverflow(merged, nil, emit)
	}
	return merged, nil
}

// omitLastAttempt removes the failed assistant message from the context. The
// entry stays in the file and a context edit names it, so the session records
// what happened without the model having to read it.
//
// The replacement is the literal JSON null, not nil: a nil replacement is how
// AppendContextEdit spells "no change", and the failed attempt would stay in
// the projection the retry is built from.
func (a *App) omitLastAttempt() error {
	a.mu.Lock()
	leaf, ok := a.manager.LeafEntry()
	a.mu.Unlock()
	if !ok || leaf.Type != session.TypeMessage {
		return errNoFailedAttempt
	}
	messages, err := session.EntryMessages(leaf)
	if err != nil || len(messages) != 1 || !provider.IsContextOverflow(messages[0]) {
		return errNoFailedAttempt
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if _, err := a.manager.AppendContextEdit(leaf.ID, json.RawMessage("null")); err != nil {
		return err
	}
	return a.refresh()
}

// failedOverflow reports a recovery that could not finish. The vendor's text
// stays on the failed message; this line says what to do about it.
func (a *App) failedOverflow(result Result, cause error, emit func(wire.AgentEvent) error) (Result, error) {
	_, window := a.ContextUsage()
	message := overflowGiveUp(window, cause).Error()
	if err := sendEvent(emit, wire.AgentEvent{
		Type:         wire.AgentEventCompactionEnd,
		Reason:       wire.CompactionReasonOverflow,
		Aborted:      wire.Ptr(false),
		WillRetry:    wire.Ptr(false),
		ErrorMessage: message,
	}); err != nil {
		return result, err
	}
	result.ErrorMessage = message
	return result, nil
}

// overflowRefusal words a request that was never sent: how far over the window it
// is, why it could not be shrunk when that is worth saying, and the two ways out.
// The numbers are the request itself, so they are still true here — this is the
// estimate the footer showed plus what the reader just typed.
//
// It reads as a sentence rather than a Go-style lowercase clause because every
// caller prints it verbatim: the transcript, the TUI error line, and the command
// line all show it as the turn's outcome, with nothing in front of it.
func overflowRefusal(tokens, window int, cause error) error {
	if cause == nil || errors.Is(cause, ErrNothingToCompact) || errors.Is(cause, ErrAlreadyCompacted) {
		// Nothing was tradeable. That is a fact about the session, not a reason
		// the request failed, so quoting it would offer a cause nobody can use.
		return fmt.Errorf("Context overflow: %d estimated tokens do not fit a %d-token window. Try a smaller prompt or a model with a larger context window", tokens, window)
	}
	return fmt.Errorf("Context overflow: %d estimated tokens do not fit a %d-token window, and summarizing the branch failed (%v). Try a smaller prompt or a model with a larger context window", tokens, window, cause)
}

// overflowGiveUp words a recovery that ran and did not work. It quotes no token
// count: by the time it is written the branch has been summarized, so its
// estimate describes the retry rather than the request that failed, and a
// sentence claiming three thousand tokens do not fit a two-hundred-thousand-token
// window would be worse than no number at all.
func overflowGiveUp(window int, cause error) error {
	switch {
	case cause == nil:
		return fmt.Errorf("Context overflow: the request did not fit the %d-token window even after summarizing. Try a smaller prompt or a model with a larger context window", window)
	case errors.Is(cause, ErrNothingToCompact), errors.Is(cause, ErrAlreadyCompacted):
		// A fact about the session, not a reason the request failed: the branch
		// had no range left to trade, so the prompt is the whole problem.
		return fmt.Errorf("Context overflow: the request does not fit the %d-token window and the context has no range left to summarize. Try a smaller prompt or a model with a larger context window", window)
	}
	return fmt.Errorf("Context overflow: the request does not fit the %d-token window and the summary failed (%v). Try a smaller prompt or a model with a larger context window", window, cause)
}

// overflowed reports whether the turn ended because the request was too large.
func overflowed(result Result) bool {
	return result.Assistant != nil && provider.IsContextOverflow(*result.Assistant)
}

// contextTokens reads the same estimate the footer shows.
func (a *App) contextTokens() int {
	tokens, _ := a.ContextUsage()
	return tokens
}
