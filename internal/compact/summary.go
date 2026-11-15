package compact

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"pi-go/internal/provider"
	"pi-go/internal/wire"
)

// The prompts are pi's, verbatim. The summary text is user-visible and becomes
// the only record of everything before the boundary, so its shape has to stay
// stable across implementations.
const summarizationSystemPrompt = `You are a context summarization assistant. Your task is to read a conversation between a user and an AI assistant, then produce a structured summary following the exact format specified.

Do NOT continue the conversation. Do NOT respond to any questions in the conversation. ONLY output the structured summary.`

const summarizationPrompt = `The messages above are a conversation to summarize. Create a structured context checkpoint summary that another LLM will use to continue the work.

Use this EXACT format:

## Goal
[What is the user trying to accomplish? Can be multiple items if the session covers different tasks.]

## Constraints & Preferences
- [Any constraints, preferences, or requirements mentioned by user]
- [Or "(none)" if none were mentioned]

## Progress
### Done
- [x] [Completed tasks/changes]

### In Progress
- [ ] [Current work]

### Blocked
- [Issues preventing progress, if any]

## Key Decisions
- **[Decision]**: [Brief rationale]

## Next Steps
1. [Ordered list of what should happen next]

## Critical Context
- [Any data, examples, or references needed to continue]
- [Or "(none)" if not applicable]

Keep each section concise. Preserve exact file paths, function names, and error messages.`

const updateSummarizationInstructions = `Update the existing structured summary with new information. RULES:
- PRESERVE all existing information from the previous summary
- ADD new progress, decisions, and context from the new messages
- UPDATE the Progress section: move items from "In Progress" to "Done" when completed
- UPDATE "Next Steps" based on what was accomplished
- PRESERVE exact file paths, function names, and error messages
- If something is no longer relevant, you may remove it

Use this EXACT format:

## Goal
[Preserve existing goals, add new ones if the task expanded]

## Constraints & Preferences
- [Preserve existing, add new ones discovered]

## Progress
### Done
- [x] [Include previously done items AND newly completed items]

### In Progress
- [ ] [Current work - update based on progress]

### Blocked
- [Current blockers - remove if resolved]

## Key Decisions
- **[Decision]**: [Brief rationale] (preserve all previous, add new)

## Next Steps
1. [Update based on current state]

## Critical Context
- [Preserve important context, add new if needed]

Keep each section concise. Preserve exact file paths, function names, and error messages.`

const updateSummarizationPrompt = `The messages above are NEW conversation messages to incorporate into the existing summary provided in <previous-summary> tags.

` + updateSummarizationInstructions

const turnPrefixSummarizationPrompt = `The messages above are earlier context from an ongoing conversation. Later messages are stored separately and do not need to be reconstructed.

Create a concise checkpoint of the user's request and the progress shown above. This checkpoint will be placed before the later messages so the conversation can continue with the necessary context.

## Original Request
[What did the user ask for?]

## Progress So Far
- [Key decisions and work completed in these messages]

## Context Needed to Continue
- [Information from these messages needed to understand the later work]

Only summarize information explicitly present above. Do not infer or recreate later messages.`

const (
	// toolResultMaxCharacters bounds one tool result inside a summary prompt.
	// Summarizing does not need the whole output, and an unbounded result would
	// make the summarization request larger than the context it summarizes.
	toolResultMaxCharacters = 2000
	// turnPrefixSeparator joins the two summaries a split turn produces, with
	// pi's heading so a reader can tell the halves apart.
	turnPrefixSeparator = "\n\n---\n\n**Turn Context (split turn):**\n\n"
	// noPriorHistory fills the history half when a previous checkpoint already
	// covers it. pi's wording, kept verbatim because it ends up in the session.
	noPriorHistory = "No prior history."
)

// Summarizer generates summaries with one provider. It holds no session state,
// so the same value can summarize a history range and a turn prefix.
type Summarizer struct {
	Provider provider.Provider
	Model    wire.Model
	// Options carries credentials, headers, and the thinking level. MaxTokens is
	// replaced per call.
	Options provider.Options
}

// SummarizeHistory condenses the discarded range. previous is the summary of an
// earlier compaction on the same branch, which switches the prompt to update
// mode: without it every compaction would drop what the previous one already
// condensed.
func (s *Summarizer) SummarizeHistory(ctx context.Context, messages []wire.Message, settings Settings, previous, instructions string) (string, wire.Usage, error) {
	prompt := historyPrompt(messages, previous, instructions)
	return s.summarize(ctx, prompt, s.historyBudget(settings.normalized()))
}

// SummarizeTurnPrefix condenses the part of the turn that ends up on the far side
// of the cut. It gets a smaller budget because it covers one turn, not a session.
func (s *Summarizer) SummarizeTurnPrefix(ctx context.Context, messages []wire.Message, settings Settings) (string, wire.Usage, error) {
	prompt := "# Conversation\n" + serializeConversation(messages) + "\n\n# Instructions\n" + turnPrefixSummarizationPrompt
	return s.summarize(ctx, prompt, s.prefixBudget(settings.normalized()))
}

