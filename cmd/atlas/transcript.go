package main

import (
	"fmt"
	"io"
	"strings"

	"pi-go/internal/tui"
	"pi-go/internal/wire"
)

// writeTranscript replays a recorded conversation into the terminal. A resumed
// or continued session already holds messages; a REPL that showed none of them
// would look like the switch had thrown the history away, and the file on disk
// would be the only place left to read it.
//
// The shapes are the ones the full-screen session prints, because that screen is
// the only reader: "> " for a prompt, an answer under the bar that runs down its
// left edge, one branch per tool call with what it produced, and the indented
// body of a failed one. Thinking stays out because a live turn only reports how
// long it took, and that duration is not in the session file.
//
// answers is where a replayed answer goes, or nil to draw it into out. The
// interface passes its own: it wraps an answer inside the bar and cannot do that
// for text handed to it a line at a time, so the block has to be built there.
//
// It reports whether anything was printed, so a session with no conversation —
// a fresh one, or one that recorded only a system prompt — stays quiet.
func writeTranscript(out io.Writer, messages []wire.Message, answers func(text string)) bool {
	count := conversationLength(messages)
	if count == 0 {
		return false
	}
	fmt.Fprintf(out, "\nhistory: %s\n", countText(count))
	results, calls := recordedToolCalls(messages)
	for _, message := range messages {
		body := recordedMessage(message, results, calls, answers)
		if body == "" {
			continue
		}
		fmt.Fprintf(out, "\n%s", body)
	}
	fmt.Fprintln(out)
	return true
}

// recordedMessage renders one message of a replayed conversation, or "" when
// it holds nothing the live renderer would have printed: system messages, a
// thinking-only assistant message, an empty content list, a tool result that is
// already folded into the call that produced it.
func recordedMessage(message wire.Message, results map[string]wire.Message, calls map[string]bool, answers func(string)) string {
	switch message.Role {
	case wire.RoleUser:
		return recordedPrompt(message)
	case wire.RoleAssistant:
		return recordedAssistant(message, results, answers)
	case wire.RoleToolResult:
		return recordedToolResult(message, calls)
	}
	return ""
}

// recordedToolCalls indexes the conversation for replay. A call and its result
// are two records in the file but one line on screen, so the call needs the
// result's size while the result needs to know its call was already shown.
// Results with no call — a fork can keep one and drop the other — are replayed
// on a line of their own rather than dropped.
func recordedToolCalls(messages []wire.Message) (results map[string]wire.Message, calls map[string]bool) {
	results = make(map[string]wire.Message)
	calls = make(map[string]bool)
	for _, message := range messages {
		switch message.Role {
		case wire.RoleAssistant:
			for _, call := range message.ToolCalls() {
				if call.ID != "" {
					calls[call.ID] = true
				}
			}
		case wire.RoleToolResult:
			if message.ToolCallID != "" {
				results[message.ToolCallID] = message
			}
		}
	}
	return results, calls
}

// recordedPrompt prints a prompt under the marker it was typed at. A pasted
// snippet keeps its line breaks; indenting the continuations is what makes it
// read as one message instead of several prompts. An image-only prompt keeps a
// marker instead of vanishing: the answer under it would otherwise look
// unprompted.
func recordedPrompt(message wire.Message) string {
	text := promptText(message)
	if strings.TrimSpace(text) == "" {
		text = imageMarkers(message)
	}
	if strings.TrimSpace(text) == "" {
		return ""
	}
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	var b strings.Builder
	for index, line := range lines {
		if index == 0 {
			b.WriteString("> " + line + "\n")
			continue
		}
		b.WriteString(strings.TrimRight("  "+line, " ") + "\n")
	}
	return b.String()
}

// promptText is what the reader typed, without the tags that say what their
// references turned into.
//
// Both halves of an attachment are stored: the line, then the file's contents
// under its path. The second half is what the model was handed, and replaying a
// resumed session with it would print a whole file under the prompt that asked
// about it. It is still in the export and in the request, which is where it
// belongs.
func promptText(message wire.Message) string {
	if message.Content.IsPlain() {
		return message.Content.PlainText()
	}
	var parts []string
	for _, block := range message.Content.Items() {
		if block.Type != wire.ContentText || block.Text == "" || attachmentTag(block.Text) {
			continue
		}
		parts = append(parts, block.Text)
	}
	return strings.Join(parts, "\n")
}

