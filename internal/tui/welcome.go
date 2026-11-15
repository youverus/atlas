package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// Welcome is what the home screen says about the session it is about to start.
//
// The facts come from the host — a version, a directory and a model are things
// the interface cannot know — while the page itself is drawn here. That split is
// the same one the rest of the package uses: the host names tokens, the
// interface decides where they go.
type Welcome struct {
	Version string // "0.4.1"
	Dir     string // "~/proj/pi-agent-go"
	Model   string // "deepseek/deepseek-chat"
	// Note is the tail of the model line: how hard the next request asks the
	// model to think, and what the agent may touch. Empty leaves it out.
	Note string
	// Title is the page's first line. Empty draws the wordmark and the version
	// line, which is right for the session itself; a view that is not the
	// session names itself in one line instead of borrowing the mark of the
	// thing it branches from.
	Title string
	// Flash is one line about what just happened — a conversation was archived,
	// a file was deleted — said on the page because the page is what came back.
	//
	// It is empty in the ordinary case: the home screen is then saying what is
	// about to start, and nothing has happened yet. A command that lands the
	// reader on a session with nothing in it has to put its receipt here
	// instead of in the transcript: the transcript is what the page replaces,
	// so a notice would push away the one screen that says "an empty session,
	// as asked".
	Flash string
}

// logo is the wordmark, one row per string, drawn in full blocks so the letters
// read as solid shapes instead of as the strokes a figlet face leaves behind.
//
// A block is the one glyph terminals disagree about the width of — two cells in
// an emulator set to treat East Asian ambiguous characters as wide, one almost
// everywhere else — so this is a deliberate trade, not an oversight: the mark is
// the only thing on this page that is not text, and every row carries the same
// number of blocks, which keeps the letters in line with each other whatever
// width the terminal gives them. Only the centring of the mark against the rows
// beneath it can drift, and only on those emulators.
var logo = []string{
	"███  ███  █    ███  ███",
	"█ █   █   █    █ █  █  ",
	"███   █   █    ███  ███",
	"█ █   █   █    █ █    █",
	"█ █   █   ███  █ █  ███",
}

// wordmarkWidth is the room the mark asks for: the widest row, because the rows
// are padded to each other and the mark is placed as one block rather than row
// by row.
func wordmarkWidth() int {
	widest := 0
	for _, row := range logo {
		if w := lipgloss.Width(row); w > widest {
			widest = w
		}
	}
	return widest
}

// welcomeHints are the gestures worth knowing before the first line is typed.
// They are all the page teaches: /help prints the commands and the footer
// carries the state, so the home screen only has to bridge the moment before
// there is any of either.
//
// They differ per view because the keys do. Ctrl+C stops a turn in both, but
// with nothing running it steps out of the side conversation and only ends the
// session from the main one — so a page that stayed silent about the difference
// would be teaching the gesture that lost the session.
func welcomeHints(view int) []string {
	if view == ViewSide {
		return []string{
			"Type a message, or / for the commands",
			"Ctrl+C or Ctrl+O goes back to the main conversation",
		}
	}
	return []string{
		"Type a message, or / for the commands",
		"Ctrl+O switches conversation · Ctrl+C twice exits",
	}
}

// welcomeLines draws the home screen in a body of exactly height rows, centred.
//
// It fills the whole body rather than sitting on its last rows the way the
// transcript does: an empty conversation has nothing to anchor to the bottom,
// and a logo pressed against the prompt would read as a stray block instead of
// as the top of a session.
func welcomeLines(info Welcome, hints []string, height, width int, s Styles) []string {
	if height < 1 {
		return nil
	}
	block := welcomeBlock(info, hints, height, width, s)
	out := make([]string, 0, height)
	for pad := (height - len(block)) / 2; pad > 0; pad-- {
		out = append(out, "")
	}
	out = append(out, block...)
	for len(out) < height {
		out = append(out, "")
	}
	if len(out) > height {
		out = out[:height]
	}
	return out
}

