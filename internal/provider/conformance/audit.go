package conformance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"pi-go/internal/wire"
)

// auditStream reports the stream promises obs broke, in the order they were
// checked. It is separate from Run so that the checks themselves can be tested
// against a deliberately broken stream: a harness that cannot fail is worse
// than no harness.
func auditStream(obs observation, suite Suite, tc Case) []string {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	if obs.recvErr != nil {
		add("Recv returned %v after the terminal event; want io.EOF", obs.recvErr)
	}
	if len(obs.events) == 0 {
		return append(problems, "the stream emitted no events")
	}

	first := obs.events[0]
	if first.Type != wire.EventStart {
		add("the first event is %q; want %q", first.Type, wire.EventStart)
	}
	switch {
	case first.Partial == nil:
		add("the start event carries no partial message")
	case first.Partial.Role != wire.RoleAssistant:
		add("the start partial has role %q; want %q", first.Partial.Role, wire.RoleAssistant)
	}

	// Exactly one terminal event, last of all: a second one would mean the
	// caller sees two endings, and an event after it would never be delivered
	// by a caller that stops at the terminal event.
	terminals, terminalAt := 0, -1
	for i, event := range obs.events {
		if event.Type == wire.EventDone || event.Type == wire.EventError {
			terminals++
			terminalAt = i
		}
	}
	switch {
	case terminals == 0:
		add("the stream never emitted a terminal event")
	case terminals > 1:
		add("the stream emitted %d terminal events; want exactly 1", terminals)
	case terminalAt != len(obs.events)-1:
		add("event %q follows the terminal event; the terminal event must be last", obs.events[terminalAt+1].Type)
	}

	for _, retained := range obs.partials {
		if now := encodeMessage(*retained.ptr); now != retained.when {
			add("the partial delivered by event %d changed afterwards: %s became %s", retained.at, retained.when, now)
			break
		}
	}

	if obs.final == nil {
		if terminals > 0 {
			add("the terminal event carries no message")
		}
		return problems
	}
	final := *obs.final

	if final.Role != wire.RoleAssistant {
		add("the terminal message has role %q; want %q", final.Role, wire.RoleAssistant)
	}
	if final.API != obs.api {
		add("the terminal message says api %q; the adapter reports %q", final.API, obs.api)
	}
	if final.Model != suite.Model.ID {
		add("the terminal message says model %q; the request asked for %q", final.Model, suite.Model.ID)
	}
	if suite.Model.Provider != "" && final.Provider != suite.Model.Provider {
		add("the terminal message says provider %q; want %q", final.Provider, suite.Model.Provider)
	}
	if final.StopReason == wire.StopPending {
		add("the terminal message is still pending; a terminal message must carry a settled stop reason")
	}

	text := joinedText(final.Content, wire.ContentText)
	thinking := joinedText(final.Content, wire.ContentThinking)
	if text != tc.Want.Text {
		add("text is %q; want %q", text, tc.Want.Text)
	}
	if thinking != tc.Want.Thinking {
		add("thinking is %q; want %q", thinking, tc.Want.Thinking)
	}
	if tc.Want.Signature != "" {
		if signature := firstSignature(final.Content); signature != tc.Want.Signature {
			add("the first thinking block has signature %q; want %q", signature, tc.Want.Signature)
		}
	}
	auditToolCalls(add, final.ToolCalls(), tc.Want.ToolCalls)

	wantStop := tc.Want.Stop
	wantType := wire.EventDone
	if wantStop == "" {
		wantStop = wire.StopStop
	}
	if tc.Want.Error != "" {
		wantType = wire.EventError
		wantStop = tc.Want.ErrorStop
		if wantStop == "" {
			wantStop = wire.StopError
		}
	}
	if last := obs.events[len(obs.events)-1].Type; last != wantType {
		add("the terminal event is %q; want %q", last, wantType)
	}
	if final.StopReason != wantStop {
		add("the stop reason is %q; want %q", final.StopReason, wantStop)
	}
	if tc.Want.Error != "" {
		if !strings.Contains(final.ErrorMessage, tc.Want.Error) {
			add("the error message is %q; it must mention %q", final.ErrorMessage, tc.Want.Error)
		}
	} else if final.ErrorMessage != "" {
		add("a message that completed carries errorMessage %q", final.ErrorMessage)
	}

	if tc.Want.Usage != nil {
		if final.Usage == nil {
			add("the message carries no usage; want %+v", *tc.Want.Usage)
		} else if !reflect.DeepEqual(*final.Usage, *tc.Want.Usage) {
			add("usage is %+v; want %+v", *final.Usage, *tc.Want.Usage)
		}
	}

	auditDeltas(add, obs.events, text, thinking)
	auditIndices(add, obs.events, final.Content.Len())
	return problems
}

