package tui

// Layout is the geometry of the interface: every number that is a count of rows
// or cells rather than a color or a word.
//
// These were constants until 0.4.10, and that was the wrong shape for them. The
// numbers are the difference between a screen that suits one terminal and one
// that suits the next — a 24-row window and a 60-row window want different
// amounts of air, and the reasoning block that is a footnote on one is the whole
// answer on another — so they belong to the reader, not to the build.
//
// The bricks stay here and the file only overrides them: internal/config owns
// the JSON, cmd/atlas does the merging, and this package draws whatever it is
// handed. That is the same shape the colors already have, and for the same
// reason: the interface should not know that a config file exists.
type Layout struct {
	// ComposerGap is the blank rows between the conversation and the input
	// area. Zero puts the prompt straight under the last answer.
	ComposerGap int

	// StatusGap is the blank rows between the input area and the status bar.
	// Zero leaves the bar directly under the prompt, which is where it sat
	// until the gap became a setting: a bar attached to the prompt reads as
	// part of it, and a reader who wants them apart should not have to give up
	// the row above the prompt to get it.
	StatusGap int

	// BlockGap is the blank rows between two transcript blocks that are
	// separated at all. Zero makes a transcript one solid column; the
	// separation itself is not a setting, because it is what tells prose from
	// a tool call.
	BlockGap int

	// ComposerRows bounds how tall the input line may grow. Past it the
	// transcript disappears while the reader types.
	ComposerRows int

	// ComposerFrame draws the input area inside a rounded box on the raised
	// background, which is what puts the prompt visually above the
	// conversation instead of in line with it.
	//
	// It is here rather than in a theme because it is geometry: the border
	// takes two rows and four cells, so the transcript's height and the width
	// the prompt wraps at both depend on it. A theme decides what the box looks
	// like; this decides whether there is one.
	ComposerFrame bool

	// TranscriptFrames draws a run of adjacent blocks of the same kind inside
	// one box and titles it, so a turn that alternates reasoning and calls
	// reads as a stack of segments instead of a column of lines.
	//
	// It is geometry, for the same reason ComposerFrame is: a segment costs a
	// rule above and one below, and every boxed segment takes those two rows
	// from the conversation. Off is the 0.4.15 transcript, to the byte.
	TranscriptFrames bool

	// PaletteRows bounds the command palette above the prompt. Zero hides it.
	PaletteRows int

	// MenuRows bounds the list inside a pick menu, which scrolls past it.
	MenuRows int

	// MenuWidth bounds the pick menu's box, borders included.
	MenuWidth int

	// BoxWidth bounds the approval question's box, borders included. It is
	// narrower than a menu by default: a question is one sentence, and the
	// list carries three columns.
	BoxWidth int

	// ThinkingRows is how much reasoning is shown: the tail, never the whole
	// stream. Zero leaves the header and drops the body.
	ThinkingRows int

	// HomePage draws the welcome page for a conversation with nothing in it.
	// Off leaves that screen blank, which is what a reader who wants the
	// prompt at the top of an empty terminal asks for.
	HomePage bool
}

// DefaultLayout is the geometry 0.4.9 drew with. Every knob reads as the
// constant it replaced, so a build with no configuration is pixel for pixel the
// interface that shipped.
func DefaultLayout() Layout {
	return Layout{
		ComposerGap:      1,
		StatusGap:        0,
		BlockGap:         1,
		ComposerRows:     8,
		ComposerFrame:    true,
		TranscriptFrames: true,
		PaletteRows:      5,
		MenuRows:         10,
		MenuWidth:        72,
		BoxWidth:         64,
		ThinkingRows:     8,
		HomePage:         true,
	}
}

// composerBorder is what the frame takes from the width on each side: one
// border column and one blank column of air inside it. The air is inside the
// box on purpose, so the surface reaches the edges and the text never touches
// the border.
const composerBorder = 2

// frames reports whether the box is drawn at this width.
//
// Below the minimum the frame would eat the prompt it exists to frame, so it is
// dropped whole rather than drawn smaller: a two-cell box is not a smaller box,
// it is a broken one.
func (l Layout) frames(width int) bool {
	return l.ComposerFrame && width >= 2*composerBorder+4
}

// composerWidth is the width the prompt wraps at: the screen less the frame,
// and the whole screen when there is no frame.
func (l Layout) composerWidth(width int) int {
	if !l.frames(width) {
		return width
	}
	return width - 2*composerBorder
}

// composerTop is how many rows sit between the top of the input area and the
// first row of text: the box's upper rule, or nothing.
func (l Layout) composerTop(width int) int {
	if !l.frames(width) {
		return 0
	}
	return 1
}

// composerLeft is the column the first cell of text lands in: the border and
// the air inside it.
func (l Layout) composerLeft(width int) int {
	if !l.frames(width) {
		return 0
	}
	return composerBorder
}

// resolveLayout answers what a caller meant by the Layout on its Config.
//
// An all-zero value means "the default one": a Config assembled without a
// layout — every test, and any host that has no opinion — has to get the
// interface everybody knows rather than an empty screen. Anything else is taken
// as written, which is why a caller that changes one field should start from
// DefaultLayout(): the other fields of a literal are zero, and zero is a real
// setting for most of them.
func resolveLayout(given Layout) Layout {
	if given == (Layout{}) {
		return DefaultLayout()
	}
	return given.normalized()
}

// normalized keeps the numbers inside what the frame arithmetic can survive.
//
// It clamps rather than reports, and that is not laziness about errors: the
// config file is validated where the file is read, by a package that can name
// the file and the line it is in, and a host that builds a Layout in code
// should get a working screen rather than an error about a row count. Clamping
// cannot disagree with the config's ranges in a way that matters either — those
// are about taste, these are about a slice that would go out of bounds.
func (l Layout) normalized() Layout {
	clamp := func(value, low int) int {
		if value < low {
			return low
		}
		return value
	}
	l.ComposerGap = clamp(l.ComposerGap, 0)
	l.StatusGap = clamp(l.StatusGap, 0)
	l.BlockGap = clamp(l.BlockGap, 0)
	l.ComposerRows = clamp(l.ComposerRows, 1)
	l.PaletteRows = clamp(l.PaletteRows, 0)
	l.MenuRows = clamp(l.MenuRows, 1)
	// A box narrower than these cannot hold a border, its padding and one cell
	// of text, and both boxes already fall back to the screen width below it.
	l.MenuWidth = clamp(l.MenuWidth, 12)
	l.BoxWidth = clamp(l.BoxWidth, 12)
	l.ThinkingRows = clamp(l.ThinkingRows, 0)
	return l
}
