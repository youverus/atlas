package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// approvalBox draws the question as a bordered box: a title, what the tool would
// do, and the keys that answer it.
//
// The box is painted on the raised surface, like the prompt's frame, so it reads
// as a panel above the conversation instead of text floating in it. Its frame
// and title are the warn color: unlike a menu, which offers choices, this
// question stops the turn until it is answered, and the color is the difference
// between "pick one" and "decide now".
//
// The rows behind the box are replaced rather than composited. A real overlay
// would re-draw the transcript underneath, which means re-wrapping it at a
// narrower width for the duration of the question — and the answer is often the
// thing that was on screen, so it must not move while it is being decided.
func approvalBox(request AskRequest, width int, s Styles) []string {
	if width < 12 {
		width = 12
	}
	panel := s.Panel()
	inner := width - 2 // between the vertical borders
	body := inner - 2  // one cell of padding on each side

	subject := request.Subject
	if strings.TrimSpace(subject) == "" {
		subject = "this tool call?"
	}

	type row struct {
		text  string
		style lipgloss.Style
	}
	rows := []row{{text: "allow", style: panel.Warn.Bold(true)}}
	for _, line := range wrapText(subject, body) {
		rows = append(rows, row{text: line, style: panel.Text})
	}
	// The third key names the tool and the lifetime it would get. A footer that
	// said "always allow" would be a different promise: the answer is spent by
	// this session, and everything outside it is still asked. Both halves of the
	// scope are in the sentence because both are decisions — which tool, and for
	// how long — and neither can be inferred from the other.
	for _, line := range wrapText(approvalKeys(request.Tool), body) {
		rows = append(rows, row{text: line, style: panel.Dim})
	}

	out := make([]string, 0, len(rows)+2)
	out = append(out, panel.Warn.Render("╭"+strings.Repeat("─", inner)+"╮"))
	for _, row := range rows {
		out = append(out, boxRow(row.style.Render(row.text), body, panel))
	}
	out = append(out, panel.Warn.Render("╰"+strings.Repeat("─", inner)+"╯"))
	return out
}

// approvalKeys is the line under the question: the keys, and what the third one
// covers. A request without a tool has two answers, so it advertises two.
func approvalKeys(tool string) string {
	if tool = strings.TrimSpace(tool); tool == "" {
		return "y yes · n no"
	}
	return "y yes · a " + tool + " for this session · n no"
}

// approvalSubject is the sentence a gate puts in front of the reader: what the
// call is, in the same shape the transcript names it.
func approvalSubject(name, detail string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "a tool"
	}
	subject := "Ran  " + name
	if detail = strings.TrimSpace(detail); detail != "" {
		subject += "  " + detail
	}
	return subject
}
