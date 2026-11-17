package tui

import (
	"pi-go/internal/transcript"
)

// The model is the transcript package's; these aliases only spare the renderers
// a package qualifier on every line. An alias is the same type, not a copy, so
// the fold still happens once — see internal/transcript for the reason it moved
// out of this package.
type (
	Role  = transcript.Role
	Block = transcript.Block
	Span  = transcript.Span
)

const (
	RoleUser     = transcript.RoleUser
	RoleAnswer   = transcript.RoleAnswer
	RoleThinking = transcript.RoleThinking
	RoleTool     = transcript.RoleTool
	RoleNotice   = transcript.RoleNotice
	RoleError    = transcript.RoleError
)

// Transcript is the shared model plus the one piece of state that belongs to a
// renderer: the markdown cache.
//
// The cache is here because it is keyed by block index and holds rows that were
// laid out for a cell grid. A window drawing the same blocks has no use for it
// and would have to throw it away, so it stays with the interface that spends
// the cost — and Reset clears it, because the indices it keys on then belong to
// a different conversation.
type Transcript struct {
	*transcript.Transcript
	md *markdown
}

// NewTranscript returns an empty transcript.
func NewTranscript() Transcript {
	model := transcript.New()
	return Transcript{Transcript: &model}
}

// Reset drops every block, which is what switching to a different session does.
// The clock survives: it is configuration, not content.
func (t *Transcript) Reset() {
	t.Transcript.Reset()
	if t.md != nil {
		// The rows are still valid as markdown, but they are keyed by block
		// index, and the indices now belong to a different conversation.
		t.md.forget()
	}
}

// revealChunkSpans is how many held-back stretches one scroll to the top of the
// window reveals. The chunk is a screenful or two of older conversation rather
// than a page: meeting the history gradually is what makes the reader unable to
// tell it was ever missing.
const revealChunkSpans = 32

// Marker widths. They are ASCII, so len and cell width agree; keeping them
// named is what makes the continuation indent below stay in step.
const (
	userMarker   = "> "
	depthMarker  = "  ├ • "
	indent       = "  "
	depthIndent  = "      "
	thinkingMark = "… "
)

// AnswerBar is the left edge of an answer: the same two cells on every line, so
// a reply that wraps to twenty lines still reads as one block, and it keeps
// reading that way while the words are still arriving. It is exported because
// replaying a recorded conversation has to draw the same edge the live turns do.
const AnswerBar = "│ "

// Segment geometry. A segment box takes one border column and one column of air
// on each side, the same two cells the composer's frame spends: the air is
// inside the box so the surface reaches the rule and the text never touches it.
const segmentBorder = 2

// minSegmentWidth is the narrowest a box is drawn at. Below it the rule would
// eat the calls it exists to group, so the stretch is drawn one block per line
// instead — a two-cell box is not a smaller box, it is a broken one.
const minSegmentWidth = 2*segmentBorder + 4

// segmentDetailIndent is how far a failure's body sits from the box's rule, so
// it reads as belonging to the call above it rather than to the next one.
const segmentDetailIndent = 2

// Frame is one rendering of the transcript: the rows, and the rows each stretch
// took.
//
// The two travel together because a frame is the only thing that knows both. A
// click arrives as a row on the screen, and the question it asks — which stretch
// is this — is answered by the mapping and by nothing else. Working it out in a
// second pass would be a second walk of the same blocks, and the second walk
// could disagree with the rows it claims to describe.
type Frame struct {
	Lines []string
	// FirstSpan is the transcript span index of Spans[0]. A frame that starts
	// mid-conversation — the reveal window holds part of the history back — has
	// to answer clicks with span indices the transcript itself holds, not with
	// positions in a shrunken list.
	FirstSpan int
	// Spans is one entry per rendered stretch, in the order Transcript.Spans
	// holds them from FirstSpan on.
	Spans []SpanRows
}

// SpanRows is the row range one stretch occupies in a Frame: First and Last are
// indices into Lines, inclusive, and First <= Last always holds because every
// renderer draws at least one row.
type SpanRows struct {
	First, Last int
}

