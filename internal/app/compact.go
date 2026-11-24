package app

import (
	"context"
	"encoding/json"
	"errors"

	"pi-go/internal/compact"
	"pi-go/internal/provider"
	"pi-go/internal/session"
	"pi-go/internal/wire"
)

var (
	// ErrAlreadyCompacted reports a manual compaction on a branch whose tip is
	// already a checkpoint. Compacting it would summarize the summary.
	ErrAlreadyCompacted = errors.New("app: already compacted")
	// ErrNothingToCompact reports a manual compaction that would leave nothing
	// in the summarized range.
	ErrNothingToCompact = errors.New("app: nothing to compact (session too small)")
)

// CompactionSettings returns the settings automatic compaction uses.
func (a *App) CompactionSettings() compact.Settings {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.compaction
}

// SetAutoCompaction enables or disables threshold compaction. The setting is not
// persisted: pi keeps it in a settings file, and this kernel has none yet, so a
// resumed conversation starts from the configured default again.
func (a *App) SetAutoCompaction(enabled bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.compaction.Enabled = enabled
}

// Compact summarizes the branch and appends a checkpoint, returning what it
// recorded. It needs an idle conversation: abort a running turn first.
//
// The flow is event-visible: compaction_start first, then compaction_end with
// either the result or the error. A client renders one protocol, whether the
// compaction was asked for or triggered by the threshold.
func (a *App) Compact(ctx context.Context, instructions string, emit func(wire.AgentEvent) error) (wire.CompactionResult, error) {
	a.mu.Lock()
	if a.busy {
		a.mu.Unlock()
		return wire.CompactionResult{}, ErrBusy
	}
	if err := a.guardArchived(); err != nil {
		a.mu.Unlock()
		return wire.CompactionResult{}, err
	}
	a.busy = true
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.busy = false
		a.mu.Unlock()
	}()

	return a.compact(ctx, wire.CompactionReasonManual, instructions, emit, false)
}

// compact is the shared body of the manual, threshold, and overflow paths.
//
// Manual compaction announces itself before checking anything, so a client shows
// the attempt even when there was nothing to summarize. Overflow does the same:
// it is a recovery the reader asked to watch. The threshold path has already
// decided, so it starts only once a preparation exists, and it treats "nothing to
// compact" as a stale estimate rather than a failure.
//
// willRetry is what the compaction_end event promises: a turn will be repeated
// because of this summary. Only the overflow path can say yes, and only when the
// caller really does retry.
func (a *App) compact(ctx context.Context, reason, instructions string, emit func(wire.AgentEvent) error, willRetry bool) (wire.CompactionResult, error) {
	announce := reason != wire.CompactionReasonThreshold
	if announce {
		if err := sendEvent(emit, wire.AgentEvent{Type: wire.AgentEventCompactionStart, Reason: reason}); err != nil {
			return wire.CompactionResult{}, err
		}
	}

	result, err := a.runCompaction(ctx, reason, instructions, emit, announce, willRetry)
	if err == nil {
		return result, nil
	}
	if errors.Is(err, ErrNothingToCompact) || errors.Is(err, ErrAlreadyCompacted) {
		switch reason {
		case wire.CompactionReasonThreshold:
			return wire.CompactionResult{}, nil
		case wire.CompactionReasonOverflow:
			// The caller has the better sentence: the context does not fit and
			// there is nothing left to trade for room. A generic
			// "auto-compaction failed" would hide that.
			return wire.CompactionResult{}, err
		}
	}
	return wire.CompactionResult{}, a.reportCompactionFailure(emit, reason, err)
}

