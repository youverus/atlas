package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"pi-go/internal/app"
	"pi-go/internal/tool"
	"pi-go/internal/wire"
)

// The transcript is read as a tree: the answer is a solid bullet at the margin,
// and everything the agent did to produce it hangs off a trunk two columns in.
// One bullet per thing the model said, one branch per thing it ran, so a turn
// reads as a block instead of a wall of equal-weight lines.
const (
	textMarker   = "● "
	textIndent   = "  "
	branchMarker = "  ├ • "
	detailIndent = "    "

	// A side conversation uses the same tree with an open bullet. Two filled
	// bullets in one scrollback would leave the reader guessing which
	// conversation a branch belonged to.
	sideTextMarker   = "○ "
	sideBranchMarker = "  ├ ○ "
)

// renderer turns one agent event stream into something a human reads: streamed
// assistant text, tool calls in flight, thinking, compaction progress, and a
// one-line summary at the end of a turn.
//
// The JSONL mode bypasses all of it. The protocol shape is the compatibility
// contract, and pretty-printing it would create a second format to keep in step
// with pi.
type renderer struct {
	out    io.Writer
	errOut io.Writer
	asJSON bool
	json   *json.Encoder

	// now is the clock behind "Thought for 6.4s". It is a field so a test can
	// pin the duration instead of racing it.
	now func() time.Time

	// mark and branch are the bullet and the connector of the conversation
	// being written. They are fields rather than constants because a side
	// conversation shares this renderer with the main one and has to be
	// recognizable in the scrollback afterwards.
	mark   string
	branch string

	// wrote records that the current assistant message printed text, so a
	// provider that sends no deltas still shows its message.
	wrote bool
	// open marks a text block whose last line has not been terminated. Every
	// other marker closes it first, because a branch cannot start mid-sentence.
	open bool
	// lineStart marks that the next character of the open block starts a line,
	// and so needs the indent that sits under the bullet.
	lineStart bool
	// last is the kind of block on screen. It decides whether the next block
	// gets a blank line above it: text and tree never butt together, and
	// consecutive branches stay tight because they are one block.
	last blockKind

	// thinkingStarted is when the current thinking block began; the closing
	// line reports how long the model spent there.
	thinkingStarted time.Time

	// tool is the call whose line is still open on screen. Its outcome is
	// appended when the call ends, so a long call shows what it is doing while
	// it runs rather than after the fact.
	tool toolLine
	// approval is the call whose line was closed to ask about it. Its result
	// continues under the question instead of starting a second "Ran" line,
	// which would read as the call having run twice.
	approval toolLine
}

// blockKind is what the last thing written to the terminal was, which is all
// the blank-line rule needs.
type blockKind int

const (
	blockNone blockKind = iota
	blockText
	blockTree
)

// toolLine names the call whose branch is still open. The id is what tells a
// result that can append from one that has to start its own line.
type toolLine struct {
	id   string
	open bool
}

func newRenderer(out, errOut io.Writer, asJSON bool) *renderer {
	encoder := json.NewEncoder(out)
	// The protocol forbids escaping HTML: "<" in a diff must arrive as "<".
	encoder.SetEscapeHTML(false)
	return &renderer{
		out:    out,
		errOut: errOut,
		asJSON: asJSON,
		json:   encoder,
		now:    time.Now,
		mark:   textMarker,
		branch: branchMarker,
	}
}

// setSide points the renderer at one conversation or the other. The markers
// change, nothing else does: a side answer is drawn by the same code as a main
// answer because it is the same kind of thing, and only the transcript needs to
// tell them apart later.
func (r *renderer) setSide(on bool) {
	if on {
		r.mark, r.branch = sideTextMarker, sideBranchMarker
		return
	}
	r.mark, r.branch = textMarker, branchMarker
}

