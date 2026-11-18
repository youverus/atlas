package transcript

import (
	"strings"

	"pi-go/internal/wire"
)

// How much of a call the window carries for its card to open. A tool that read
// a megabyte is not one the page should hold in the DOM; the file drawer is
// where a whole file belongs. The terminal never reads these two fields.
const (
	argsLimit   = 4000
	resultLimit = 20000
)

// Replay rebuilds the blocks a recorded conversation would have produced live.
//
// A session file is the only place a past conversation exists, so a window that
// switches to one has to fold those messages through the same role split the
// live stream uses: a prompt is one block, an answer is one block, and a call
// and its result are one line. Doing that anywhere else would give the window a
// second opinion on what counts as one tool call.
//
// Thinking stays out. A live turn only reports how long it took, and that
// duration is not in the file, so replayed reasoning would be a block with no
// content or a made-up one.
func Replay(messages []wire.Message) []Block {
	results, calls := recordedCalls(messages)
	blocks := make([]Block, 0, len(messages))
	for _, message := range messages {
		switch message.Role {
		case wire.RoleUser:
			if text := replayPrompt(message); text != "" {
				blocks = append(blocks, Block{Role: RoleUser, Text: text})
			}
		case wire.RoleAssistant:
			blocks = replayAssistant(blocks, message, results)
		case wire.RoleToolResult:
			if message.ToolCallID != "" && calls[message.ToolCallID] {
				// Already drawn on the branch of the call that produced it.
				continue
			}
			blocks = append(blocks, replayOrphanResult(message))
		}
	}
	return blocks
}

// recordedCalls indexes the conversation for replay. A call and its result are
// two records in the file but one line on screen, so the call needs the
// result's outcome while the result needs to know its call was already drawn.
func recordedCalls(messages []wire.Message) (results map[string]wire.Message, calls map[string]bool) {
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

// replayPrompt is what the reader typed. The second half of an attachment — the
// file's own text under the line that referenced it — is what the model was
// handed, and drawing it would show a whole file under a prompt that asked
// about it.
func replayPrompt(message wire.Message) string {
	if message.Content.IsPlain() {
		return strings.TrimRight(message.Content.PlainText(), "\n")
	}
	var parts []string
	for _, block := range message.Content.Items() {
		switch {
		case block.Type == wire.ContentText && block.Text != "" && !attachmentTag(block.Text):
			parts = append(parts, block.Text)
		case block.Type == wire.ContentImage:
			parts = append(parts, "[image: "+block.MIMEType+"]")
		}
	}
	return strings.TrimRight(strings.Join(parts, "\n"), "\n")
}

// attachmentTag reports whether a text block is one of the tags the writer of
// an attachment used. The shape is pi's, so it is recognized rather than
// marked: <file name="…">…</file>.
func attachmentTag(text string) bool {
	return strings.HasPrefix(text, `<file name="`) && strings.HasSuffix(text, "</file>")
}

// replayAssistant appends what one turn produced, in the order it produced it.
func replayAssistant(blocks []Block, message wire.Message, results map[string]wire.Message) []Block {
	for _, block := range message.Content.Items() {
		switch block.Type {
		case wire.ContentText:
			text := strings.TrimRight(block.Text, "\n")
			if strings.TrimSpace(text) == "" {
				continue
			}
			blocks = append(blocks, Block{Role: RoleAnswer, Text: text})
		case wire.ContentToolCall:
			blocks = append(blocks, replayCall(block, results))
		}
	}
	if message.StopReason == wire.StopError && message.ErrorMessage != "" {
		blocks = append(blocks, Block{Role: RoleError, Text: message.ErrorMessage})
	}
	return blocks
}

// replayCall draws one call as the single line the live renderer left behind:
// what ran, with what arguments, and what came back. The line is the claim; the
// arguments and the result are what a reader opens the card to read, and they
// are carried in full because a preview is exactly what a card already shows.
func replayCall(call wire.Content, results map[string]wire.Message) Block {
	note := compactJSON(call.Arguments, 120)
	args := indentJSON(call.Arguments, argsLimit)
	result, ok := results[call.ID]
	if !ok {
		// A call with no recorded result was cut off mid-flight. Saying so
		// beats a line that looks like it finished.
		return Block{
			Role: RoleTool,
			Name: call.Name,
			ID:   call.ID,
			Note: joinNote(note, "no result"),
			Args: args,
		}
	}
	outcome := "done"
	if result.IsErrorResult() {
		outcome = "failed"
	}
	block := Block{
		Role:   RoleTool,
		Name:   call.Name,
		ID:     call.ID,
		Note:   joinNote(note, outcome),
		Args:   args,
		Result: truncate(result.Text(), resultLimit),
	}
	if result.IsErrorResult() {
		block.Detail = truncate(result.Text(), 2000)
	}
	return block
}

// replayOrphanResult draws a result whose call is gone. A fork can keep one and
// drop the other, and hiding the survivor would look like the work never ran.
func replayOrphanResult(message wire.Message) Block {
	name := message.ToolName
	if name == "" {
		name = "tool"
	}
	outcome := "done"
	if message.IsErrorResult() {
		outcome = "failed"
	}
	block := Block{
		Role:   RoleTool,
		Name:   name,
		ID:     message.ToolCallID,
		Note:   outcome,
		Result: truncate(message.Text(), resultLimit),
	}
	if message.IsErrorResult() {
		block.Detail = truncate(message.Text(), 2000)
	}
	return block
}