// welcomeBlock is the page itself. A terminal too short for it gets the one
// line that is worth the rows: cutting the tail instead would keep the wordmark
// and lose the version and the gestures, which are the two facts a reader looks
// at this screen for.
//
// The width the mark needs is read off the mark rather than written down twice:
// a page two cells wider than the letters would put them hard against both
// edges, and that reads as cropped, not as centred.
func welcomeBlock(info Welcome, hints []string, height, width int, s Styles) []string {
	if width < wordmarkWidth()+4 {
		return []string{s.Dim.Render(centre(cutCells(welcomeHeadline(info), width), width))}
	}
	block := make([]string, 0, len(logo)+6)
	if strings.TrimSpace(info.Title) == "" {
		for _, row := range logo {
			block = append(block, s.Accent.Render(centre(row, width)))
		}
		block = append(block, "", welcomeRow(welcomeTitle(info, width), width, s.Text))
	} else {
		block = append(block, welcomeRow(info.Title, width, s.Accent))
	}
	if model := welcomeModel(info, width); model != "" {
		block = append(block, welcomeRow(model, width, s.Dim))
	}
	if flash := strings.TrimSpace(info.Flash); flash != "" {
		// The flash is drawn in the ordinary text style against the dim facts
		// above it and the dim hints below: it is the one line on this page
		// that reports rather than describes, and it goes first among the
		// things said in the same size.
		block = append(block, welcomeRow(flash, width, s.Text))
	}
	block = append(block, "")
	for _, hint := range hints {
		// A hint cut in half is not a hint: it is the first half of a sentence
		// the reader has to guess the end of.
		if lipgloss.Width(hint) > width {
			continue
		}
		block = append(block, welcomeRow(hint, width, s.Dim))
	}
	if len(block) > height {
		return []string{welcomeRow(welcomeHeadline(info), width, s.Accent)}
	}
	return block
}

// welcomeHeadline is the page in one line, for a terminal with room for one.
func welcomeHeadline(info Welcome) string {
	if title := strings.TrimSpace(info.Title); title != "" {
		return title
	}
	return welcomeVersion(info)
}

// welcomeRow centres one line and pads it to the width, so every row is the
// same length before the frame is assembled. Padding here rather than in the
// caller is what keeps the page from leaking spaces into the transcript that
// replaces it.
func welcomeRow(text string, width int, style lipgloss.Style) string {
	return style.Render(centre(cutCells(text, width), width))
}

// welcomeVersion is the one fact the page never drops.
func welcomeVersion(info Welcome) string {
	if v := strings.TrimSpace(info.Version); v != "" {
		return "atlas " + v
	}
	return "atlas"
}

// welcomeTitle says what this is and where it runs. The directory is the field
// that goes when there is no room: a reader on a narrow terminal still needs to
// know which build they are looking at.
func welcomeTitle(info Welcome, width int) string {
	title := welcomeVersion(info)
	dir := strings.TrimSpace(info.Dir)
	if dir == "" {
		return title
	}
	if full := title + " · " + dir; lipgloss.Width(full) <= width {
		return full
	}
	return title
}

// welcomeModel says who answers and on what terms. The terms go first, for the
// same reason: the model is what still identifies the session when there is only
// room for one field.
func welcomeModel(info Welcome, width int) string {
	model := strings.TrimSpace(info.Model)
	note := strings.TrimSpace(info.Note)
	if model == "" {
		return note
	}
	if note == "" {
		return model
	}
	if full := model + " · " + note; lipgloss.Width(full) <= width {
		return full
	}
	return model
}

// centre puts text in the middle of the row. An odd remainder goes to the left,
// so the right edge stays a cell further in and the page reads as aligned with
// itself rather than with the terminal.
func centre(text string, width int) string {
	if width < 1 {
		return ""
	}
	gap := width - lipgloss.Width(text)
	if gap <= 0 {
		return text
	}
	return strings.Repeat(" ", gap/2) + text
}
