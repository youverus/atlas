package provider

import (
	"regexp"

	"pi-go/internal/wire"
)

// ClassifyText names a failure from the words a vendor used.
//
// The alternative is a structured field per vendor, and the four APIs do not
// offer one: openai's `context_length_exceeded` says nothing about the other
// three, anthropic reports "prompt is too long" as prose, Gemini puts
// INVALID_ARGUMENT on the enclosing error, and a gateway in front of any of them
// may answer with plain text. Text is the only shared surface.
//
// Guessing wrong is cheap but not free: a false positive compacts the branch and
// retries a turn that would never have succeeded, which is why the exclusion
// list comes first. The patterns are pi's, trimmed to the four dialects this
// kernel speaks plus the generic fallbacks its adapters can produce.
func ClassifyText(text string) string {
	if text == "" {
		return ""
	}
	for _, excluded := range notOverflow {
		if excluded.MatchString(text) {
			return ""
		}
	}
	for _, pattern := range overflow {
		if pattern.MatchString(text) {
			return wire.ErrorCodeContextOverflow
		}
	}
	return ""
}

// notOverflow wins over every overflow pattern below. Rate limits and
// cancellations borrow the same words: Bedrock says "Too many tokens, please
// wait before trying again" for a throttle, and every transport reports a
// timeout as something that "exceeded".
var notOverflow = []*regexp.Regexp{
	regexp.MustCompile(`(?i)rate limit`),
	regexp.MustCompile(`(?i)too many requests`),
	regexp.MustCompile(`(?i)throttl`),
	regexp.MustCompile(`(?i)deadline exceeded`),
	regexp.MustCompile(`(?i)context canceled`),
}

// overflow lists the ways a request can be reported as too large. Each entry
// names the dialect whose words it matches; a pattern that stopped matching
// anything is dead weight, so they are kept separate rather than merged into one
// clever expression.
var overflow = []*regexp.Regexp{
	// anthropic: "prompt is too long: 213462 tokens > 200000 maximum", and
	// z.ai borrows the same wording.
	regexp.MustCompile(`(?i)prompt (?:is )?too long`),
	// z.ai's CN endpoint rewords the same failure.
	regexp.MustCompile(`(?i)prompt exceeds max length`),
	// anthropic 413: {"error":{"type":"request_too_large", ...}}. The type field
	// does not survive into the formatted message — adapters keep only the
	// human-readable half — so the sentence is matched as well.
	regexp.MustCompile(`(?i)request_too_large`),
	regexp.MustCompile(`(?i)exceeds the maximum size`),
	// openai, both APIs: "Your input exceeds the context window of this model".
	regexp.MustCompile(`(?i)exceeds the context window`),
	// openai-compatible proxies: "exceeds the model's maximum context length of
	// 131072 tokens".
	regexp.MustCompile(`(?i)maximum context length`),
	// openai's own code for the same failure, when a gateway forwards it as text.
	regexp.MustCompile(`(?i)context[_ ]length[_ ]exceeded`),
	// google: "The input token count (1196265) exceeds the maximum number of
	// tokens allowed (1048575)".
	regexp.MustCompile(`(?i)input token count.*exceeds the maximum`),
	// openrouter and poolside: "Input length X exceeds the maximum allowed input
	// length of Y tokens."
	regexp.MustCompile(`(?i)exceeds (?:the )?maximum allowed input length`),
	// llama.cpp and DS4 style local servers.
	regexp.MustCompile(`(?i)exceeds the available context size`),
	regexp.MustCompile(`(?i)configured context size`),
	// groq: "Please reduce the length of the messages or completion".
	regexp.MustCompile(`(?i)reduce the length of the messages`),
	// Generic fallbacks, last because they are the easiest to match by accident.
	// The exclusion list above is what keeps them honest.
	regexp.MustCompile(`(?i)too many tokens`),
	regexp.MustCompile(`(?i)token limit exceeded`),
	regexp.MustCompile(`(?i)model_context_window_exceeded`),
}

// IsContextOverflow reports whether a message records an overflow.
func IsContextOverflow(message wire.Message) bool {
	return message.StopReason == wire.StopError && message.ErrorCode == wire.ErrorCodeContextOverflow
}
