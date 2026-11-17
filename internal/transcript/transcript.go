// Package transcript folds the agent's event stream into the blocks a reader
// sees, and holds the state that is not itself text: which block is open, which
// stretches are folded, and how much of the history is revealed.
//
// It is the model half of an interface and not a renderer. Nothing here knows
// what a cell is, so the same fold can be drawn twice — once as text in the
// TUI, once as components in a window — and "what counts as one tool call" has
// one answer instead of one per frontend.
//
// Two interfaces is the cost this package exists to keep from doubling. The
// TUI renders Blocks into rows; anything else reads the same Blocks and draws
// them its own way. Neither may re-implement the fold.
//
// Only the standard library and wire are imported: this is a kernel package,
// and the dependency gate holds every kernel package to that.
package transcript

import (
	"strings"
	"time"
)

// Role is what a block is for. It decides the marker, the indent, and the
// style, and nothing else in the interface branches on it.
type Role int

const (
	// RoleUser is something the reader typed.
	RoleUser Role = iota
	// RoleAnswer is what the model said.
	RoleAnswer
	// RoleThinking is the reasoning a model streamed before its answer.
	RoleThinking
	// RoleTool is one tool call and its outcome.
	RoleTool
	// RoleNotice is the interface talking about itself: compaction, a resumed
	// session, a switched view.
	RoleNotice
	// RoleError is a failure worth colouring.
	RoleError
)

// String names the role. It exists for the log line and for any frontend that
// speaks JSON: an integer is a stable encoding and a terrible contract, because
// inserting a role in the middle of the list silently renames every one after
// it. Consumers key on these names.
func (r Role) String() string {
	switch r {
	case RoleUser:
		return "user"
	case RoleAnswer:
		return "answer"
	case RoleThinking:
		return "thinking"
	case RoleTool:
		return "tool"
	case RoleNotice:
		return "notice"
	case RoleError:
		return "error"
	}
	return "unknown"
}

// Block is one item in a transcript. Blocks are appended and updated in place;
// two of them can be open at once while a call runs inside a streamed answer.
type Block struct {
	Role Role
	// Name is the tool name, for RoleTool.
	Name string
	// Text is the body. It is appended to while the block is open.
	Text string
	// Progress is what a running call last reported. It replaces itself in
	// place rather than accumulating: a call that reports a hundred times is
	// still one line, because the line is not the report, it is the sign that
	// the call is alive. Cleared when the result lands, which is where the
	// outcome takes the same slot.
	Progress string
	// Note is appended to the marker line: an outcome, a duration, an argument
	// preview.
	Note string
	// Detail is shown only when a call failed, because that is the one result a
	// reader has to act on.
	Detail string
	// ID is the tool call this block belongs to.
	ID string
	// Open marks a block that is still being written.
	Open bool
	// since is when an open block started, for the duration in its Note.
	since time.Time
	// progressAt is when Progress last changed. A server chooses its own
	// reporting rate and some choose one that would repaint faster than the
	// screen can be read, so updates are taken at most once per progressEvery.
	progressAt time.Time
}

// Span is one stretch of the transcript: a run of adjacent blocks of one kind.
//
// Spans used to exist only inside the renderer, as the run it discovered while
// walking the blocks. 0.12.15 puts them in the model, because folding is a
// question about a stretch and a stretch has to be something the reader can
// point at: the renderer may draw a box around a run, but a click has to be
// able to say which run, and a stale frame must not be able to make it say the
// wrong one.
//
// Every block belongs to exactly one span, so the spans partition the
// transcript: a prompt, an answer and a notice each own a span of one, and a
// stretch of reasoning or of calls owns the run of blocks it was built from.
type Span struct {
	// First and Last are block indices, inclusive.
	First, Last int
	// Collapsed folds the stretch to its title line. It is a reader action and
	// not a rendering mode: the box stays the default shape, which is the
	// tradeoff 0.4.18 made and folding does not revisit. Every stretch opens
	// unfolded, and the setting that turns the boxes off turns folding off with
	// them: there is no box to fold.
	Collapsed bool
}

