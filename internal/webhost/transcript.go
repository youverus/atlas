package webhost

import (
	"pi-go/internal/httpapi"
	"pi-go/internal/transcript"
)

// Transcript replays the conversation in view.
//
// The fold is the kernel's: two front ends must agree on what counts as one
// tool call, so a window that re-implemented it would drift from the terminal
// the first time either changed. This is the translation into the transport's
// shape and nothing else.
func (h *Host) Transcript() ([]httpapi.TranscriptBlock, error) {
	blocks := transcript.Replay(h.activeSlot().live.App.Messages())
	out := make([]httpapi.TranscriptBlock, 0, len(blocks))
	for _, block := range blocks {
		out = append(out, httpapi.TranscriptBlock{
			Role:         block.Role.String(),
			Name:         block.Name,
			Text:         block.Text,
			Note:         block.Note,
			Detail:       block.Detail,
			Args:         block.Args,
			Result:       block.Result,
			ID:           block.ID,
			Subagent:     block.Subagent,
			Artifact:     block.Artifact,
			ErrorCode:    block.ErrorCode,
			Timestamp:    block.Timestamp,
			DurationMs:   block.DurationMs,
			TokensPerSec: block.TokensPerSec,
		})
	}
	return out, nil
}
