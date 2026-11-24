package webhost

import (
	"context"
	"strings"

	"pi-go/internal/httpapi"
	"pi-go/internal/transcript"
	"pi-go/internal/wire"
)

func (h *Host) Compact(ctx context.Context, note string, emit func(wire.AgentEvent) error) error {
	_, err := h.activeSlot().live.App.Compact(ctx, strings.TrimSpace(note), emit)
	return err
}

// Side reads the side conversation in the shape the panel draws.
func (h *Host) Side() (httpapi.SideView, error) {
	slot := h.activeSlot()
	side := slot.live.App.Side()
	if side == nil {
		return httpapi.SideView{Open: false, Blocks: []httpapi.TranscriptBlock{}}, nil
	}
	blocks := transcript.Replay(side.Messages())
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
			ErrorCode:    block.ErrorCode,
			Timestamp:    block.Timestamp,
			DurationMs:   block.DurationMs,
			TokensPerSec: block.TokensPerSec,
		})
	}
	return httpapi.SideView{Open: true, Inherited: slot.getSideInherited(), Blocks: out}, nil
}

// OpenSide takes the snapshot, unless one is already open.
func (h *Host) OpenSide() error {
	slot := h.activeSlot()
	if slot.live.App.Side() != nil {
		return nil
	}
	side, err := slot.live.App.BeginSide()
	if err != nil {
		return err
	}
	slot.setSideInherited(len(transcript.Replay(side.Messages())))
	return nil
}

// CloseSide discards it. Nothing reached disk, so there is nothing to undo.
func (h *Host) CloseSide() error {
	slot := h.activeSlot()
	slot.live.App.EndSide()
	slot.setSideInherited(0)
	return nil
}
