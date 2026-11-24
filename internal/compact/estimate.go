// Package compact compacts a session's context: it decides where to cut the
// transcript, summarizes the discarded range with one or two model calls, and
// returns the entry boundary the caller records as a compaction entry.
//
// The package never writes the session. Persisting is the caller's job because
// only the caller knows the branch to append to and how to render the boundary.
package compact

import (
	"encoding/json"
	"unicode/utf8"

	"pi-go/internal/session"
	"pi-go/internal/wire"
)

// Settings mirrors pi's CompactionSettings. Zero values are not useful defaults:
// use DefaultSettings and override the fields a caller wants to change.
type Settings struct {
	Enabled bool
	// ReserveTokens is headroom kept free for the next response.
	ReserveTokens int
	// KeepRecentTokens is the approximate size of the range that survives a
	// compaction, measured with EstimateTokens.
	KeepRecentTokens int
}

// DefaultSettings matches pi's DEFAULT_COMPACTION_SETTINGS.
var DefaultSettings = Settings{Enabled: true, ReserveTokens: 16384, KeepRecentTokens: 20000}

const (
	// charsPerToken is the divisor pi uses. A real tokenizer would be an
	// external dependency, which the single-binary goal rules out; this ratio
	// only has to be good enough to trigger early rather than late.
	charsPerToken = 4
	// imageCharacters is what one image costs regardless of its payload: pi
	// charges a flat rate because the byte length of a base64 payload says
	// nothing about what the model spends on the image.
	imageCharacters = 4800
)

// ContextUsage is what a threshold check needs. LastUsageIndex is -1 when no
// assistant message carried usable usage, which means Tokens is a pure estimate.
type ContextUsage struct {
	Tokens         int
	UsageTokens    int
	TrailingTokens int
	LastUsageIndex int
}

// ContextTokens reports the context size a usage record describes. Providers
// differ in whether they fill TotalTokens, so it falls back to the components.
func ContextTokens(usage wire.Usage) int {
	if usage.TotalTokens > 0 {
		return usage.TotalTokens
	}
	return usage.Input + usage.Output + usage.CacheRead + usage.CacheWrite
}

// EstimateTokens estimates what one message costs in the model's context. It
// deliberately overestimates: triggering a compaction too early costs one
// summary, triggering it too late costs a failed request.
func EstimateTokens(message wire.Message) int {
	chars := 0
	switch message.Role {
	case wire.RoleAssistant:
		for _, block := range message.Content.Items() {
			switch block.Type {
			case wire.ContentText:
				chars += textCharacters(block.Text)
			case wire.ContentThinking:
				chars += textCharacters(block.Thinking)
			case wire.ContentToolCall:
				chars += textCharacters(block.Name) + len(block.Arguments)
			}
		}
	default:
		chars = contentCharacters(message.Content)
	}
	if message.Role == wire.RoleSystem {
		for _, section := range message.Sections {
			if section != nil {
				chars += textCharacters(*section)
			}
		}
		if len(message.ToolsAdded) > 0 {
			chars += toolsCharacters(message.ToolsAdded)
		}
	}
	return tokensFor(chars)
}

// EstimateTextTokens estimates token count for plain text using the same
// character-to-token ratio as EstimateTokens.
func EstimateTextTokens(text string) int {
	if text == "" {
		return 0
	}
	return tokensFor(textCharacters(text))
}

// EstimateToolsTokens estimates token count for serialized tool declarations.
func EstimateToolsTokens(tools []wire.Tool) int {
	if len(tools) == 0 {
		return 0
	}
	return tokensFor(toolsCharacters(tools))
}

func toolsCharacters(tools []wire.Tool) int {
	// A tool declaration costs roughly its JSON size, and re-encoding it
	// is cheap compared with the request it will be part of.
	encoded, err := json.Marshal(tools)
	if err != nil {
		return 0
	}
	return len(encoded)
}

// EstimateContextTokens anchors the estimate on the last assistant usage record
// and only estimates the messages after it. That is much closer to what the
// provider will bill than re-estimating a whole transcript.
func EstimateContextTokens(messages []wire.Message) ContextUsage {
	index := lastUsageIndex(messages)
	if index < 0 {
		estimated := EstimateMessages(messages)
		return ContextUsage{Tokens: estimated, TrailingTokens: estimated, LastUsageIndex: -1}
	}
	usageTokens := ContextTokens(*messages[index].Usage)
	trailing := EstimateMessages(messages[index+1:])
	return ContextUsage{
		Tokens:         usageTokens + trailing,
		UsageTokens:    usageTokens,
		TrailingTokens: trailing,
		LastUsageIndex: index,
	}
}