// event is the emit callback handed to the kernel.
func (r *renderer) event(event wire.AgentEvent) error {
	if r.asJSON {
		return r.json.Encode(event)
	}

	switch event.Type {
	case wire.AgentEventMessageStart:
		// The prompt and the tool results also start messages; only the
		// assistant's own start says the next text block is a fresh one.
		if event.Message != nil && event.Message.Role == wire.RoleAssistant {
			r.wrote = false
		}
	case wire.AgentEventMessageUpdate:
		return r.assistantUpdate(event.AssistantEvent)
	case wire.AgentEventMessageEnd:
		return r.assistantEnd(event.Message)
	case wire.AgentEventToolExecStart:
		return r.toolStart(event)
	case wire.AgentEventToolExecEnd:
		return r.toolEnd(event)
	case wire.AgentEventCompactionStart:
		return r.treeLine("Compacting  " + reasonOf(event.Reason))
	case wire.AgentEventCompactionEnd:
		return r.treeLine(compactionLine(event))
	case wire.AgentEventEnd:
		// Nothing should still be in flight, but a turn cut short by a
		// cancellation never sends the result: close the branch so the summary
		// on stderr does not land on the end of it.
		if err := r.endText(); err != nil {
			return err
		}
		// A question answered by a cancellation leaves a marker aimed at a
		// result that will never arrive, so it goes with the branch.
		r.approval = toolLine{}
		return r.closeTool(" · interrupted")
	}
	return nil
}

// assistantUpdate handles the streamed half of a message: text that arrives
// while it is still being written, and the thinking block around it.
func (r *renderer) assistantUpdate(assistant *wire.AssistantEvent) error {
	if assistant == nil {
		return nil
	}
	switch assistant.Type {
	case wire.EventTextDelta:
		if assistant.Delta == "" {
			return nil
		}
		if err := r.startText(); err != nil {
			return err
		}
		return r.writeText(assistant.Delta)
	case wire.EventThinkingStart:
		r.thinkingStarted = r.now()
	case wire.EventThinkingEnd:
		if r.thinkingStarted.IsZero() {
			// No start was streamed, so there is no duration to report and a
			// made-up one would be worse than silence.
			return nil
		}
		elapsed := r.now().Sub(r.thinkingStarted)
		r.thinkingStarted = time.Time{}
		return r.treeLine("Thought for " + humanDuration(elapsed))
	}
	return nil
}

// assistantEnd closes the message. A provider that never sent deltas still has
// its answer on the message itself, and dropping it would leave a prompt with
// nothing under it.
func (r *renderer) assistantEnd(message *wire.Message) error {
	if message == nil || message.Role != wire.RoleAssistant {
		return nil
	}
	if !r.wrote {
		if text := message.Text(); text != "" {
			if err := r.startText(); err != nil {
				return err
			}
			if err := r.writeText(text); err != nil {
				return err
			}
		}
	}
	return r.endText()
}

// toolStart opens the branch for a call. The line has no newline yet: the
// outcome is appended when the call returns, so the branch reads as one line
// whether it took ten milliseconds or ten seconds.
func (r *renderer) toolStart(event wire.AgentEvent) error {
	if err := r.endText(); err != nil {
		return err
	}
	if err := r.closeTool(" · running"); err != nil {
		return err
	}
	if err := r.separate(blockTree); err != nil {
		return err
	}
	r.last = blockTree
	body := "Ran  " + event.ToolName
	if args := compactJSON(event.Args, 120); args != "" {
		body += "  " + args
	}
	if _, err := io.WriteString(r.out, r.branch+body); err != nil {
		return err
	}
	r.tool = toolLine{id: event.ToolCallID, open: true}
	return nil
}