// Transcript is the scrolled history plus the state that is not itself text:
// which block is open, and what time it is.
//
// It is not concurrency safe and does not need to be: one owner touches it —
// the bubbletea update loop in the TUI, the turn goroutine in a window that
// serialises its own calls. Events reach that owner as messages, never as
// concurrent calls.
type Transcript struct {
	blocks []Block
	// spans partitions the blocks, and is maintained as they arrive rather
	// than discovered per frame: the fold state lives here, so the boundary and
	// the state have to be decided at the same moment. Appended blocks never
	// move, which is what lets a span be a pair of indices.
	spans []Span
	// reveal is the first span a frame renders. The spans before it are held
	// back: resuming a session replays its whole conversation as a burst of
	// closed blocks, and drawing all of it on the first frame bills the reader
	// for history nobody is looking at. The reader meets the rest a chunk at a
	// time by scrolling to the top of the window, which moves reveal back.
	reveal int
	// Now is the clock the thinking and tool durations are measured with. A
	// field rather than the package function so a test can pin the elapsed time
	// instead of sleeping.
	Now func() time.Time

	// streamed marks the block the current assistant message wrote its text
	// into, and is valid only while answered is set. The provider also puts the
	// whole answer on the message that ends it, so message_end has to know
	// whether the deltas already delivered it: without this the transcript
	// shows every answer twice.
	streamed bool
	answer   int

	// stopped records that the reader asked for the turn to stop. A tool that
	// was running at that moment comes back with the kernel's abort message in
	// an error result, and "failed" beside a call the reader cut short is the
	// wrong word for what happened: it says something broke, when what broke
	// was the plan.
	stopped bool
}

// New returns an empty transcript.
func New() Transcript {
	return Transcript{Now: time.Now}
}

// Reset drops every block, which is what switching to a different session does.
// The clock survives: it is configuration, not content.
func (t *Transcript) Reset() {
	t.blocks = nil
	t.spans = nil
	t.reveal = 0
	t.streamed = false
	t.answer = 0
}

// TrimAnswer keeps only the newest answer and whatever follows it drawn. The
// transcript still holds the rest: scrolling to the top of the window reveals
// it again.
//
// A resumed session arrives as a burst of closed answers, and each
// intermediate frame would redraw everything appended so far. The newest
// answer is the one message the reader came back for, so it is the whole of
// the first window: one answer is cheap to draw at every step of the burst,
// where a window of fixed span count is not — sixty-four short answers are
// still sixty-four answers.
func (t *Transcript) TrimAnswer() {
	for i := len(t.spans) - 1; i >= 0; i-- {
		if t.blocks[t.spans[i].First].Role == RoleAnswer {
			t.reveal = i
			return
		}
	}
	t.reveal = 0
}

// Hidden reports how many spans are held back above the rendered window.
func (t *Transcript) Hidden() int { return t.reveal }

// Reveal moves the top of the rendered window back by up to n spans.
func (t *Transcript) Reveal(n int) {
	if n <= 0 || t.reveal == 0 {
		return
	}
	t.reveal -= n
	if t.reveal < 0 {
		t.reveal = 0
	}
}

// Len reports how many blocks the transcript holds.
func (t *Transcript) Len() int { return len(t.blocks) }

// Blocks returns the blocks for inspection. The slice is the transcript's own:
// a caller that writes to it changes the history.
func (t *Transcript) Blocks() []Block { return t.blocks }

// Append adds a block and returns its index.
func (t *Transcript) Append(block Block) int {
	if block.Open && block.since.IsZero() {
		block.since = t.clock()
	}
	t.blocks = append(t.blocks, block)
	index := len(t.blocks) - 1
	// A framed block continues the stretch before it when that stretch is of
	// the same kind; everything else starts its own. The two kinds that
	// alternate are reasoning and calls — a prompt, an answer and a notice each
	// stand alone whatever the reader writes — so no other grouping exists.
	if n := len(t.spans); n > 0 && Framed(block.Role) && t.blocks[t.spans[n-1].First].Role == block.Role {
		t.spans[n-1].Last = index
		return index
	}
	t.spans = append(t.spans, Span{First: index, Last: index})
	return index
}

// Spans returns the stretches of the transcript for inspection. The slice is
// the transcript's own: a caller that writes to it changes the history.
func (t *Transcript) Spans() []Span { return t.spans }