// EstimateMessages sums the per-message estimates and trusts no usage record.
// It is what a caller reports as the size of a context it just rebuilt: after a
// compaction the old usage describes a context that no longer exists.
func EstimateMessages(messages []wire.Message) int {
	tokens := 0
	for _, message := range messages {
		tokens += EstimateTokens(message)
	}
	return tokens
}

// EstimateProjection estimates the active context and refuses to trust usage
// captured before a later compaction or context edit. Kept pre-compaction
// messages carry usage that describes the old, larger context; trusting it would
// trigger another compaction immediately after one just finished.
//
// The branch is needed because the projection alone cannot tell a message that
// survived a compaction from one written after it: both follow the compaction
// entry. Position in the branch is what separates them.
func EstimateProjection(projection *session.Projection, path []session.Entry) ContextUsage {
	estimate := EstimateContextTokens(projection.Messages)
	if estimate.LastUsageIndex >= 0 {
		usageEntry := entryForMessage(projection.Entries, estimate.LastUsageIndex)
		if usageEntry >= 0 {
			usageAt := indexOfEntry(path, projection.Entries[usageEntry].Source.ID)
			if usageAt > lastInvalidating(path) {
				return estimate
			}
		}
	}
	// Re-estimate the whole projection without the usage anchor.
	tokens := EstimateMessages(projection.Messages)
	return ContextUsage{Tokens: tokens, TrailingTokens: tokens, LastUsageIndex: -1}
}

// indexOfEntry reports where an entry sits on the branch, or -1.
func indexOfEntry(path []session.Entry, id string) int {
	for i, entry := range path {
		if entry.ID == id {
			return i
		}
	}
	return -1
}

// lastInvalidating reports the position of the newest entry that rewrites the
// context, or -1. Usage recorded before it describes a context that is gone.
func lastInvalidating(path []session.Entry) int {
	for i := len(path) - 1; i >= 0; i-- {
		switch path[i].Type {
		case session.TypeCompaction, session.TypeContextEdit:
			return i
		}
	}
	return -1
}

// ShouldCompact reports whether the context is close enough to the window that
// another turn would risk overflowing. A window of zero means the caller did not
// declare one, and an unknown window must not trigger compaction: the comparison
// would be true on every turn.
func ShouldCompact(contextTokens, contextWindow int, settings Settings) bool {
	if !settings.Enabled || contextWindow <= 0 {
		return false
	}
	return contextTokens > contextWindow-settings.ReserveTokens
}

// lastUsageIndex finds the newest assistant usage worth trusting. Aborted and
// failed messages report usage for a request that never produced a context.
func lastUsageIndex(messages []wire.Message) int {
	for i := len(messages) - 1; i >= 0; i-- {
		message := messages[i]
		if message.Role != wire.RoleAssistant || message.Usage == nil {
			continue
		}
		switch message.StopReason {
		case wire.StopAborted, wire.StopError:
			continue
		}
		if ContextTokens(*message.Usage) > 0 {
			return i
		}
	}
	return -1
}

// entryForMessage maps a projection message index back to the entry that
// produced it, or -1 when the index is out of range.
func entryForMessage(entries []session.ProjectedEntry, index int) int {
	remaining := index
	for i, entry := range entries {
		if remaining < len(entry.Messages) {
			return i
		}
		remaining -= len(entry.Messages)
	}
	return -1
}

func contentCharacters(content wire.ContentList) int {
	if content.IsPlain() {
		return textCharacters(content.PlainText())
	}
	chars := 0
	for _, block := range content.Items() {
		switch block.Type {
		case wire.ContentText:
			chars += textCharacters(block.Text)
		case wire.ContentImage:
			chars += imageCharacters
		}
	}
	return chars
}

// textCharacters counts ASCII as one character and everything else as the bytes
// it encodes to. pi divides the JavaScript string length by four, which
// undercounts CJK text by roughly a factor of four; a CJK glyph costs about one
// token and three UTF-8 bytes, so byte counting lands close to the truth while
// leaving English ratios identical to pi's.
func textCharacters(text string) int {
	chars := 0
	for _, r := range text {
		if r < utf8.RuneSelf {
			chars++
			continue
		}
		chars += utf8.RuneLen(r)
	}
	return chars
}

func tokensFor(chars int) int {
	return (chars + charsPerToken - 1) / charsPerToken
}
