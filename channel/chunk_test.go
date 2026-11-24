package channel

import (
	"strings"
	"testing"
)

func TestChunkShortStaysOne(t *testing.T) {
	got := Chunk("你好，世界", DefaultChunkRunes)
	if len(got) != 1 || got[0] != "你好，世界" {
		t.Fatalf("short text changed: %q", got)
	}
}

func TestChunkRespectsMax(t *testing.T) {
	text := strings.Repeat("字", DefaultChunkRunes*3+7)
	got := Chunk(text, DefaultChunkRunes)
	total := 0
	for i, piece := range got {
		runes := len([]rune(piece))
		if runes > DefaultChunkRunes {
			t.Fatalf("chunk %d is %d runes, over the %d ceiling", i, runes, DefaultChunkRunes)
		}
		if runes == 0 {
			t.Fatalf("chunk %d is empty", i)
		}
		total += runes
	}
	if total != len([]rune(text)) {
		t.Fatalf("chunks lost runes: got %d, want %d", total, len([]rune(text)))
	}
	if len(got) != 4 {
		t.Fatalf("want 4 chunks, got %d", len(got))
	}
}

func TestChunkPrefersParagraphBreaks(t *testing.T) {
	para := strings.Repeat("a", 60)
	text := para + "\n\n" + para + "\n\n" + para
	got := Chunk(text, 100)
	want := []string{para + "\n\n", para + "\n\n", para}
	if len(got) != len(want) {
		t.Fatalf("want %d chunks, got %d", len(want), len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("chunk %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestChunkPrefersLineBreakOverMidWord(t *testing.T) {
	first := strings.Repeat("字", 30)
	second := strings.Repeat("字", 30)
	got := Chunk(first+"\n"+second, 50)
	if len(got) != 2 || got[0] != first+"\n" {
		t.Fatalf("want the cut after the line break, got %q", got)
	}
}

func TestChunkHardCutWhenNoBreak(t *testing.T) {
	text := strings.Repeat("字", 300)
	got := Chunk(text, 100)
	if len(got) != 3 || got[0] != strings.Repeat("字", 100) {
		t.Fatalf("hard cut wrong: %d chunks", len(got))
	}
}

func TestChunkZeroMaxFallsBack(t *testing.T) {
	got := Chunk("abc", 0)
	if len(got) != 1 {
		t.Fatalf("zero max must fall back, got %d chunks", len(got))
	}
}