// ToggleSpan folds or unfolds one stretch. It reports whether the index named
// a stretch that can be folded at all: a prompt is one line already, and a
// frames-off layout has no boxes to fold.
func (t *Transcript) ToggleSpan(index int) bool {
	if index < 0 || index >= len(t.spans) {
		return false
	}
	span := &t.spans[index]
	if !Framed(t.blocks[span.First].Role) {
		return false
	}
	span.Collapsed = !span.Collapsed
	return true
}

// ToggleAll folds every stretch when the transcript is fully open, and unfolds
// every stretch in every other case. Answering a click that left only some
// stretches folded with a fold of the rest would make this key alternate; it
// resolves instead, so one press after a click always shows the open shape.
func (t *Transcript) ToggleAll() bool {
	foldable := 0
	folded := 0
	for _, span := range t.spans {
		if !Framed(t.blocks[span.First].Role) {
			continue
		}
		foldable++
		if span.Collapsed {
			folded++
		}
	}
	if foldable == 0 {
		return false
	}
	collapse := folded == 0
	for i := range t.spans {
		if Framed(t.blocks[t.spans[i].First].Role) {
			t.spans[i].Collapsed = collapse
		}
	}
	return true
}

// Update rewrites one block in place.
func (t *Transcript) Update(index int, fn func(*Block)) {
	if index < 0 || index >= len(t.blocks) {
		return
	}
	fn(&t.blocks[index])
}

// Close stops a block and stamps how long it was open into its Note.
func (t *Transcript) Close(index int, label string) {
	t.Update(index, func(block *Block) {
		if !block.Open {
			return
		}
		block.Open = false
		if label != "" && !block.since.IsZero() {
			block.Note = joinNote(block.Note, label+" "+humanDuration(t.clock().Sub(block.since)))
		}
	})
}

// Stop records that the reader asked the running turn to stop. Results that
// arrive afterwards were cut short rather than failed, and the blocks say so.
// The flag lives until the turn reports back, which is what bounds it: the next
// turn starts with a clean slate.
func (t *Transcript) Stop() { t.stopped = true }

// Finish closes whatever a turn left open. It runs when the turn reports back,
// which is the only moment that is guaranteed to come: a cancellation skips
// every event that would otherwise have closed a block.
func (t *Transcript) Finish() {
	t.closeRole(RoleAnswer, "")
	t.closeRole(RoleThinking, "Thought for")
	if i, ok := t.openBlockOf(RoleTool); ok {
		t.Update(i, func(block *Block) {
			block.Open = false
			block.Progress = ""
			block.Note = joinNote(withoutNote(block.Note, "running"), "interrupted")
		})
	}
	t.stopped = false
}

// openIndex is the last open block, or -1. A streamed answer and a running call
// cannot both be last, so "the open one" is unambiguous.
func (t *Transcript) openIndex() int {
	for i := len(t.blocks) - 1; i >= 0; i-- {
		if t.blocks[i].Open {
			return i
		}
	}
	return -1
}

// toolIndex finds the block for a call id. Parallel calls come back out of
// order, so the id is the only way to match a result to its line.
func (t *Transcript) toolIndex(id string) int {
	if id == "" {
		return -1
	}
	for i := len(t.blocks) - 1; i >= 0; i-- {
		if t.blocks[i].Role == RoleTool && t.blocks[i].ID == id {
			return i
		}
	}
	return -1
}

func (t *Transcript) clock() time.Time {
	if t.Now == nil {
		return time.Now()
	}
	return t.Now()
}

// Framed reports whether a run of blocks of this kind is drawn as one unit.
//
// The decision is here rather than in a renderer because it is also the
// grouping rule: a stretch of calls is one thing to fold, to name and to click,
// and a prompt is not. Both frontends ask this and get one answer; the TUI
// draws a box around the answer, a window draws a card, and neither decides
// which kinds group.
func Framed(role Role) bool {
	return role == RoleThinking || role == RoleTool
}

func joinNote(note, addition string) string {
	switch {
	case note == "":
		return addition
	case addition == "":
		return note
	}
	return note + " · " + addition
}

// withoutNote removes one part of a joined note.
func withoutNote(note, part string) string {
	if note == "" {
		return ""
	}
	parts := strings.Split(note, " · ")
	out := parts[:0]
	for _, value := range parts {
		if value != part {
			out = append(out, value)
		}
	}
	return strings.Join(out, " · ")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
