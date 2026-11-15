package mcp

import (
	"context"
	"fmt"
	"sync"
	"time"

	"pi-go/internal/tool"
	"pi-go/internal/wire"
)

// methodProgress is the notification that keeps a slow call alive. The others a
// server may send — a tool list that changed, log records, resource updates —
// are handled in client.notify or dropped: a changed tool list is listed again
// (docs/mcp.md section 5), and the rest has nowhere to go.
const methodProgress = "notifications/progress"

// progressNote is one notifications/progress.
//
// ProgressToken decodes to any because the spec lets a server echo a number or a
// string, and a server given a string sometimes echoes the number it stands for.
// Everything is compared in its string form, which is the one shape that matches
// either echo.
type progressNote struct {
	ProgressToken any     `json:"progressToken"`
	Progress      float64 `json:"progress"`
	Total         float64 `json:"total"`
	Message       string  `json:"message"`
}

// callState is one tools/call while it is in flight, and the clock that decides
// when to give up on it.
//
// The budget is idle, not total: every progress notification restarts it, so a
// call that keeps reporting can run as long as it has work to do. That is the
// whole point — a build that takes ninety seconds is slow, not stuck, and the one
// thing this client cannot tell apart from stuck is silence.
type callState struct {
	token  string
	idle   time.Duration
	cancel context.CancelFunc
	update func(tool.Partial)

	mu sync.Mutex
	// timer is the current countdown. Each arm replaces it rather than reusing
	// it, because a timer callback captures the generation it was armed for and
	// a reset timer would keep the stale one.
	timer *time.Timer
	// gen invalidates a callback that has already been replaced: a notification
	// that arrives while the previous countdown is firing must not be cancelled
	// by it.
	gen     int
	done    bool
	expired bool
}

// arm starts the countdown. It runs with the mutex held, from either the first
// arm or a notification.
func (s *callState) arm() {
	s.gen++
	gen := s.gen
	if s.timer != nil {
		s.timer.Stop()
	}
	s.timer = time.AfterFunc(s.idle, func() { s.fire(gen) })
}

// fire is the countdown reaching zero. The generation check is what keeps a
// superseded timer from cancelling a call that has reported since.
func (s *callState) fire(gen int) {
	s.mu.Lock()
	if s.done || gen != s.gen {
		s.mu.Unlock()
		return
	}
	s.done = true
	s.expired = true
	cancel := s.cancel
	s.mu.Unlock()
	cancel()
}

// note is one progress notification for this call: restart the clock, then tell
// whoever is watching what the server said.
//
// The update runs outside the lock. It reaches the interface, which is allowed
// to be slow, and a slow repaint must not delay the next notification or the
// answer that is coming behind it.
func (s *callState) note(note progressNote) {
	s.mu.Lock()
	if s.done {
		s.mu.Unlock()
		return
	}
	s.arm()
	update := s.update
	s.mu.Unlock()

	if update == nil {
		return
	}
	text := progressText(note)
	update(tool.Partial{Content: []wire.Content{wire.TextContent(text)}})
}

// finish stops the clock and reports whether it had already run out. Afterwards
// the state is inert: a late notification finds done and is dropped.
func (s *callState) finish() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.done = true
	if s.timer != nil {
		s.timer.Stop()
	}
	return s.expired
}

// abandoned reports whether this call was given up on for silence. It is read
// after the call returns, to decide which error to report.
func (s *callState) abandoned() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.expired
}

// progressText is what one notification shows a person. The server's own message
// wins: it is the only part that says what is happening, while the numbers only
// say how far. A server that sent neither is dropped rather than shown as "0".
func progressText(note progressNote) string {
	if note.Message != "" {
		return note.Message
	}
	if note.Total > 0 {
		return fmt.Sprintf("%g/%g", note.Progress, note.Total)
	}
	return fmt.Sprintf("%g", note.Progress)
}

// shortDuration renders a budget the way the config writes it: whole seconds when
// the number is whole, the full form otherwise. "1m0s" is not how anyone writes
// sixty seconds, and a test that counts milliseconds has to stay readable.
func shortDuration(d time.Duration) string {
	if d%time.Second == 0 {
		return fmt.Sprintf("%ds", int(d/time.Second))
	}
	return d.String()
}