// runCompaction prepares, summarizes, records, and reports one compaction. When
// the start event was not announced yet it is emitted here, because only the
// preparation knows whether a compaction happens at all.
func (a *App) runCompaction(ctx context.Context, reason, instructions string, emit func(wire.AgentEvent) error, announced, willRetry bool) (wire.CompactionResult, error) {
	preparation, settings, summarizer, leaf, err := a.prepareCompaction()
	if err != nil {
		return wire.CompactionResult{}, err
	}
	if preparation == nil {
		if leaf != nil && leaf.Type == session.TypeCompaction {
			return wire.CompactionResult{}, ErrAlreadyCompacted
		}
		return wire.CompactionResult{}, ErrNothingToCompact
	}
	if !announced {
		if err := sendEvent(emit, wire.AgentEvent{Type: wire.AgentEventCompactionStart, Reason: reason}); err != nil {
			return wire.CompactionResult{}, err
		}
	}

	result, err := summarizer.Run(ctx, preparation, settings, instructions)
	if err != nil {
		return wire.CompactionResult{}, err
	}
	details, err := result.Details()
	if err != nil {
		return wire.CompactionResult{}, err
	}
	usage, err := encodeCompactionUsage(result.Usage)
	if err != nil {
		return wire.CompactionResult{}, err
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if _, err := a.manager.AppendCompaction(result.Summary, result.FirstKeptEntryID, result.TokensBefore, session.CompactionOptions{
		Details:  details,
		Usage:    usage,
		Provider: summarizer.Model.Provider,
		Model:    summarizer.Model.ID,
	}); err != nil {
		return wire.CompactionResult{}, err
	}
	if err := a.refresh(); err != nil {
		return wire.CompactionResult{}, err
	}

	// The reported size is a plain sum over the rebuilt context, the number pi
	// reports: the usage records still in it describe the pre-compaction context
	// and must not anchor it.
	projection, err := a.manager.BuildProjection()
	if err != nil {
		return wire.CompactionResult{}, err
	}
	estimatedAfter := compact.EstimateMessages(projection.Messages)
	recorded := wire.CompactionResult{
		Summary:              result.Summary,
		FirstKeptEntryID:     result.FirstKeptEntryID,
		TokensBefore:         result.TokensBefore,
		EstimatedTokensAfter: &estimatedAfter,
		Details:              details,
	}
	if result.Usage != (wire.Usage{}) {
		recordedUsage := result.Usage
		recorded.Usage = &recordedUsage
	}

	payload, err := json.Marshal(recorded)
	if err != nil {
		return wire.CompactionResult{}, err
	}
	if err := sendEvent(emit, wire.AgentEvent{
		Type:      wire.AgentEventCompactionEnd,
		Reason:    reason,
		Result:    payload,
		Aborted:   wire.Ptr(false),
		WillRetry: wire.Ptr(willRetry),
	}); err != nil {
		return wire.CompactionResult{}, err
	}
	return recorded, nil
}

// prepareCompaction snapshots the knobs and decides what a compaction would do.
// The lock is released before the model call: reads like Messages have to stay
// available while a summary is being generated.
func (a *App) prepareCompaction() (*compact.Preparation, compact.Settings, *compact.Summarizer, *session.Entry, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	settings := a.compaction
	path := a.manager.Path("")
	summarizer := &compact.Summarizer{
		Provider: a.provider,
		Model:    a.model,
		Options: provider.Options{
			ThinkingLevel: a.thinking,
			SessionID:     a.manager.ID(),
		},
	}
	preparation, err := compact.Prepare(path, settings)
	if err != nil {
		return nil, settings, nil, nil, err
	}
	var leaf *session.Entry
	if len(path) > 0 {
		leaf = &path[len(path)-1]
	}
	return preparation, settings, summarizer, leaf, nil
}

// reportCompactionFailure emits compaction_end for a failed attempt and returns
// the error the caller should see. An aborted attempt carries no message: the
// cancellation is the reason, not a failure of the model.
func (a *App) reportCompactionFailure(emit func(wire.AgentEvent) error, reason string, cause error) error {
	aborted := errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded)
	message := ""
	if !aborted {
		label := "Compaction failed"
		if reason == wire.CompactionReasonThreshold {
			label = "Auto-compaction failed"
		}
		message = label + ": " + cause.Error()
	}
	if err := sendEvent(emit, wire.AgentEvent{
		Type:         wire.AgentEventCompactionEnd,
		Reason:       reason,
		Aborted:      wire.Ptr(aborted),
		WillRetry:    wire.Ptr(false),
		ErrorMessage: message,
	}); err != nil {
		return err
	}
	return cause
}

// autoCompact compacts in place when the context is close to the window. It runs
// after a completed turn and never fails it: the turn already produced its
// answer, and a compaction problem is reported through compaction_end instead.
func (a *App) autoCompact(ctx context.Context, emit func(wire.AgentEvent) error) {
	a.mu.Lock()
	settings := a.compaction
	window := a.model.ContextWindow
	path := a.manager.Path("")
	a.mu.Unlock()
	if !settings.Enabled || window <= 0 {
		return
	}
	projection, err := a.manager.BuildProjection()
	if err != nil {
		return
	}
	if !compact.ShouldCompact(compact.EstimateProjection(projection, path).Tokens, window, settings) {
		return
	}
	_, _ = a.compact(ctx, wire.CompactionReasonThreshold, "", emit, false)
}

// sendEvent forwards one event. A nil sink is a caller that does not want
// events, not an error.
func sendEvent(emit func(wire.AgentEvent) error, event wire.AgentEvent) error {
	if emit == nil {
		return nil
	}
	return emit(event)
}

// encodeCompactionUsage stores the usage the summary calls reported. A zero
// usage is omitted rather than recorded as zeroes.
func encodeCompactionUsage(usage wire.Usage) (json.RawMessage, error) {
	if usage == (wire.Usage{}) {
		return nil, nil
	}
	encoded, err := json.Marshal(usage)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}
