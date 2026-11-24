package app

import (
	"encoding/json"

	"pi-go/internal/compact"
	"pi-go/internal/session"
	"pi-go/internal/wire"
)

// TokenStats sums token counts billed across a session.
type TokenStats struct {
	Input      int
	Output     int
	CacheRead  int
	CacheWrite int
	Total      int
}

// ContextStats reports the active branch's token estimate against the current
// model's context window. Tokens and Percent are nil immediately after a
// compaction until a post-compaction assistant response supplies real usage,
// because pre-compaction usage describes a context that no longer exists.
type ContextStats struct {
	Tokens        *int
	ContextWindow int
	Percent       *float64
}

// SessionStats aggregates message counts, billed usage across all session
// entries, and the active branch's current context usage.
type SessionStats struct {
	SessionFile       string
	SessionID         string
	UserMessages      int
	AssistantMessages int
	ToolCalls         int
	ToolResults       int
	TotalMessages     int
	Tokens            TokenStats
	Cost              float64
	ContextUsage      *ContextStats
}

// SessionStats computes usage totals across every entry in the session —
// including history that was compacted away — together with the active branch's
// current context window usage.
//
// Holding the lock across both the entry scan and the branch projection keeps
// concurrent queries during a streaming turn from racing message_end writes.
func (a *App) SessionStats() (SessionStats, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	stats := SessionStats{
		SessionFile: a.manager.File(),
		SessionID:   a.manager.ID(),
	}
	addUsage := func(u wire.Usage) {
		stats.Tokens.Input += u.Input
		stats.Tokens.Output += u.Output
		stats.Tokens.CacheRead += u.CacheRead
		stats.Tokens.CacheWrite += u.CacheWrite
		stats.Cost += u.Cost.Total
	}

	accounted := make(map[string]bool)
	for _, entry := range a.manager.Entries() {
		switch entry.Type {
		case session.TypeUsage, session.TypeBranchSummary, session.TypeCompaction:
			if len(entry.Usage) > 0 {
				var u wire.Usage
				if err := json.Unmarshal(entry.Usage, &u); err == nil {
					addUsage(u)
					if entry.Type == session.TypeUsage && entry.Kind == wire.SubagentToolName {
						accounted[entry.Note] = true
					}
				}
			}
		case session.TypeMessage:
			var probe struct {
				Role wire.Role `json:"role"`
			}
			if err := json.Unmarshal(entry.Message, &probe); err != nil {
				continue
			}
			stats.TotalMessages++
			switch probe.Role {
			case wire.RoleUser:
				stats.UserMessages++
			case wire.RoleToolResult:
				stats.ToolResults++
				var msg wire.Message
				if err := json.Unmarshal(entry.Message, &msg); err == nil && msg.Usage != nil {
					addUsage(*msg.Usage)
				}
			case wire.RoleAssistant:
				stats.AssistantMessages++
				var msg wire.Message
				if err := json.Unmarshal(entry.Message, &msg); err == nil {
					stats.ToolCalls += len(msg.ToolCalls())
					if msg.Usage != nil {
						addUsage(*msg.Usage)
					}
				}
			}
		}
	}
	if a.subagents != nil {
		updates, err := a.subagents.Completed(stats.SessionID, stats.SessionFile)
		if err != nil {
			return SessionStats{}, err
		}
		for _, update := range updates {
			if update.Usage != nil && !accounted[update.ID] {
				addUsage(*update.Usage)
				accounted[update.ID] = true
			}
		}
	}
	stats.Tokens.Total = stats.Tokens.Input + stats.Tokens.Output + stats.Tokens.CacheRead + stats.Tokens.CacheWrite

	window := a.model.ContextWindow
	if window > 0 {
		projection, err := a.manager.BuildProjection()
		if err != nil {
			return SessionStats{}, err
		}
		stats.ContextUsage = contextUsageStats(projection, a.manager.Path(""), window)
	}
	return stats, nil
}

// contextUsageStats computes the active branch's token estimate and percentage
// against window. Immediately after a compaction — before any assistant on the
// active branch has returned real post-compaction usage — both Tokens and
// Percent stay nil so callers never display stale pre-compaction sizes or an
// unanchored guess as exact usage.
func contextUsageStats(projection *session.Projection, path []session.Entry, window int) *ContextStats {
	compactionIndex := -1
	for i := len(path) - 1; i >= 0; i-- {
		if path[i].Type == session.TypeCompaction {
			compactionIndex = i
			break
		}
	}
	if compactionIndex >= 0 {
		hasUsage := make(map[string]bool)
		for _, pe := range projection.Entries {
			for _, msg := range pe.Messages {
				if msg.Role != wire.RoleAssistant || msg.Usage == nil {
					continue
				}
				if msg.StopReason == wire.StopAborted || msg.StopReason == wire.StopError {
					continue
				}
				if compact.ContextTokens(*msg.Usage) > 0 {
					hasUsage[pe.Source.ID] = true
					break
				}
			}
		}
		hasPostCompactionUsage := false
		for _, entry := range path[compactionIndex+1:] {
			if hasUsage[entry.ID] {
				hasPostCompactionUsage = true
				break
			}
		}
		if !hasPostCompactionUsage {
			return &ContextStats{
				Tokens:        nil,
				ContextWindow: window,
				Percent:       nil,
			}
		}
	}

	tokens := compact.EstimateProjection(projection, path).Tokens
	percent := (float64(tokens) / float64(window)) * 100
	return &ContextStats{
		Tokens:        &tokens,
		ContextWindow: window,
		Percent:       &percent,
	}
}