// attachmentTag reports whether a text block is one of the tags attachMessage
// wrote. The shape is pi's, which is why it can be recognized rather than
// marked: <file name="…">…</file>.
func attachmentTag(text string) bool {
	return strings.HasPrefix(text, `<file name="`) && strings.HasSuffix(text, "</file>")
}

// imageMarkers names the images in a message the way the export does, so a
// prompt that was a screenshot does not replay as nothing at all.
func imageMarkers(message wire.Message) string {
	var parts []string
	for _, block := range message.Content.Items() {
		if block.Type == wire.ContentImage {
			parts = append(parts, "[image: "+block.MIMEType+"]")
		}
	}
	return strings.Join(parts, "\n")
}

// recordedAssistant prints what the turn produced, in the order it produced it:
// the answer text, then the calls it made. A turn that ended in an error
// reports the reason, because otherwise the replay would show a prompt, no
// answer, and no explanation.
func recordedAssistant(message wire.Message, results map[string]wire.Message, answers func(string)) string {
	var b strings.Builder
	last := blockNone
	for _, block := range message.Content.Items() {
		switch block.Type {
		case wire.ContentText:
			text := strings.TrimRight(block.Text, "\n")
			if strings.TrimSpace(text) == "" {
				continue
			}
			if answers != nil {
				answers(text)
			} else {
				if last != blockNone {
					b.WriteString("\n")
				}
				b.WriteString(replayedAnswer(text))
			}
			last = blockText
		case wire.ContentToolCall:
			if last == blockText {
				b.WriteString("\n")
			}
			b.WriteString(recordedCall(block, results) + "\n")
			last = blockTree
		}
	}
	if message.StopReason == wire.StopError && message.ErrorMessage != "" {
		b.WriteString("error: " + message.ErrorMessage + "\n")
	}
	return b.String()
}

// replayedAnswer puts an answer under the bar the full-screen session draws,
// on every line rather than only the first: a replay is read in the same place
// the live turn was, so a two-line answer that came back as a bullet and an
// indent would look like a different kind of message from the ones below it.
func replayedAnswer(text string) string {
	lines := strings.Split(text, "\n")
	var b strings.Builder
	for _, line := range lines {
		b.WriteString(strings.TrimRight(tui.AnswerBar+line, " ") + "\n")
	}
	return b.String()
}

// recordedCall prints one call as the single branch the live renderer left
// behind: what ran, with what arguments, and what came back.
func recordedCall(call wire.Content, results map[string]wire.Message) string {
	body := "Ran  " + call.Name
	if args := compactJSON(call.Arguments, 120); args != "" {
		body += "  " + args
	}
	result, ok := results[call.ID]
	if !ok {
		// A call with no recorded result was cut off mid-flight. Saying so
		// beats a line that looks like it finished.
		return branchMarker + body + " · no result"
	}
	texts := []string{result.Text()}
	line := branchMarker + body + " · " + toolOutcome(texts, result.IsErrorResult())
	if !result.IsErrorResult() {
		return line
	}
	if text := resultBody(texts); text != "" {
		line += "\n" + indent(text, detailIndent)
	}
	return line
}

// recordedToolResult prints a result whose call is not in the conversation. The
// call it belongs to is gone, so the line carries what is left: the tool, its
// size, and the body of a failure — the same rule the live renderer follows.
func recordedToolResult(message wire.Message, calls map[string]bool) string {
	if message.ToolCallID != "" && calls[message.ToolCallID] {
		// Already shown on its call's branch.
		return ""
	}
	name := message.ToolName
	if name == "" {
		name = "tool"
	}
	texts := []string{message.Text()}
	line := branchMarker + "Ran  " + name + " · " + toolOutcome(texts, message.IsErrorResult()) + "\n"
	if !message.IsErrorResult() {
		return line
	}
	if body := resultBody(texts); body != "" {
		line += indent(body, detailIndent) + "\n"
	}
	return line
}