// toolEnd completes a branch. When another call started while this one was in
// flight its line is already closed, so the outcome goes on a line of its own
// rather than being dropped.
func (r *renderer) toolEnd(event wire.AgentEvent) error {
	failed := event.IsError != nil && *event.IsError
	tail := " · " + toolOutcome(resultTexts(event.Result), failed)
	if millis := event.DurationMs; millis != nil && *millis >= 1000 {
		// Under a second the number is noise on every line; over it, it is the
		// first thing that explains a turn that felt slow.
		tail += " · " + humanDuration(time.Duration(*millis)*time.Millisecond)
	}

	if r.tool.open && r.tool.id == event.ToolCallID {
		if err := r.endText(); err != nil {
			return err
		}
		r.tool = toolLine{}
		if _, err := io.WriteString(r.out, tail+"\n"); err != nil {
			return err
		}
	} else if r.approval.open && r.approval.id == event.ToolCallID {
		// The line was closed to ask, so the outcome continues under the
		// question instead of naming the call a second time.
		r.approval = toolLine{}
		if err := r.endText(); err != nil {
			return err
		}
		if _, err := io.WriteString(r.out, detailIndent+strings.TrimPrefix(tail, " · ")+"\n"); err != nil {
			return err
		}
	} else if err := r.treeLine("Ran  " + event.ToolName + tail); err != nil {
		return err
	}

	// A failure is the one result worth showing unbidden: the model sees it,
	// but the command that broke is the human's to fix.
	if !failed {
		return nil
	}
	text := resultText(event.Result)
	if text == "" {
		return nil
	}
	_, err := io.WriteString(r.out, indent(text, detailIndent)+"\n")
	return err
}

// treeLine writes one branch of the current tree. Every line uses the same
// connector on purpose: with calls running in parallel any line can turn out to
// be the last, and a corner glyph would have to be taken back out of the
// scrollback once another arrived.
func (r *renderer) treeLine(body string) error {
	if err := r.endText(); err != nil {
		return err
	}
	if err := r.closeTool(" · running"); err != nil {
		return err
	}
	if err := r.separate(blockTree); err != nil {
		return err
	}
	r.last = blockTree
	_, err := io.WriteString(r.out, r.branch+body+"\n")
	return err
}

// closeTool terminates a branch whose own result has not come back, naming why.
func (r *renderer) closeTool(tail string) error {
	if !r.tool.open {
		return nil
	}
	r.tool = toolLine{}
	_, err := io.WriteString(r.out, tail+"\n")
	return err
}

// approvalSubject is the one line that names what the call would do: the tool
// and its arguments. Both readers show it, so the terminal box and the piped
// question cannot describe the same call two ways.
func approvalSubject(call tool.Call) string {
	subject := call.Name
	if compact := compactJSON(call.Arguments, 100); compact != "" {
		subject += "  " + compact
	}
	return subject
}

// startText opens an answer with the solid bullet, on a line of its own.
func (r *renderer) startText() error {
	if r.open {
		return nil
	}
	if err := r.closeTool(" · running"); err != nil {
		return err
	}
	if err := r.separate(blockText); err != nil {
		return err
	}
	if _, err := io.WriteString(r.out, r.mark); err != nil {
		return err
	}
	r.open = true
	r.lineStart = false
	r.wrote = true
	r.last = blockText
	return nil
}

// writeText prints a delta, indenting the lines it starts so a multi-line
// answer stays lined up under the bullet instead of running back to the margin
// where it would read as several messages.
func (r *renderer) writeText(delta string) error {
	for index, line := range strings.Split(delta, "\n") {
		if index > 0 {
			if _, err := io.WriteString(r.out, "\n"); err != nil {
				return err
			}
			r.lineStart = true
		}
		if line == "" {
			continue
		}
		if r.lineStart {
			if _, err := io.WriteString(r.out, textIndent); err != nil {
				return err
			}
			r.lineStart = false
		}
		if _, err := io.WriteString(r.out, line); err != nil {
			return err
		}
	}
	return nil
}

// endText terminates the open block. A message that printed nothing leaves open
// false, and then this is a no-op rather than a blank line.
func (r *renderer) endText() error {
	if !r.open {
		return nil
	}
	r.open = false
	r.lineStart = false
	_, err := io.WriteString(r.out, "\n")
	return err
}

// separate writes the blank line between two blocks of different kinds. The gap
// is what makes a bullet read as the start of something rather than a
// continuation of the branch above it.
func (r *renderer) separate(next blockKind) error {
	if r.last == blockNone || r.last == blockTree && next == blockTree {
		return nil
	}
	_, err := io.WriteString(r.out, "\n")
	return err
}

