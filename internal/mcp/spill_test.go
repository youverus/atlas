package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pi-go/internal/tool"
)

// A result past the text limit keeps its middle and writes the whole thing to
// the set's spill directory, so "the full result was written to …" names a file
// the model can read back rather than one in a temp directory nobody cleans.
func TestLargeResultSpillsToTheSetsDirectory(t *testing.T) {
	spillDir := t.TempDir()
	spill := tool.NewSpill(func() string { return spillDir })

	// 30KB of head, then a marker only the middle cut would hide, then a tail.
	head := strings.Repeat("a", 15<<10)
	middle := "THE-MIDDLE-OF-THE-RESULT"
	tail := strings.Repeat("z", 15<<10)
	full := head + middle + tail

	result := renderResult(RemoteTool{Server: "docs", Name: "big"}, callResult{
		Content: []contentBlock{{Type: "text", Text: full}},
	}, spill)

	if len(result.Content) != 1 || result.Content[0].Type != "text" {
		t.Fatalf("content: %+v", result.Content)
	}
	text := result.Content[0].Text
	if strings.Contains(text, middle) {
		t.Fatal("the middle survived, so nothing was truncated")
	}

	marker := "the full result ("
	index := strings.Index(text, marker)
	if index < 0 {
		t.Fatalf("notice names no file:\n%s", text[max(0, len(text)-300):])
	}
	rest := text[index:]
	saved := rest[strings.Index(rest, "was written to ")+len("was written to "):]
	saved, _, _ = strings.Cut(saved, "]")

	if dir := filepath.Dir(saved); dir != spillDir {
		t.Fatalf("spill in %q, want %q", dir, spillDir)
	}
	written, err := os.ReadFile(saved)
	if err != nil {
		t.Fatalf("read spill: %v", err)
	}
	if string(written) != full {
		t.Fatalf("spill has %d bytes, want the whole result (%d)", len(written), len(full))
	}
}

// A set built by Open has no directory to write to, and it must still work: the
// notice is the point, and its absence would be a silently truncated answer.
func TestLargeResultWithoutASpillStillNamesAFile(t *testing.T) {
	full := strings.Repeat("x", 40<<10)

	result := renderResult(RemoteTool{Server: "docs", Name: "big"}, callResult{
		Content: []contentBlock{{Type: "text", Text: full}},
	}, nil)

	text := result.Content[0].Text
	if !strings.Contains(text, "was written to ") {
		t.Fatalf("no file was named:\n%s", text[max(0, len(text)-300):])
	}
}