func auditToolCalls(add func(string, ...any), got []wire.Content, want []ToolCall) {
	if len(got) != len(want) {
		add("the message carries %d tool calls; want %d", len(got), len(want))
		return
	}
	for i, want := range want {
		call := got[i]
		if call.ID != want.ID {
			add("tool call %d has id %q; want %q", i, call.ID, want.ID)
		}
		if call.Name != want.Name {
			add("tool call %d has name %q; want %q", i, call.Name, want.Name)
		}
		if !sameJSON(call.Arguments, want.Arguments) {
			add("tool call %d (%s) has arguments %s; want %s", i, want.Name, string(call.Arguments), want.Arguments)
		}
	}
}

// auditDeltas requires the pieces the stream announced to add up to the blocks
// it finished with: a dropped delta and a block nobody could have built from the
// stream are different bugs, and only comparing the two finds the first.
//
// A block with no deltas at all is left alone: a redacted reasoning block
// arrives whole, by definition.
func auditDeltas(add func(string, ...any), events []wire.AssistantEvent, text, thinking string) {
	var textDeltas, thinkingDeltas strings.Builder
	for _, event := range events {
		switch event.Type {
		case wire.EventTextDelta:
			textDeltas.WriteString(event.Delta)
		case wire.EventThinkingDelta:
			thinkingDeltas.WriteString(event.Delta)
		}
	}
	if textDeltas.Len() > 0 && textDeltas.String() != text {
		add("text deltas total %q but the finished message says %q", textDeltas.String(), text)
	}
	if thinkingDeltas.Len() > 0 && thinkingDeltas.String() != thinking {
		add("thinking deltas total %q but the finished message says %q", thinkingDeltas.String(), thinking)
	}
}

// auditIndices requires blocks to be numbered from zero in the order they open,
// and requires every later event to name a block that was opened. Interleaving
// is legitimate — two calls can stream at once — so order of use is not checked;
// what is checked is that no event points at a block nobody started, and that
// the message ends up holding exactly the blocks the stream opened.
func auditIndices(add func(string, ...any), events []wire.AssistantEvent, blocks int) {
	started := map[int]bool{}
	opened := 0
	for i, event := range events {
		if event.ContentIndex == nil {
			continue
		}
		index := *event.ContentIndex
		switch event.Type {
		case wire.EventTextStart, wire.EventThinkingStart, wire.EventToolCallStart:
			if index != opened {
				add("block %d opens at content index %d; blocks are numbered from zero in the order they open", opened, index)
			}
			started[index] = true
			opened++
		case wire.EventTextDelta, wire.EventThinkingDelta, wire.EventToolCallDelta,
			wire.EventTextEnd, wire.EventThinkingEnd, wire.EventToolCallEnd:
			if !started[index] {
				add("event %d (%s) refers to content index %d before any start event opened it", i, event.Type, index)
			}
		}
	}
	if blocks != opened {
		add("the message holds %d blocks but the stream opened %d", blocks, opened)
	}
}

