package tool

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Output limits shared by the file and shell tools. Truncation is based on two
// independent limits, and whichever is hit first wins. The numbers match pi so
// a Go tool result and a pi tool result stay comparable.
const (
	DefaultMaxLines   = 2000
	DefaultMaxBytes   = 50 * 1024
	GrepMaxLineLength = 500
)

// Truncation describes a shortened output. The JSON names follow pi's
// TruncationResult because tool details are written into sessions.
type Truncation struct {
	Content     string `json:"content"`
	Truncated   bool   `json:"truncated"`
	TruncatedBy string `json:"truncatedBy,omitempty"` // "lines" or "bytes"

	TotalLines  int `json:"totalLines"`
	TotalBytes  int `json:"totalBytes"`
	OutputLines int `json:"outputLines"`
	OutputBytes int `json:"outputBytes"`

	// LastLinePartial is set by TruncateTail when a single trailing line is
	// larger than the byte limit and only its tail is kept.
	LastLinePartial bool `json:"lastLinePartial"`
	// FirstLineExceedsLimit is set by TruncateHead when the very first line is
	// larger than the byte limit, so no complete line fits at all.
	FirstLineExceedsLimit bool `json:"firstLineExceedsLimit"`

	MaxLines int `json:"maxLines"`
	MaxBytes int `json:"maxBytes"`
}

// Limits overrides the defaults. A zero field keeps the default value.
type Limits struct {
	MaxLines int
	MaxBytes int
}