// RowsFor reports the rows of the stretch at index, and false when the index
// names no stretch of this frame. The index is a transcript span index, the
// same one Spans and ToggleSpan use; a window that starts mid-conversation
// subtracts its own offset.
func (f Frame) RowsFor(index int) (SpanRows, bool) {
	index -= f.FirstSpan
	if index < 0 || index >= len(f.Spans) {
		return SpanRows{}, false
	}
	return f.Spans[index], true
}

// SpanAt reports the stretch that drew the row, and false for a row that no
// stretch drew — the blank line between two of them, or the padding above a
// short conversation. It reports the transcript span index, so the answer is
// the same whatever part of the history the window starts at.
func (f Frame) SpanAt(row int) (int, bool) {
	for index, span := range f.Spans {
		if row >= span.First && row <= span.Last {
			return f.FirstSpan + index, true
		}
	}
	return 0, false
}

// Lines renders the revealed part of the transcript at width. It is called
// once per frame, so it stays a pure function of the blocks, the width and the
// layout. Spans held back by the reveal window are not drawn.
func (t *Transcript) Lines(width int, s Styles, l Layout) []string {
	return t.Frame(width, s, l).Lines
}

// Frame renders the revealed window of the transcript and reports which rows
// each stretch took. The window starts at the reveal span: everything before it
// is held back until the reader scrolls to the top of the window, and a frame
// never pays for it.
//
// The walk is over the stretches rather than over the blocks, because a stretch
// is what a frame draws — one box, or one line when it is folded — and it is
// what a click can name.
func (t *Transcript) Frame(width int, s Styles, l Layout) Frame {
	all, spans, reveal := t.Blocks(), t.Spans(), t.Hidden()
	if width < 4 {
		width = 4
	}
	frame := Frame{
		Lines:     make([]string, 0, len(all)*3),
		FirstSpan: reveal,
		Spans:     make([]SpanRows, 0, len(spans)-reveal),
	}
	var previous Role
	for at, span := range spans {
		if at < reveal {
			continue
		}
		blocks := all[span.First : span.Last+1]
		role := blocks[0].Role
		if at > reveal && separates(previous, role, l) {
			for gap := 0; gap < l.BlockGap; gap++ {
				frame.Lines = append(frame.Lines, "")
			}
		}
		first := len(frame.Lines)
		switch {
		case span.Collapsed && l.TranscriptFrames:
			frame.Lines = append(frame.Lines, collapsedSpan(blocks, width, s)...)
		case l.TranscriptFrames && transcript.Framed(role):
			frame.Lines = append(frame.Lines, segmentLines(blocks, width, s, l)...)
		default:
			// Without boxes the stretch is not drawn as one thing: folding it
			// has nothing to fold, and the run of blocks is the column of lines
			// 0.4.15 drew. Separators inside a stretch are not lost here — a
			// stretch is one kind of block by construction, and two blocks of
			// one kind are never separated.
			for _, block := range blocks {
				if block.Role == RoleAnswer {
					frame.Lines = append(frame.Lines, t.answerLines(span.First, block, width, s)...)
					continue
				}
				frame.Lines = append(frame.Lines, renderBlock(block, width, s, l)...)
			}
		}
		frame.Spans = append(frame.Spans, SpanRows{First: first, Last: len(frame.Lines) - 1})
		// An empty answer is not a block yet: the marker would promise text
		// that has not arrived.
		last := blocks[len(blocks)-1]
		if len(last.Text) > 0 || last.Role != RoleAnswer {
			previous = last.Role
		}
	}
	return frame
}

// separates is where the blank line goes, which is a different question once
// segments have rules: two boxes are already apart, so the air is what tells
// one stretch from the next rather than what tells prose from a call.
func separates(previous, next Role, l Layout) bool {
	if !l.TranscriptFrames {
		return separate(previous, next)
	}
	if previous == RoleUser || next == RoleUser {
		return true
	}
	return previous != next
}

// separate decides where a blank line goes. Two rules, both about telling
// things apart on a screen that scrolls:
//
// Prose and the tree of tool calls are different things, so a change between
// them gets air, while consecutive calls do not.
//
// And what the reader typed is separated from everything on both sides. It is
// the one block that was not written by the model, and it is the one a reader
// scrolls back to find: with no gap, a prompt sat directly on the answer above
// it and read as part of it.
func separate(previous, next Role) bool {
	if previous == RoleUser || next == RoleUser {
		return true
	}
	before := previous == RoleTool
	after := next == RoleTool
	return before != after
}
