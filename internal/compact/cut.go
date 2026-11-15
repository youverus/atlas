package compact

import (
	"pi-go/internal/session"
	"pi-go/internal/wire"
)

// CutPoint is where the kept range starts. TurnStartIndex points at the user
// message that opened the turn being split, or -1 when the cut lands on a turn
// start.
type CutPoint struct {
	FirstKeptEntryIndex int
	TurnStartIndex      int
	IsSplitTurn         bool
}

// FindCutPoint keeps roughly keepRecentTokens of the newest context by walking
// backwards from endIndex and cutting at the nearest valid boundary, where
// "valid" means a user or assistant message. Tool results are never a cut point:
// a tool result must stay behind its tool call, or the next request carries an
// orphaned block that both wire APIs reject.
func FindCutPoint(entries []session.ProjectedEntry, startIndex, endIndex, keepRecentTokens int) CutPoint {
	if startIndex < 0 {
		startIndex = 0
	}
	if endIndex > len(entries) {
		endIndex = len(entries)
	}
	if startIndex >= endIndex {
		return CutPoint{FirstKeptEntryIndex: startIndex, TurnStartIndex: -1}
	}

	cutPoints := make([]int, 0, endIndex-startIndex)
	for i := startIndex; i < endIndex; i++ {
		if isCutPoint(entries[i]) {
			cutPoints = append(cutPoints, i)
		}
	}
	if len(cutPoints) == 0 {
		return CutPoint{FirstKeptEntryIndex: startIndex, TurnStartIndex: -1}
	}

	accumulated := 0
	cut := cutPoints[0]
	for i := endIndex - 1; i >= startIndex; i-- {
		tokens := entryTokens(entries[i])
		// Metadata entries carry no context, so they neither add to the budget
		// nor become a cut point.
		if tokens == 0 {
			continue
		}
		accumulated += tokens
		if accumulated >= keepRecentTokens {
			cut = nearestCutPoint(cutPoints, i)
			break
		}
	}

	// Pull in adjacent entries that do not contribute context so the boundary
	// does not leave a dangling model or thinking-level change behind it. An
	// entry that was emptied by a context edit stops the walk: an omitted
	// attempt must stay summarized, or the split-turn check would treat it as
	// the start of a turn that was already accounted for.
	for cut > startIndex {
		previous := entries[cut-1]
		if previous.Source.Type == session.TypeCompaction || len(previous.Messages) > 0 || omitted(previous) {
			break
		}
		cut--
	}

	startsTurn := isTurnStart(entries[cut])
	turnStart := -1
	if !startsTurn {
		turnStart = findTurnStart(entries, cut, startIndex)
	}
	return CutPoint{
		FirstKeptEntryIndex: cut,
		TurnStartIndex:      turnStart,
		IsSplitTurn:         !startsTurn && turnStart >= 0,
	}
}

// nearestCutPoint returns the closest valid cut at or after index, falling back
// to the newest one when the tail contains no boundary. The fallback keeps a
// trailing run of tool results with its assistant call instead of collapsing to
// the oldest message.
func nearestCutPoint(cutPoints []int, index int) int {
	for _, candidate := range cutPoints {
		if candidate >= index {
			return candidate
		}
	}
	return cutPoints[len(cutPoints)-1]
}

func findTurnStart(entries []session.ProjectedEntry, entryIndex, startIndex int) int {
	for i := entryIndex; i >= startIndex; i-- {
		if isTurnStart(entries[i]) {
			return i
		}
	}
	return -1
}

// isCutPoint reports whether an entry may start the kept range.
func isCutPoint(entry session.ProjectedEntry) bool {
	if entry.Source.Type == session.TypeCompaction {
		return false
	}
	for _, message := range entry.Messages {
		switch message.Role {
		case wire.RoleUser, wire.RoleAssistant:
			return true
		}
	}
	return false
}

// isTurnStart reports whether an entry opens a turn. Session-only roles
// (bashExecution, custom, branchSummary, compactionSummary) are projected to user
// messages, so the role check covers them.
func isTurnStart(entry session.ProjectedEntry) bool {
	if entry.Source.Type == session.TypeCompaction {
		return false
	}
	for _, message := range entry.Messages {
		if message.Role == wire.RoleUser {
			return true
		}
	}
	return false
}

// omitted reports whether an entry would produce context but currently produces
// none, which is what a context edit with a null replacement leaves behind.
func omitted(entry session.ProjectedEntry) bool {
	if entry.Source.Type == session.TypeContextEdit || len(entry.Messages) > 0 {
		return false
	}
	messages, err := session.EntryMessages(entry.Source)
	return err == nil && len(messages) > 0
}

func entryTokens(entry session.ProjectedEntry) int {
	tokens := 0
	for _, message := range entry.Messages {
		tokens += EstimateTokens(message)
	}
	return tokens
}
