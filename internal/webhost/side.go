package webhost

import (
	"context"
	"strings"
	"sync"

	"pi-go/internal/httpapi"
	"pi-go/internal/transcript"
	"pi-go/internal/wire"
)

// Compact runs one manual compaction of the conversation in view.
//
// The note is the reader's steer on the summary, and an empty one is passed
// through as empty: the kernel decides what a compaction with no instructions
// says, and trimming here would hide the difference between "no note" and "a
// note that happened to be blank".
func (h *Host) Compact(ctx context.Context, note string, emit func(wire.AgentEvent) error) error {
	_, err := h.kernel.App.Compact(ctx, strings.TrimSpace(note), emit)
	return err
}

// sideTracker remembers how many messages the current side conversation
// inherited, which is the one fact the kernel does not keep.
//
// It is here rather than in the kernel because only a reader needs it: the side
// agent holds the same messages either way, and a divider in a panel is a fact
// about what the panel draws. Recording it at OpenSide is also what makes the
// count survive a page reload, since the conversation does and the page does not.
type sideTracker struct {
	mu        sync.Mutex
	inherited int
}

func (t *sideTracker) set(inherited int) {
	t.mu.Lock()
	t.inherited = inherited
	t.mu.Unlock()
}

func (t *sideTracker) get() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.inherited
}

// Side reads the side conversation in the shape the panel draws.
//
// The blocks are the whole conversation the agent holds, inherited prefix
// included: the panel puts a divider at that point instead of dropping the
// messages, so a reader who asks the side "what did we just say" can see the
// same words the model was given.
func (h *Host) Side() (httpapi.SideView, error) {
	side := h.kernel.App.Side()
	if side == nil {
		return httpapi.SideView{Open: false, Blocks: []httpapi.TranscriptBlock{}}, nil
	}
	blocks := transcript.Replay(side.Messages())
	out := make([]httpapi.TranscriptBlock, 0, len(blocks))
	for _, block := range blocks {
		out = append(out, httpapi.TranscriptBlock{
			Role:   block.Role.String(),
			Name:   block.Name,
			Text:   block.Text,
			Note:   block.Note,
			Detail: block.Detail,
			Args:   block.Args,
			Result: block.Result,
			ID:     block.ID,
		})
	}
	return httpapi.SideView{Open: true, Inherited: h.side.get(), Blocks: out}, nil
}

// OpenSide takes the snapshot, unless one is already open.
//
// The guard is the difference between the terminal and a page. In the terminal
// /side means "start over", and BeginSide discarding the previous conversation
// is the feature; here the same call arrives whenever the panel is asked for,
// and throwing away what the reader was reading would be a redraw that costs a
// conversation.
func (h *Host) OpenSide() error {
	if h.kernel.App.Side() != nil {
		return nil
	}
	side, err := h.kernel.App.BeginSide()
	if err != nil {
		return err
	}
	// The snapshot is everything the side holds before its first turn, so the
	// inherited prefix is exactly what the fold makes of it now. Counted in
	// blocks rather than messages because that is the unit the panel draws: a
	// divider placed by message count would land in the middle of a tool card.
	h.side.set(len(transcript.Replay(side.Messages())))
	return nil
}

// CloseSide discards it. Nothing reached disk, so there is nothing to undo.
func (h *Host) CloseSide() error {
	h.kernel.App.EndSide()
	h.side.set(0)
	return nil
}
