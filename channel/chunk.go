package channel

import (
	"strings"
)

// DefaultChunkRunes is the per-message ceiling the Feishu adapter declares.
// It is not the platform's hard limit: it is the point where one bubble stops
// being readable, the same number hermes picked for the same reason.
const DefaultChunkRunes = 8000

// Chunk splits text into pieces of at most max runes, preferring a paragraph
// or line break over a mid-word cut. A reader who gets five chunks should
// never see a word start in one and end in another.
func Chunk(text string, max int) []string {
	if max <= 0 {
		max = DefaultChunkRunes
	}
	runes := []rune(text)
	if len(runes) <= max {
		return []string{text}
	}

	var out []string
	for len(runes) > max {
		window := runes[:max]
		cut := breakAt(window)
		if cut <= 0 {
			cut = max // no break in the window: a hard cut beats an infinite loop
		}
		piece := strings.TrimRight(string(runes[:cut]), " \t")
		out = append(out, piece)
		runes = runes[cut:]
	}
	if len(runes) > 0 {
		out = append(out, string(runes))
	}
	return out
}

// breakAt returns the cut index inside window that ends a whole line or, one
// step coarser, a whole sentence-ish gap. Paragraph breaks win over line
// breaks; the index cuts AFTER the break character so the next chunk starts
// on its own line.
func breakAt(window []rune) int {
	for i := len(window) - 1; i > 0; i-- {
		if window[i] == '\n' && i+1 < len(window) && window[i+1] == '\n' {
			// Cut past BOTH newlines: the next chunk starts on its own line,
			// not with the tail of the break it was split on.
			return i + 2
		}
	}
	for i := len(window) - 1; i > 0; i-- {
		if window[i] == '\n' {
			return i + 1
		}
	}
	for i := len(window) - 1; i > 0; i-- {
		if window[i] == ' ' {
			return i
		}
	}
	return 0
}
