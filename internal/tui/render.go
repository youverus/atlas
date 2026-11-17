package tui

import (
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
)

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
//
// It is a function rather than a method because a method can only be written
// where its type is declared, and the type now lives in internal/transcript.
func head(b Block) string {
	line := b.Name
	if b.Text != "" {
		line += "  " + b.Text
	}
	if b.Progress != "" {
		line += " · " + b.Progress
	}
	if b.Note != "" {
		line += " · " + b.Note
	}
	return line
}

func toolBlock(block Block, width int, s Styles) []string {
	out := marked(head(block), depthMarker, depthIndent, width, s.Tool, s.Tool)
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
		for _, line := range wrapText(head(block), inner) {
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