// summarize sends one summarization request. Anything that must not become a
// checkpoint is a failure: an error or length stop, a tool call, or an empty
// body. A partial summary is worse than none, because it replaces the history it
// was supposed to preserve.
func (s *Summarizer) summarize(ctx context.Context, prompt string, maxTokens int) (string, wire.Usage, error) {
	stream, err := s.Provider.Stream(ctx, provider.Request{
		Model: s.Model,
		Messages: []wire.Message{
			wire.SystemMessage(summarizationSystemPrompt),
			wire.UserMessage(prompt),
		},
		Options: s.options(maxTokens),
	})
	if err != nil {
		return "", wire.Usage{}, fmt.Errorf("compact: summarize: %w", err)
	}
	message, _, err := provider.Collect(stream)
	if err != nil {
		return "", wire.Usage{}, fmt.Errorf("compact: summarize: %w", err)
	}
	if err := summarizationFailure(message); err != nil {
		return "", wire.Usage{}, err
	}
	text := strings.TrimSpace(message.Content.Text())
	if text == "" {
		return "", wire.Usage{}, fmt.Errorf("compact: summarize failed: the model returned no text")
	}
	usage := wire.Usage{}
	if message.Usage != nil {
		usage = *message.Usage
	}
	return text, usage, nil
}

func summarizationFailure(message wire.Message) error {
	switch message.StopReason {
	case wire.StopError:
		reason := message.ErrorMessage
		if reason == "" {
			reason = "unknown error"
		}
		return fmt.Errorf("compact: summarize failed: %s", reason)
	case wire.StopLength:
		return fmt.Errorf("compact: summarize failed: generation hit the token cap and the summary is incomplete")
	}
	for _, block := range message.Content.Items() {
		if block.Type == wire.ContentToolCall {
			return fmt.Errorf("compact: summarize failed: the model attempted to call a tool")
		}
	}
	return nil
}

// options builds per-call options. Thinking is forwarded only for a reasoning
// model: passing a level to a model without reasoning makes some adapters send
// parameters the endpoint rejects.
func (s *Summarizer) options(maxTokens int) provider.Options {
	options := s.Options
	options.MaxTokens = wire.Ptr(maxTokens)
	if !s.Model.Reasoning {
		options.ThinkingLevel = ""
	}
	return options
}

// historyPrompt wraps the conversation so the model reads it as material rather
// than a conversation to continue, and selects the initial or update prompt.
func historyPrompt(messages []wire.Message, previous, instructions string) string {
	prompt := "<conversation>\n" + serializeConversation(messages) + "\n</conversation>\n\n"
	if previous != "" {
		prompt += "<previous-summary>\n" + previous + "\n</previous-summary>\n\n"
	}
	base := summarizationPrompt
	if previous != "" {
		base = updateSummarizationPrompt
	}
	if instructions != "" {
		base += "\n\nAdditional focus: " + instructions
	}
	return prompt + base
}

// historyBudget takes a fraction of the reserve, capped by the model's own output
// limit: the summary has to fit in the room the next request leaves for it.
func (s *Summarizer) historyBudget(settings Settings) int {
	return capBudget(settings.ReserveTokens*8/10, s.Model.MaxTokens)
}

func (s *Summarizer) prefixBudget(settings Settings) int {
	return capBudget(settings.ReserveTokens/2, s.Model.MaxTokens)
}

func capBudget(budget, modelMax int) int {
	if budget <= 0 {
		budget = DefaultSettings.ReserveTokens / 2
	}
	if modelMax > 0 && modelMax < budget {
		return modelMax
	}
	return budget
}

// serializeConversation renders what happened: user text, assistant text, tool
// calls, and truncated tool results. Thinking is omitted on purpose. A provider
// often returns a summary of its reasoning, and replayed thinking is frequently
// not part of the model's live context, so including it can make the summary
// request larger than the context it summarizes.
func serializeConversation(messages []wire.Message) string {
	parts := make([]string, 0, len(messages))
	for _, message := range messages {
		switch message.Role {
		case wire.RoleUser:
			if text := message.Content.Text(); strings.TrimSpace(text) != "" {
				parts = append(parts, "[User]: "+text)
			}
		case wire.RoleAssistant:
			calls := make([]string, 0, 2)
			hasText := false
			for _, block := range message.Content.Items() {
				switch block.Type {
				case wire.ContentText:
					if block.Text != "" {
						hasText = true
					}
				case wire.ContentToolCall:
					calls = append(calls, block.Name+"("+formatArguments(block.Arguments)+")")
				}
			}
			if hasText {
				parts = append(parts, "[Assistant]: "+message.Content.Text())
			}
			if len(calls) > 0 {
				parts = append(parts, "[Assistant tool calls]: "+strings.Join(calls, "; "))
			}
		case wire.RoleToolResult:
			if text := message.Content.Text(); strings.TrimSpace(text) != "" {
				parts = append(parts, "[Tool result]: "+truncateForSummary(text, toolResultMaxCharacters))
			}
		}
	}
	return strings.Join(parts, "\n\n")
}

// formatArguments renders tool arguments as key=value pairs. Keys are sorted
// rather than kept in document order: the order carries no meaning to a reader,
// and sorting keeps the serialized prompt deterministic across runs.
func formatArguments(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var arguments map[string]json.RawMessage
	if err := json.Unmarshal(raw, &arguments); err != nil {
		return string(raw)
	}
	keys := make([]string, 0, len(arguments))
	for key := range arguments {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, key := range keys {
		pairs = append(pairs, key+"="+string(arguments[key]))
	}
	return strings.Join(pairs, ", ")
}

// truncateForSummary keeps the head of a body and reports how much was dropped,
// counting runes so the number matches what a reader sees.
func truncateForSummary(text string, maxCharacters int) string {
	runes := []rune(text)
	if len(runes) <= maxCharacters {
		return text
	}
	truncated := len(runes) - maxCharacters
	return fmt.Sprintf("%s\n\n[... %d more characters truncated]", string(runes[:maxCharacters]), truncated)
}
