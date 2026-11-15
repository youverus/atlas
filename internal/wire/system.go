package wire

import "strings"

// NormalizeContext folds the system prompt and tool declarations into a leading
// system message, which is what providers receive. Unlike pi's normalizeContext
// this does not prepend a second system message when the transcript already
// starts with one, because the Go agent keeps its transcript in the same shape
// it sends.
func NormalizeContext(systemPrompt string, tools []Tool, messages []Message) []Message {
	if len(messages) > 0 && messages[0].Role == RoleSystem {
		out := make([]Message, len(messages))
		copy(out, messages)
		return out
	}
	if systemPrompt == "" && len(tools) == 0 {
		out := make([]Message, len(messages))
		copy(out, messages)
		return out
	}
	head := SystemMessage(systemPrompt)
	if len(tools) > 0 {
		head.ToolsAdded = tools
	}
	out := make([]Message, 0, len(messages)+1)
	out = append(out, head)
	out = append(out, messages...)
	return out
}

// CurrentTools replays tool declarations and removals from system messages in
// order, returning the tool set in effect at the end of the transcript.
func CurrentTools(messages []Message) []Tool {
	var order []string
	byName := map[string]Tool{}
	for _, message := range messages {
		if message.Role != RoleSystem {
			continue
		}
		for _, removed := range message.ToolsRemoved {
			delete(byName, removed.Name)
		}
		for _, added := range message.ToolsAdded {
			if _, exists := byName[added.Name]; !exists {
				order = append(order, added.Name)
			}
			byName[added.Name] = added
		}
	}
	tools := make([]Tool, 0, len(order))
	for _, name := range order {
		if tool, ok := byName[name]; ok {
			tools = append(tools, tool)
		}
	}
	return tools
}

// CurrentSystemMessage replays every system message into one leading system
// message holding the current prompt, sections, and tools. Later content is
// appended to the base prompt, sections patch by name, and tools resolve with
// CurrentTools. It reports false when the transcript has no system message and
// declares no tools, so an empty transcript stays empty.
func CurrentSystemMessage(messages []Message) (Message, bool) {
	var parts []string
	var timestamp int64
	seen := false
	for _, message := range messages {
		if message.Role != RoleSystem {
			continue
		}
		if !seen {
			timestamp = message.Timestamp
			seen = true
		}
		if text := message.Content.Text(); text != "" {
			parts = append(parts, text)
		}
	}
	tools := CurrentTools(messages)
	if !seen && len(tools) == 0 {
		return Message{}, false
	}
	out := Message{
		Role:      RoleSystem,
		Content:   NewTextList(strings.Join(parts, "\n\n")),
		Timestamp: timestamp,
	}
	if sections := ApplySystemSections(messages); len(sections) > 0 {
		out.Sections = make(map[string]*string, len(sections))
		for name, value := range sections {
			text := value
			out.Sections[name] = &text
		}
	}
	if len(tools) > 0 {
		out.ToolsAdded = tools
	}
	return out, true
}

// ApplySystemSections replays section patches in order. A nil value removes a
// section.
func ApplySystemSections(messages []Message) map[string]string {
	sections := map[string]string{}
	for _, message := range messages {
		if message.Role != RoleSystem {
			continue
		}
		for name, value := range message.Sections {
			if value == nil {
				delete(sections, name)
				continue
			}
			sections[name] = *value
		}
	}
	return sections
}
