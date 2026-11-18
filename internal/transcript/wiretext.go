package transcript

// The small conversions the fold needs on the way from a wire event to a line
// of text. They live here rather than in a renderer because the fold decides
// what a line says: "failed", "1 output line", "Thought for 1.2s" are the same
// words in every frontend, and only their styling differs. cmd/atlas/render.go
// keeps its own copies for the inline interface, which is deleted.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"pi-go/internal/wire"
)

// humanDuration renders a duration the way a reader thinks about it: in
// milliseconds under a second, tenths under a minute, and a clock reading past
// that.
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

// compactJSON flattens a JSON argument object onto one line and bounds its
// length. Re-encoding would reorder the keys the model sent, which makes a diff
// between two runs of the same call harder to read.
func compactJSON(raw json.RawMessage, limit int) string {
	if len(raw) == 0 {
		return ""
	}
	return truncate(strings.Join(strings.Fields(string(raw)), " "), limit)
}

// indentJSON re-renders a recorded argument object as something a reader can
// scan. The wire keeps arguments as one line of JSON, and one line of JSON is
// the one shape nobody reads. Text that is not JSON is returned as it stands,
// because a tool taking a plain string should not be shown in quotes.
func indentJSON(raw json.RawMessage, limit int) string {
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return ""
	}
	var out bytes.Buffer
	if err := json.Indent(&out, []byte(text), "", "  "); err != nil {
		return truncate(text, limit)
	}
	return truncate(out.String(), limit)
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

// resultBody bounds the text a result carries, so one failing command that
// dumps a log cannot take over the screen.
func resultBody(texts []string) string {
	return truncate(strings.TrimSpace(strings.Join(texts, "\n")), 800)
}

// progressCells is how much of a progress message fits on the marker line. The
// tail is one line by contract, so an update that came with a paragraph — or a
// stack trace — is cut the same way an argument preview is.
const ProgressCells = 60

// shortProgress folds a server's progress message onto that one line.
func shortProgress(text string) string {
	return truncate(strings.Join(strings.Fields(text), " "), ProgressCells)
}

func truncate(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	if limit <= 1 {
		return string(runes[:limit])
	}
	return string(runes[:limit-1]) + "…"
}

// compactionLine reports what a compaction did. A failed one reports the reason
// instead of numbers, because the numbers were never produced.
func compactionLine(event wire.AgentEvent) string {
	if event.ErrorMessage != "" {
		return "Compaction failed: " + event.ErrorMessage
	}
	var result wire.CompactionResult
	_ = json.Unmarshal(event.Result, &result)
	if result.EstimatedTokensAfter != nil {
		return fmt.Sprintf("Compacted  %d → %d tokens", result.TokensBefore, *result.EstimatedTokensAfter)
	}
	return fmt.Sprintf("Compacted  %d tokens", result.TokensBefore)
}