func (l Limits) resolve() (int, int) {
	maxLines, maxBytes := l.MaxLines, l.MaxBytes
	if maxLines <= 0 {
		maxLines = DefaultMaxLines
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	return maxLines, maxBytes
}

// TruncateHead keeps the first lines that fit. It is what a file read needs:
// the beginning is where a file's structure lives.
//
// It never returns a partial line. When the first line alone exceeds the byte
// limit it returns an empty Content and sets FirstLineExceedsLimit, letting the
// caller point the model at a byte-range command instead.
func TruncateHead(content string, limits Limits) Truncation {
	maxLines, maxBytes := limits.resolve()
	totalBytes := len(content)
	lines := splitLines(content)
	totalLines := len(lines)

	if totalLines <= maxLines && totalBytes <= maxBytes {
		return Truncation{
			Content:     content,
			TotalLines:  totalLines,
			TotalBytes:  totalBytes,
			OutputLines: totalLines,
			OutputBytes: totalBytes,
			MaxLines:    maxLines,
			MaxBytes:    maxBytes,
		}
	}

	if len(lines) > 0 && len(lines[0]) > maxBytes {
		return Truncation{
			Truncated:             true,
			TruncatedBy:           "bytes",
			TotalLines:            totalLines,
			TotalBytes:            totalBytes,
			FirstLineExceedsLimit: true,
			MaxLines:              maxLines,
			MaxBytes:              maxBytes,
		}
	}

	kept := make([]string, 0, maxLines)
	keptBytes := 0
	truncatedBy := "lines"
	for i, line := range lines {
		if i >= maxLines {
			break
		}
		// The +1 accounts for the newline that joins this line to the previous.
		cost := len(line)
		if i > 0 {
			cost++
		}
		if keptBytes+cost > maxBytes {
			truncatedBy = "bytes"
			break
		}
		kept = append(kept, line)
		keptBytes += cost
	}
	if len(kept) >= maxLines && keptBytes <= maxBytes {
		truncatedBy = "lines"
	}

	output := strings.Join(kept, "\n")
	return Truncation{
		Content:     output,
		Truncated:   true,
		TruncatedBy: truncatedBy,
		TotalLines:  totalLines,
		TotalBytes:  totalBytes,
		OutputLines: len(kept),
		OutputBytes: len(output),
		MaxLines:    maxLines,
		MaxBytes:    maxBytes,
	}
}

// TruncateTail keeps the last lines that fit, which is what a command output
// needs: errors and final results live at the end.
//
// When the last line alone exceeds the byte limit it keeps that line's tail and
// sets LastLinePartial.
func TruncateTail(content string, limits Limits) Truncation {
	maxLines, maxBytes := limits.resolve()
	totalBytes := len(content)
	lines := splitLines(content)
	totalLines := len(lines)

	if totalLines <= maxLines && totalBytes <= maxBytes {
		return Truncation{
			Content:     content,
			TotalLines:  totalLines,
			TotalBytes:  totalBytes,
			OutputLines: totalLines,
			OutputBytes: totalBytes,
			MaxLines:    maxLines,
			MaxBytes:    maxBytes,
		}
	}

	kept := make([]string, 0, maxLines)
	keptBytes := 0
	truncatedBy := "lines"
	lastLinePartial := false

	for i := len(lines) - 1; i >= 0 && len(kept) < maxLines; i-- {
		cost := len(lines[i])
		if len(kept) > 0 {
			cost++
		}
		if keptBytes+cost > maxBytes {
			truncatedBy = "bytes"
			if len(kept) == 0 {
				// The final line alone is too large; keep as much of its end as
				// fits so the model still sees the last error message.
				tail := tailBytes(lines[i], maxBytes)
				kept = append([]string{tail}, kept...)
				keptBytes = len(tail)
				lastLinePartial = true
			}
			break
		}
		kept = append([]string{lines[i]}, kept...)
		keptBytes += cost
	}
	if len(kept) >= maxLines && keptBytes <= maxBytes {
		truncatedBy = "lines"
	}

	output := strings.Join(kept, "\n")
	return Truncation{
		Content:         output,
		Truncated:       true,
		TruncatedBy:     truncatedBy,
		TotalLines:      totalLines,
		TotalBytes:      totalBytes,
		OutputLines:     len(kept),
		OutputBytes:     len(output),
		LastLinePartial: lastLinePartial,
		MaxLines:        maxLines,
		MaxBytes:        maxBytes,
	}
}

// omittedFormat is the marker that stands where the middle was. It names a byte
// count rather than a line count because the cut is byte based, and a reader who
// wants the rest knows what to ask for.
const omittedFormat = "\n… %d bytes omitted …\n"

// TruncateMiddle keeps the beginning and the end of content and cuts the middle
// out. A tool result has that shape: a header or a summary sits at each end, and
// the bulk in between is the part nobody reads twice.
//
// The cut is byte based because the omitted region has no line structure worth
// preserving, while both kept halves stop on a rune boundary. It is what an MCP
// tool answer needs: pi truncates the same way at the same 20KB.
func TruncateMiddle(content string, limits Limits) Truncation {
	_, maxBytes := limits.resolve()
	totalBytes := len(content)
	totalLines := len(splitLines(content))

	if totalBytes <= maxBytes {
		return Truncation{
			Content:     content,
			TotalLines:  totalLines,
			TotalBytes:  totalBytes,
			OutputLines: totalLines,
			OutputBytes: totalBytes,
			MaxBytes:    maxBytes,
		}
	}

	// The marker eats into the budget and reports a count that depends on the
	// split, so it is measured once from an estimate and then corrected. One
	// correction is enough in practice: the count changes by at most a digit.
	estimate := fmt.Sprintf(omittedFormat, totalBytes-maxBytes)
	budget := maxBytes - len(estimate)
	head := headBytes(content, budget/2)
	tail := tailBytes(content, budget-budget/2)
	marker := fmt.Sprintf(omittedFormat, totalBytes-len(head)-len(tail))

	output := head + marker + tail
	return Truncation{
		Content:     output,
		Truncated:   true,
		TruncatedBy: "middle",
		TotalLines:  totalLines,
		TotalBytes:  totalBytes,
		OutputLines: len(splitLines(output)),
		OutputBytes: len(output),
		MaxBytes:    maxBytes,
	}
}

// FormatSize renders a byte count the way pi does: 800B, 1.5KB, 2.0MB.
func FormatSize(bytes int) string {
	switch {
	case bytes < 1024:
		return fmt.Sprintf("%dB", bytes)
	case bytes < 1024*1024:
		return fmt.Sprintf("%.1fKB", float64(bytes)/1024)
	default:
		return fmt.Sprintf("%.1fMB", float64(bytes)/(1024*1024))
	}
}

// TruncateLine caps one line and marks the cut, used by grep so a single
// minified file cannot swallow the whole output budget.
func TruncateLine(line string, maxChars int) (string, bool) {
	if maxChars <= 0 {
		maxChars = GrepMaxLineLength
	}
	if utf8.RuneCountInString(line) <= maxChars {
		return line, false
	}
	runes := []rune(line)
	return string(runes[:maxChars]) + "... [truncated]", true
}

// splitLines counts lines the way pi does: a trailing newline does not start an
// extra empty line.
func splitLines(content string) []string {
	if content == "" {
		return nil
	}
	lines := strings.Split(content, "\n")
	if strings.HasSuffix(content, "\n") {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// tailBytes keeps the last maxBytes bytes of s without splitting a rune.
func tailBytes(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	start := len(s) - maxBytes
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return s[start:]
}

// headBytes keeps the first maxBytes bytes of s without splitting a rune. It is
// the mirror of tailBytes and only TruncateMiddle needs both.
func headBytes(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	end := maxBytes
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end]
}
