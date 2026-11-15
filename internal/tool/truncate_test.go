package tool_test

import (
	"strings"
	"testing"

	"pi-go/internal/tool"
)

func TestTruncateHeadKeepsSmallContent(t *testing.T) {
	content := "alpha\nbeta\n"
	got := tool.TruncateHead(content, tool.Limits{MaxLines: 10, MaxBytes: 1024})

	if got.Truncated {
		t.Fatalf("truncated: got true")
	}
	if got.Content != content {
		t.Fatalf("content: got %q", got.Content)
	}
	// A trailing newline does not start an extra empty line.
	if got.TotalLines != 2 || got.OutputLines != 2 {
		t.Fatalf("lines: got total=%d output=%d", got.TotalLines, got.OutputLines)
	}
}

func TestTruncateHeadByLines(t *testing.T) {
	content := strings.Join([]string{"1", "2", "3", "4", "5"}, "\n")
	got := tool.TruncateHead(content, tool.Limits{MaxLines: 3})

	if !got.Truncated || got.TruncatedBy != "lines" {
		t.Fatalf("truncation: got truncated=%v by=%q", got.Truncated, got.TruncatedBy)
	}
	if got.Content != "1\n2\n3" {
		t.Fatalf("content: got %q", got.Content)
	}
	if got.TotalLines != 5 || got.OutputLines != 3 {
		t.Fatalf("lines: got total=%d output=%d", got.TotalLines, got.OutputLines)
	}
}

func TestTruncateHeadByBytes(t *testing.T) {
	// Each line is 10 bytes; four lines plus separators cannot fit in 25 bytes.
	content := "0123456789\n0123456789\n0123456789\n0123456789"
	got := tool.TruncateHead(content, tool.Limits{MaxLines: 100, MaxBytes: 25})

	if !got.Truncated || got.TruncatedBy != "bytes" {
		t.Fatalf("truncation: got truncated=%v by=%q", got.Truncated, got.TruncatedBy)
	}
	if got.Content != "0123456789\n0123456789" {
		t.Fatalf("content: got %q", got.Content)
	}
	if got.OutputBytes != 21 {
		t.Fatalf("output bytes: got %d", got.OutputBytes)
	}
}

func TestTruncateHeadFirstLineTooLarge(t *testing.T) {
	content := strings.Repeat("x", 64) + "\nsecond"
	got := tool.TruncateHead(content, tool.Limits{MaxBytes: 32})

	if !got.FirstLineExceedsLimit {
		t.Fatal("firstLineExceedsLimit: got false")
	}
	if got.Content != "" {
		t.Fatalf("content: got %q", got.Content)
	}
	if got.TruncatedBy != "bytes" {
		t.Fatalf("truncatedBy: got %q", got.TruncatedBy)
	}
}

func TestTruncateTailByLines(t *testing.T) {
	content := strings.Join([]string{"1", "2", "3", "4", "5"}, "\n")
	got := tool.TruncateTail(content, tool.Limits{MaxLines: 2})

	if !got.Truncated || got.TruncatedBy != "lines" {
		t.Fatalf("truncation: got truncated=%v by=%q", got.Truncated, got.TruncatedBy)
	}
	if got.Content != "4\n5" {
		t.Fatalf("content: got %q", got.Content)
	}
}

func TestTruncateTailPartialLastLine(t *testing.T) {
	// One oversized line: only its end can be kept.
	content := strings.Repeat("a", 40) + strings.Repeat("b", 40)
	got := tool.TruncateTail(content, tool.Limits{MaxBytes: 16})

	if !got.LastLinePartial {
		t.Fatalf("lastLinePartial: got false (by=%q content=%q)", got.TruncatedBy, got.Content)
	}
	if got.Content != strings.Repeat("b", 16) {
		t.Fatalf("content: got %q", got.Content)
	}
}

func TestTruncateTailPartialKeepsRuneBoundaries(t *testing.T) {
	// "世" is three bytes; a 4-byte window must not start mid-rune.
	content := strings.Repeat("世", 10)
	got := tool.TruncateTail(content, tool.Limits{MaxBytes: 4})

	if !got.LastLinePartial {
		t.Fatal("lastLinePartial: got false")
	}
	if !strings.HasPrefix(got.Content, "世") || len(got.Content)%3 != 0 {
		t.Fatalf("content is not on rune boundaries: %q", got.Content)
	}
}

func TestTruncateBoundaryIsNotTruncation(t *testing.T) {
	content := "a\nb\nc"
	got := tool.TruncateHead(content, tool.Limits{MaxLines: 3, MaxBytes: len(content)})
	if got.Truncated {
		t.Fatalf("truncated: got true (%+v)", got)
	}
}

func TestFormatSize(t *testing.T) {
	tests := []struct {
		bytes int
		want  string
	}{
		{0, "0B"},
		{999, "999B"},
		{1024, "1.0KB"},
		{50 * 1024, "50.0KB"},
		{1536, "1.5KB"},
		{1024 * 1024, "1.0MB"},
	}
	for _, test := range tests {
		if got := tool.FormatSize(test.bytes); got != test.want {
			t.Fatalf("FormatSize(%d): got %q want %q", test.bytes, got, test.want)
		}
	}
}

func TestTruncateLine(t *testing.T) {
	short := "short line"
	if got, truncated := tool.TruncateLine(short, 100); truncated || got != short {
		t.Fatalf("short: got %q truncated=%v", got, truncated)
	}

	long := strings.Repeat("x", 20)
	got, truncated := tool.TruncateLine(long, 10)
	if !truncated {
		t.Fatal("long: got truncated=false")
	}
	if got != strings.Repeat("x", 10)+"... [truncated]" {
		t.Fatalf("long: got %q", got)
	}

	// Counted in runes, not bytes, so a multi-byte line is not cut mid-rune.
	multi := strings.Repeat("世", 5)
	got, _ = tool.TruncateLine(multi, 2)
	if got != "世世... [truncated]" {
		t.Fatalf("multi-byte: got %q", got)
	}
}