// auditRequest reports what the adapter put on the wire. It checks meaning, not
// layout: a protocol may nest these values anywhere it likes, and pinning the
// exact shape belongs in the adapter's own fixtures.
func auditRequest(seen []captured, suite Suite, tc Case) []string {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	if len(seen) != 1 {
		return append(problems, fmt.Sprintf("the adapter made %d requests for one stream; want 1", len(seen)))
	}
	request := seen[0]
	if request.method != "POST" {
		add("the request method is %s; want POST", request.method)
	}
	if request.path != suite.Path {
		add("the request path is %s; want %s", request.path, suite.Path)
	}
	if got := request.header.Get(suite.HeaderKey); !strings.Contains(got, apiKey) {
		add("header %s is %q; it must carry the API key", suite.HeaderKey, got)
	}

	var body any
	if err := json.Unmarshal(request.body, &body); err != nil {
		return append(problems, fmt.Sprintf("the request body is not JSON: %v", err))
	}
	values := leafStrings(body)
	require := func(label, wanted string) {
		if wanted == "" {
			return
		}
		for _, value := range values {
			if value == wanted {
				return
			}
		}
		add("the request body never mentions the %s %q", label, wanted)
	}
	if !suite.ModelInPath {
		require("model", suite.Model.ID)
	}
	require("prompt", promptOf(tc))
	for _, tool := range toolsOf(tc) {
		require("tool", tool.Name)
	}

	if !suite.StreamInPath {
		if stream, ok := valueOf(body, "stream").(bool); !ok || !stream {
			add("the request does not ask for a stream: %v", valueOf(body, "stream"))
		}
	}
	return problems
}

// leafStrings flattens every string value in a decoded JSON document.
func leafStrings(value any) []string {
	var out []string
	var walk func(any)
	walk = func(node any) {
		switch node := node.(type) {
		case string:
			out = append(out, node)
		case []any:
			for _, item := range node {
				walk(item)
			}
		case map[string]any:
			for _, item := range node {
				walk(item)
			}
		}
	}
	walk(value)
	return out
}

// valueOf finds the first value stored under key, anywhere in the document.
func valueOf(value any, key string) any {
	switch node := value.(type) {
	case map[string]any:
		if found, ok := node[key]; ok {
			return found
		}
		for _, item := range node {
			if found := valueOf(item, key); found != nil {
				return found
			}
		}
	case []any:
		for _, item := range node {
			if found := valueOf(item, key); found != nil {
				return found
			}
		}
	}
	return nil
}

func promptOf(tc Case) string {
	if tc.Prompt == "" {
		return defaultPrompt
	}
	return tc.Prompt
}

func toolsOf(tc Case) []wire.Tool {
	if tc.Tools == nil {
		return defaultTools()
	}
	return tc.Tools
}

// joinedText concatenates the blocks of one type in content order.
func joinedText(content wire.ContentList, blockType string) string {
	var out strings.Builder
	for _, block := range content.Items() {
		if block.Type != blockType {
			continue
		}
		switch blockType {
		case wire.ContentText:
			out.WriteString(block.Text)
		case wire.ContentThinking:
			out.WriteString(block.Thinking)
		}
	}
	return out.String()
}

func firstSignature(content wire.ContentList) string {
	for _, block := range content.Items() {
		if block.Type == wire.ContentThinking {
			return block.ThinkingSignature
		}
	}
	return ""
}

// sameJSON compares two JSON documents by meaning. Arguments travel as bytes but
// a server may reformat them, so the check is on the decoded value; an empty
// expectation accepts the empty object the frame defaults to.
func sameJSON(got json.RawMessage, want string) bool {
	if want == "" {
		return len(bytes.TrimSpace(got)) == 0 || bytes.Equal(bytes.TrimSpace(got), []byte("{}"))
	}
	var left, right any
	if err := json.Unmarshal(got, &left); err != nil {
		return false
	}
	if err := json.Unmarshal([]byte(want), &right); err != nil {
		return false
	}
	return reflect.DeepEqual(left, right)
}
