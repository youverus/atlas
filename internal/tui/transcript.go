package tui

import (
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
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
// It is not concurrency safe and does not need to be: only the bubbletea update
// loop touches it. Events reach that loop as messages, never as calls from the
// turn goroutine.
type Transcript struct {
	blocks []Block
	// spans partitions the blocks, and is maintained as they arrive rather
	// than discovered per frame: the fold state lives here, so the boundary and
	// the state have to be decided at the same moment. Appended blocks never
	// move, which is what lets a span be a pair of indices.
	spans []Span
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

	// md is the markdown renderer and its per-block cache, made on the first
	// answer that needs one. It is here rather than in the frame because the
	// cost it saves is the frame's: Lines rebuilds every block sixty times a
	// second, and a reply does not change between two of them.
	md *markdown
}

// NewTranscript returns an empty transcript.
func NewTranscript() Transcript {
	return Transcript{Now: time.Now}
}

// Reset drops every block, which is what switching to a different session does.
// The clock survives: it is configuration, not content.
func (t *Transcript) Reset() {
	t.blocks = nil
	t.spans = nil
	t.streamed = false
	t.answer = 0
	if t.md != nil {
		// The rows are still valid as markdown, but they are keyed by block
		// index, and the indices now belong to a different conversation.
		t.md.forget()
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
	if n := len(t.spans); n > 0 && framed(block.Role) && t.blocks[t.spans[n-1].First].Role == block.Role {
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
	if !framed(t.blocks[span.First].Role) {
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
		if !framed(t.blocks[span.First].Role) {
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
		if framed(t.blocks[t.spans[i].First].Role) {
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
	// Spans is one entry per stretch, in the order Transcript.Spans holds them.
	Spans []SpanRows
}

// SpanRows is the row range one stretch occupies in a Frame: First and Last are
// indices into Lines, inclusive, and First <= Last always holds because every
// renderer draws at least one row.
type SpanRows struct {
	First, Last int
}

// RowsFor reports the rows of the stretch at index, and false when the index
// names no stretch of this frame.
func (f Frame) RowsFor(index int) (SpanRows, bool) {
	if index < 0 || index >= len(f.Spans) {
		return SpanRows{}, false
	}
	return f.Spans[index], true
}

// SpanAt reports the stretch that drew the row, and false for a row that no
// stretch drew — the blank line between two of them, or the padding above a
// short conversation.
func (f Frame) SpanAt(row int) (int, bool) {
	for index, span := range f.Spans {
		if row >= span.First && row <= span.Last {
			return index, true
		}
	}
	return 0, false
}

// Lines renders the whole transcript at width. It is called once per frame, so
// it stays a pure function of the blocks, the width and the layout.
func (t *Transcript) Lines(width int, s Styles, l Layout) []string {
	return t.Frame(width, s, l).Lines
}

// Frame renders the transcript and reports which rows each stretch took.
//
// The walk is over the stretches rather than over the blocks, because a stretch
// is what a frame draws — one box, or one line when it is folded — and it is
// what a click can name.
func (t *Transcript) Frame(width int, s Styles, l Layout) Frame {
	if width < 4 {
		width = 4
	}
	frame := Frame{
		Lines: make([]string, 0, len(t.blocks)*3),
		Spans: make([]SpanRows, 0, len(t.spans)),
	}
	var previous Role
	for at, span := range t.spans {
		blocks := t.blocks[span.First : span.Last+1]
		role := blocks[0].Role
		if at > 0 && separates(previous, role, l) {
			for gap := 0; gap < l.BlockGap; gap++ {
				frame.Lines = append(frame.Lines, "")
			}
		}
		first := len(frame.Lines)
		switch {
		case span.Collapsed && l.TranscriptFrames:
			frame.Lines = append(frame.Lines, collapsedSpan(blocks, width, s)...)
		case l.TranscriptFrames && framed(role):
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

// framed reports whether a run of blocks of this kind is drawn as one box.
func framed(role Role) bool {
	return role == RoleThinking || role == RoleTool
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

func renderBlock(block Block, width int, s Styles, l Layout) []string {
	switch block.Role {
	case RoleUser:
		// Both halves in the user's color: the marker alone was not enough to
		// pick the prompt out of the answers, because the words beside it were
		// painted the same as theirs.
		return marked(block.Text, userMarker, indent, width, s.User, s.User)
	case RoleAnswer:
		return answerBlock(block, width, s)
	case RoleThinking:
		return thinkingBlock(block, width, s, l)
	case RoleTool:
		return toolBlock(block, width, s)
	case RoleError:
		return marked(block.Text, "error: ", indent, width, s.Error, s.Error)
	default:
		return marked(block.Text, "", "", width, s.Dim, s.Dim)
	}
}

// answerBlock draws what the model said as one block with a bar down its left
// edge, on every line rather than only the first.
//
// The reason is the screen's own arithmetic: an answer and the tree of calls it
// caused are both prose at the same weight, and until now the only thing
// telling them apart was a marker on the first line of one and a branch glyph
// on the others. A rule down the side is the one shape that survives being
// scrolled half out of view, wrapped over twenty lines, or read while the next
// answer is still streaming into it.
//
// The words are drawn as written. Lines, which is what a frame calls, goes
// through the markdown cache instead; this is the same drawing without it, kept
// because rendering one block is a request a caller can make and it should not
// have to own a renderer to ask.
func answerBlock(block Block, width int, s Styles) []string {
	return barLines(wrapText(block.Text, answerWidth(width)), s)
}

// answerLines is the same block, with the text read as markdown. It is the path
// a frame takes.
func (t *Transcript) answerLines(index int, block Block, width int, s Styles) []string {
	if block.Text == "" {
		// An answer with no text yet owns the row: the bar says where the
		// words will land, and it is the marker the first token arrives in.
		return barLines(nil, s)
	}
	if t.md == nil {
		t.md = &markdown{}
	}
	return barLines(t.md.lines(index, block.Text, answerWidth(width), s.palette), s)
}

// answerWidth is how many cells an answer's words get once the bar has taken
// its own.
func answerWidth(width int) int {
	avail := width - lipgloss.Width(AnswerBar)
	if avail < 1 {
		avail = 1
	}
	return avail
}

// barLines puts the bar in front of every row of an answer.
func barLines(body []string, s Styles) []string {
	if len(body) == 0 {
		return []string{s.Accent.Render(strings.TrimRight(AnswerBar, " "))}
	}
	out := make([]string, 0, len(body))
	for _, line := range body {
		out = append(out, s.Accent.Render(AnswerBar)+s.Text.Render(line))
	}
	return out
}

// marked draws a marker in one style and a body in another, with continuation
// lines aligned under the body.
func marked(text, marker, continuation string, width int, markerStyle, bodyStyle lipgloss.Style) []string {
	avail := width - lipgloss.Width(marker)
	if avail < 1 {
		avail = 1
	}
	body := wrapText(text, avail)
	if len(body) == 0 {
		return []string{markerStyle.Render(strings.TrimRight(marker, " "))}
	}
	out := make([]string, 0, len(body))
	for i, line := range body {
		if i == 0 {
			out = append(out, markerStyle.Render(marker)+bodyStyle.Render(line))
			continue
		}
		out = append(out, continuation+bodyStyle.Render(line))
	}
	return out
}

func thinkingBlock(block Block, width int, s Styles, l Layout) []string {
	// The body sits four cells in: two for the marker's column and two more so
	// it reads as a footnote to the answer rather than as the answer.
	const thinkingIndent = "    "
	body := wrapText(block.Text, width-lipgloss.Width(thinkingIndent))
	clipped := false
	if len(body) > l.ThinkingRows {
		body = body[len(body)-l.ThinkingRows:]
		clipped = true
	}
	head := thinkingMark
	if block.Note != "" {
		head += block.Note
	} else if block.Open {
		head += "thinking"
	} else {
		head += "thought"
	}
	out := []string{s.Dim.Render(head)}
	for i, line := range body {
		prefix := thinkingIndent
		if i == 0 && clipped {
			prefix = "  … "
		}
		out = append(out, s.Dim.Render(prefix+line))
	}
	return out
}

// head is the marker line's text: the name, the arguments, live progress and
// the note, in that order. Both renderers build it — the unboxed tree and the
// boxed run — so the two cannot drift into saying a call differently.
func (b Block) head() string {
	head := b.Name
	if b.Text != "" {
		head += "  " + b.Text
	}
	if b.Progress != "" {
		head += " · " + b.Progress
	}
	if b.Note != "" {
		head += " · " + b.Note
	}
	return head
}

func toolBlock(block Block, width int, s Styles) []string {
	out := marked(block.head(), depthMarker, depthIndent, width, s.Tool, s.Tool)
	if block.Detail == "" {
		return out
	}
	for _, line := range wrapText(block.Detail, width-lipgloss.Width(depthIndent)) {
		out = append(out, s.Error.Render(depthIndent+line))
	}
	return out
}

// segmentRow is one line inside a segment: the text already broken to the box,
// and the slot it is painted in. The style travels with the text because the
// row is padded out to the border afterwards, and the padding has to be the
// screen's air rather than the terminal's.
type segmentRow struct {
	text  string
	style lipgloss.Style
}

// segmentLines draws a run of blocks of one kind as one box.
//
// The box is what makes a turn readable as a sequence: reasoning happens, then
// a stretch of calls, then reasoning again, and until now the only thing saying
// where a stretch ended was the alignment of the markers on the next line. A
// rule above and below says it without the reader counting markers, and the
// title says what the stretch was.
func segmentLines(blocks []Block, width int, s Styles, l Layout) []string {
	inner := width - 2*segmentBorder
	if inner < 4 {
		return stacked(blocks, width, s, l)
	}
	titles, titleStyle, rows := segmentContent(blocks, inner, s, l)
	title, ok := fittingTitle(titles, width)
	if !ok {
		return stacked(blocks, width, s, l)
	}
	return segmentBox(title, titleStyle, rows, width, s)
}

// stacked draws a stretch one block per line, which is the transcript as it was
// before there were boxes: the fallback for a terminal with no room for a title.
func stacked(blocks []Block, width int, s Styles, l Layout) []string {
	out := make([]string, 0, len(blocks))
	for _, block := range blocks {
		out = append(out, renderBlock(block, width, s, l)...)
	}
	return out
}

// fittingTitle picks the most informative title that still leaves a rule beside
// it. The box is the structure and the title is the description, so a terminal
// too narrow for both loses the description first rather than the box.
func fittingTitle(titles []string, width int) (string, bool) {
	for _, title := range titles {
		if fitsSegment(title, width) {
			return title, true
		}
	}
	return "", false
}

// fitsSegment reports whether a title leaves a rule on either side of it.
func fitsSegment(title string, width int) bool {
	if width < minSegmentWidth {
		return false
	}
	// One border, one blank, the title, one blank, a cell of rule, one border.
	return lipgloss.Width(title)+5 <= width
}

// segmentContent is the title and the body rows of one stretch. The two kinds
// answer differently: reasoning is a single stream that says how it ended, and
// calls are a list that says what it did.
// The titles come richest first, because which of them fits depends on the
// screen the conversation is being read on.
func segmentContent(blocks []Block, inner int, s Styles, l Layout) ([]string, lipgloss.Style, []segmentRow) {
	titles, style := spanTitles(blocks, s)
	if len(blocks) == 0 {
		return nil, style, nil
	}
	if blocks[0].Role == RoleTool {
		return titles, style, toolRows(blocks, inner, s)
	}
	return titles, style, thinkingRows(blocks, inner, s, l)
}

// spanTitles is what a stretch can be called, richest first, and the slot those
// words belong in.
//
// Both the box's top rule and the folded line are drawn from this, so folding a
// stretch cannot rename it: the reader folds a box called "2 calls · read, grep"
// and gets a line called the same thing back.
func spanTitles(blocks []Block, s Styles) ([]string, lipgloss.Style) {
	if len(blocks) == 0 {
		return nil, s.Dim
	}
	if blocks[0].Role == RoleTool {
		return toolTitles(blocks), s.Tool
	}
	return thinkingTitles(blocks), s.Dim
}

// foldedMark is the marker a folded stretch leads with. It is the triangle the
// reader points at, and the one glyph on screen that says rows are being held
// back rather than missing.
const foldedMark = "▸ "

// collapsedSpan draws a folded stretch as its title alone.
//
// The body is not drawn and then clipped, it is not drawn: the rows it gives
// back are the whole of what folding is for. The title is chosen the way the
// box's would be, so a stretch keeps its name across the fold; when even the
// shortest one is wider than the screen the last one is cut, because a folded
// stretch with no title would be a marker pointing at nothing.
func collapsedSpan(blocks []Block, width int, s Styles) []string {
	titles, style := spanTitles(blocks, s)
	if len(titles) == 0 {
		return []string{s.Border.Render(strings.TrimRight(foldedMark, " "))}
	}
	title, ok := fittingTitle(titles, width)
	if !ok {
		title = titles[len(titles)-1]
	}
	room := width - lipgloss.Width(foldedMark)
	if room < 1 {
		room = 1
	}
	if lipgloss.Width(title) > room {
		title = cutCells(title, room)
	}
	return []string{s.Border.Render(foldedMark) + style.Render(title)}
}

// thinkingTitles names a stretch of reasoning: how it ended when it has ended,
// and what it is doing while it has not.
func thinkingTitles(blocks []Block) []string {
	if len(blocks) == 0 {
		return nil
	}
	last := blocks[len(blocks)-1]
	bare := "thought"
	if last.Open {
		bare = "thinking"
	}
	if last.Note != "" {
		return []string{last.Note, bare}
	}
	return []string{bare}
}

// thinkingRows is the tail of the stream, which is the one rule the block
// already had: a footnote shows its end, because the end is what the answer
// followed.
func thinkingRows(blocks []Block, inner int, s Styles, l Layout) []segmentRow {
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	body := wrapText(strings.Join(parts, "\n"), inner)
	clipped := len(body) > l.ThinkingRows
	if clipped {
		body = body[len(body)-l.ThinkingRows:]
	}
	rows := make([]segmentRow, 0, len(body))
	for i, line := range body {
		if i == 0 && clipped {
			// The mark the block already used, so a cut tail still says it is
			// a tail and not the whole thought.
			line = thinkingMark + cutCells(line, inner-lipgloss.Width(thinkingMark))
		}
		rows = append(rows, segmentRow{text: line, style: s.Dim})
	}
	return rows
}

// toolTitles counts a stretch of calls and names the tools up to three, because
// the first question about a stretch of calls is what it did.
func toolTitles(blocks []Block) []string {
	names := make([]string, 0, 3)
	seen := make(map[string]bool, len(blocks))
	more := 0
	for _, block := range blocks {
		if block.Name == "" || seen[block.Name] {
			continue
		}
		seen[block.Name] = true
		if len(names) == 3 {
			more++
			continue
		}
		names = append(names, block.Name)
	}
	call := "calls"
	if len(blocks) == 1 {
		call = "call"
	}
	count := strconv.Itoa(len(blocks)) + " " + call
	titles := []string{count}
	if len(names) > 0 {
		title := count + " · " + strings.Join(names, ", ")
		if more > 0 {
			title += ", +" + strconv.Itoa(more)
		}
		titles = []string{title, count}
		if len(names) > 1 {
			titles = []string{title, count + " · " + names[0], count}
		}
	}
	return titles
}

// toolRows is one line per call with the three fields the branch glyph used to
// carry, and the body of a failure under the call it belongs to. The glyph
// itself is gone: the box is the grouping now.
func toolRows(blocks []Block, inner int, s Styles) []segmentRow {
	rows := make([]segmentRow, 0, len(blocks))
	for _, block := range blocks {
		for _, line := range wrapText(block.head(), inner) {
			rows = append(rows, segmentRow{text: line, style: s.Tool})
		}
		for _, line := range wrapText(block.Detail, inner-segmentDetailIndent) {
			rows = append(rows, segmentRow{text: strings.Repeat(" ", segmentDetailIndent) + line, style: s.Error})
		}
	}
	return rows
}

// segmentBox draws the frame: a title in the top rule, the rows, and a closing
// rule. Every row is padded out to the border, because a box whose middle is
// the terminal's color is a stripe with a rule around it.
func segmentBox(title string, titleStyle lipgloss.Style, rows []segmentRow, width int, s Styles) []string {
	inner := width - 2*segmentBorder
	out := make([]string, 0, len(rows)+2)
	out = append(out, s.Border.Render("╭")+s.Background.Render(" ")+titleStyle.Render(title)+
		s.Background.Render(" ")+s.Border.Render(strings.Repeat("─", width-4-lipgloss.Width(title))+"╮"))
	for _, row := range rows {
		text := row.text
		if lipgloss.Width(text) > inner {
			// Only reachable on a row whose own text was not wrapped to the
			// box, and a row wider than the box would push the right rule off
			// the screen.
			text = cutCells(text, inner)
		}
		line := s.Border.Render("│") + s.Background.Render(" ") + row.style.Render(text)
		if gap := inner - lipgloss.Width(text); gap > 0 {
			line += s.Background.Render(strings.Repeat(" ", gap))
		}
		out = append(out, line+s.Background.Render(" ")+s.Border.Render("│"))
	}
	out = append(out, s.Border.Render("╰"+strings.Repeat("─", width-2)+"╯"))
	return out
}

// wrapText breaks text to width and keeps the hard breaks it already had: a
// tool result that failed usually carries its own layout, and reflowing it
// would take the shape away.
func wrapText(text string, width int) []string {
	if width < 1 {
		width = 1
	}
	text = strings.TrimRight(text, "\n")
	if text == "" {
		return nil
	}
	var out []string
	for _, line := range strings.Split(text, "\n") {
		if line == "" {
			out = append(out, "")
			continue
		}
		out = append(out, strings.Split(lipgloss.Wrap(line, width, ""), "\n")...)
	}
	return out
}
