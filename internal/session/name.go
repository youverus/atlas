package session

import (
	"strings"
	"unicode/utf8"
)

// maxDerivedName bounds a name taken from a message. A picker row is one line
// wide, and a name past this length is one nobody reads to the end.
const maxDerivedName = 48

// minDerivedClause is the shortest first clause worth keeping on its own.
// "好的，帮我跑一下测试" opens with a clause too short to name anything, so a cut
// that short is discarded and the whole line is used instead.
const minDerivedClause = 6

// clauseBreaks end the phrase a name can be taken from. The set is the
// punctuation people type between a topic and its detail, in both scripts.
const clauseBreaks = "，。；！？、：,;!?:\n"

// namePrefixes are the marks a message can open with: a heading, a quote, a
// list bullet, a code fence. They say how the line is drawn rather than what it
// says, so a name never repeats them.
const namePrefixes = "#>-*+`~ \t"

// DeriveName turns what the reader typed into a session name.
//
// It is local on purpose: no model call, so naming costs nothing, works with no
// network, and gives the command line and the window the same answer for the
// same message. The result is one line — the first line carrying text, its
// leading marks removed, its blanks folded, its first clause when that clause is
// long enough to name something, and an ellipsis when it is still too long.
//
// An empty return means the message had no text to name anything with, such as
// an image sent alone. The caller leaves the session unnamed.
func DeriveName(text string) string {
	for _, line := range strings.Split(text, "\n") {
		// A fence line holds the language, not the content: the useful line is
		// the one after it.
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			continue
		}
		trimmed := strings.Join(strings.Fields(strings.TrimLeft(line, namePrefixes)), " ")
		if trimmed == "" {
			continue
		}
		if clause, ok := firstClause(trimmed); ok {
			return clause
		}
		return truncateName(trimmed)
	}
	return ""
}

// firstClause reports whether the line has an opening phrase long enough to
// stand as a name, and returns it.
func firstClause(line string) (string, bool) {
	index := strings.IndexAny(line, clauseBreaks)
	if index <= 0 {
		return "", false
	}
	clause := strings.TrimSpace(line[:index])
	if utf8.RuneCountInString(clause) < minDerivedClause {
		return "", false
	}
	return truncateName(clause), true
}

// truncateName cuts text to the length a name may have.
func truncateName(text string) string {
	runes := []rune(text)
	if len(runes) <= maxDerivedName {
		return text
	}
	head := string(runes[:maxDerivedName])
	// Cutting inside a word leaves a fragment. Backing off to the last blank
	// avoids that, but only when the blank keeps most of the budget: text
	// written without blanks is cut where the budget ends.
	if space := strings.LastIndexByte(head, ' '); space > 0 && utf8.RuneCountInString(head[:space]) >= maxDerivedName/2 {
		head = head[:space]
	}
	return strings.TrimRight(head, " ") + "…"
}