// summary reports what the turn cost. It goes to stderr so a caller can pipe
// stdout to a file and keep the transcript clean.
//
// The parts are the ones the full-screen session prints, joined with commas
// instead of its dots: one fact, spelled once, so a reader who has both front
// ends open does not have to translate between them.
func (r *renderer) summary(result app.Result, cost turnCost) {
	if r.asJSON {
		return
	}
	stop := string(result.StopReason)
	if stop == "" {
		stop = "unknown"
	}
	fields := append(cost.fields(result.Usage), "stop reason "+stop)
	fmt.Fprintf(r.errOut, "[%s]\n", strings.Join(fields, ", "))
}

// humanDuration is how long something took, in the unit a human would say it
// in: milliseconds under a second, tenths under a minute, and a clock reading
// past that.
func humanDuration(duration time.Duration) string {
	switch {
	case duration < time.Second:
		return fmt.Sprintf("%dms", duration.Milliseconds())
	case duration < time.Minute:
		return fmt.Sprintf("%.1fs", duration.Seconds())
	}
	seconds := int(duration.Seconds())
	return fmt.Sprintf("%dm%02ds", seconds/60, seconds%60)
}

// toolOutcome names what a call produced. Zero is worth saying: a call that
// worked and said nothing looks identical to one that never ran otherwise.
func toolOutcome(texts []string, failed bool) string {
	if failed {
		return "failed"
	}
	return pluralLines(lineCount(texts))
}

// lineCount counts the non-blank lines of a tool result, which is the size of
// what the model actually reads back.
func lineCount(texts []string) int {
	count := 0
	for _, text := range texts {
		for _, line := range strings.Split(text, "\n") {
			if strings.TrimSpace(line) != "" {
				count++
			}
		}
	}
	return count
}

// pluralLines names a number of output lines.
func pluralLines(count int) string {
	switch count {
	case 0:
		return "no output"
	case 1:
		return "1 output line"
	}
	return fmt.Sprintf("%d output lines", count)
}

func reasonOf(reason string) string {
	if reason == "" {
		return "start"
	}
	return reason
}

// compactionLine reports what a compaction did. A failed one reports the reason
// instead of numbers, because the numbers were never produced.
//
// An overflow failure is the exception: its message is already a sentence naming
// the overflow and the way out, so prefixing it would say the same thing twice.
func compactionLine(event wire.AgentEvent) string {
	if event.ErrorMessage != "" {
		if event.Reason == wire.CompactionReasonOverflow {
			return event.ErrorMessage
		}
		return "Compaction failed: " + event.ErrorMessage
	}
	var result wire.CompactionResult
	_ = json.Unmarshal(event.Result, &result)
	if result.EstimatedTokensAfter != nil {
		return fmt.Sprintf("Compacted  %d → %d tokens", result.TokensBefore, *result.EstimatedTokensAfter)
	}
	return fmt.Sprintf("Compacted  %d tokens", result.TokensBefore)
}

// compactJSON flattens a JSON argument object onto one line and bounds its
// length. Re-encoding would reorder the keys the model sent, which makes a diff
// between two runs of the same call harder to read.
func compactJSON(raw json.RawMessage, limit int) string {
	if len(raw) == 0 {
		return ""
	}
	return truncate(strings.Join(strings.Fields(string(raw)), " "), limit)
}

// resultTexts extracts the text of a tool result. The payload is the kernel's
// own encoding of tool.Result, content blocks included.
func resultTexts(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var payload struct {
		Content []wire.Content `json:"content"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil
	}
	var texts []string
	for _, block := range payload.Content {
		if block.Text != "" {
			texts = append(texts, block.Text)
		}
	}
	return texts
}

// resultText is the body of a tool result, bounded so one failing command that
// dumps a log cannot take over the terminal.
func resultText(raw json.RawMessage) string {
	return resultBody(resultTexts(raw))
}

// resultBody bounds the text a result carries. The same limit is used live and
// on replay, so a failure reads the same both ways.
func resultBody(texts []string) string {
	return truncate(strings.TrimSpace(strings.Join(texts, "\n")), 800)
}

func truncate(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "..."
}

func indent(text, prefix string) string {
	lines := strings.Split(text, "\n")
	for index, line := range lines {
		lines[index] = prefix + line
	}
	return strings.Join(lines, "\n")
}
