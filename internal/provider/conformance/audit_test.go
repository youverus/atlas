package conformance

// These are the tests of the harness. Every check here is planted with the
// defect it exists to catch, because a conformance suite that cannot fail is
// worse than none: it would certify an adapter nobody looked at.

import (
	"net/http"
	"strings"
	"testing"

	"pi-go/internal/provider"
	"pi-go/internal/wire"
)

const (
	probeAPI  = "probe-api"
	probePath = "/probe"
)

func probeSuite() Suite {
	return Suite{
		Model:     wire.Model{ID: "probe-model", API: probeAPI, Provider: "probe-provider"},
		Path:      probePath,
		HeaderKey: "Authorization",
	}
}

func probeCase() Case {
	return Case{
		Name: "probe",
		Want: Want{
			Text:      "hello",
			Thinking:  "thinking",
			Signature: "reasoning_content",
			ToolCalls: []ToolCall{{ID: "call_a", Name: "read", Arguments: `{"path":"a.txt"}`}},
			Stop:      wire.StopToolUse,
		},
	}
}

// goodStream is what a correct adapter emits for one text block, one reasoning
// block and one finished call.
func goodStream(t *testing.T) []wire.AssistantEvent {
	t.Helper()
	frame := provider.NewFrame(probeSuite().Model)
	events := []wire.AssistantEvent{frame.Start()}
	events = append(events,
		frame.TextStart(0),
		frame.TextDelta(0, "hello"),
		frame.TextEnd(0),
		frame.ThinkingStart(1),
		frame.ThinkingDelta(1, "thinking"),
	)
	frame.ThinkingSignature(1, "reasoning_content")
	events = append(events,
		frame.ThinkingEnd(1),
		frame.ToolCallStart(2, "call_a", "read"),
		frame.ToolCallDelta(2, `{"path":`),
		frame.ToolCallDelta(2, `"a.txt"}`),
	)
	end, err := frame.ToolCallEnd(2)
	if err != nil {
		t.Fatalf("frame tool call end: %v", err)
	}
	return append(events, end, frame.Done(wire.StopToolUse))
}

func observationOf(events []wire.AssistantEvent) observation {
	obs := observation{api: probeAPI}
	for _, event := range events {
		obs.keep(event)
	}
	return obs
}

func TestAuditAcceptsAWellFormedStream(t *testing.T) {
	problems := auditStream(observationOf(goodStream(t)), probeSuite(), probeCase())
	if len(problems) > 0 {
		t.Fatalf("a correct stream was rejected:\n  %s", strings.Join(problems, "\n  "))
	}
}

func TestAuditRejectsABrokenStream(t *testing.T) {
	cases := []struct {
		name string
		// want is the phrase the report must contain, so a check that fails for
		// an unrelated reason is not mistaken for the right one.
		want string
		// build returns the events and the expectations to check them against.
		build func(t *testing.T) ([]wire.AssistantEvent, Case)
	}{
		{
			name: "no opening event",
			want: `the first event is "text_start"`,
			build: func(t *testing.T) ([]wire.AssistantEvent, Case) {
				return goodStream(t)[1:], probeCase()
			},
		},
		{
			name: "the opening event carries no partial",
			want: "the start event carries no partial message",
			build: func(t *testing.T) ([]wire.AssistantEvent, Case) {
				events := goodStream(t)
				events[0] = wire.AssistantEvent{Type: wire.EventStart}
				return events, probeCase()
			},
		},
		{
			name: "two terminal events",
			want: "emitted 2 terminal events",
			build: func(t *testing.T) ([]wire.AssistantEvent, Case) {
				events := goodStream(t)
				frame := provider.NewFrame(probeSuite().Model)
				return append(events, frame.Done(wire.StopStop)), probeCase()
			},
		},
		{
			name: "an event after the terminal event",
			want: "follows the terminal event",
			build: func(t *testing.T) ([]wire.AssistantEvent, Case) {
				events := goodStream(t)
				return append(events, wire.AssistantEvent{Type: wire.EventTextDelta}), probeCase()
			},
		},
		{
			name: "no terminal event at all",
			want: "never emitted a terminal event",
			build: func(t *testing.T) ([]wire.AssistantEvent, Case) {
				events := goodStream(t)
				return events[:len(events)-1], probeCase()
			},
		},
		{
			name: "a dropped text delta",
			want: "text deltas total",
			build: func(t *testing.T) ([]wire.AssistantEvent, Case) {
				events := goodStream(t)
				for i := range events {
					if events[i].Type == wire.EventTextDelta {
						events[i].Delta = "hell"
						break
					}
				}
				return events, probeCase()
			},
		},
		{
			name: "arguments that differ by value",
			want: "has arguments",
			build: func(t *testing.T) ([]wire.AssistantEvent, Case) {
				tc := probeCase()
				tc.Want.ToolCalls[0].Arguments = `{"path":"b.txt"}`
				return goodStream(t), tc
			},
		},
		{
			name: "a missing tool call",
			want: "carries 1 tool calls; want 0",
			build: func(t *testing.T) ([]wire.AssistantEvent, Case) {
				tc := probeCase()
				tc.Want.ToolCalls = nil
				return goodStream(t), tc
			},
		},
		{
			name: "the wrong stop reason",
			want: `the stop reason is "toolUse"; want "stop"`,
			build: func(t *testing.T) ([]wire.AssistantEvent, Case) {
				tc := probeCase()
				tc.Want.Stop = wire.StopStop
				return goodStream(t), tc
			},
		},
		{
			name: "text the stream never produced",
			want: `text is "hello"; want "hello there"`,
			build: func(t *testing.T) ([]wire.AssistantEvent, Case) {
				tc := probeCase()
				tc.Want.Text = "hello there"
				return goodStream(t), tc
			},
		},
		{
			name: "a delta for a block nobody opened",
			want: "before any start event opened it",
			build: func(t *testing.T) ([]wire.AssistantEvent, Case) {
				events := goodStream(t)
				return append(events[:len(events)-1], wire.AssistantEvent{Type: wire.EventTextDelta, ContentIndex: wire.Ptr(7)}, events[len(events)-1]), probeCase()
			},
		},
		{
			name: "a block that skips an index",
			want: "opens at content index 1",
			build: func(t *testing.T) ([]wire.AssistantEvent, Case) {
				events := goodStream(t)
				events[1].ContentIndex = wire.Ptr(1)
				return events, probeCase()
			},
		},
		{
			name: "a message holding a block the stream never opened",
			want: "the message holds 4 blocks but the stream opened 3",
			build: func(t *testing.T) ([]wire.AssistantEvent, Case) {
				events := goodStream(t)
				last := events[len(events)-1]
				message := last.Message.Clone()
				message.Content.Ensure(3).Type = wire.ContentText
				events[len(events)-1] = wire.AssistantEvent{Type: wire.EventDone, Reason: string(wire.StopToolUse), Message: &message}
				return events, probeCase()
			},
		},
		{
			name: "a terminal message that is still pending",
			want: "still pending",
			build: func(t *testing.T) ([]wire.AssistantEvent, Case) {
				events := goodStream(t)
				frame := provider.NewFrame(probeSuite().Model)
				partial := frame.Partial()
				events[len(events)-1] = wire.AssistantEvent{Type: wire.EventDone, Reason: "stop", Message: &partial}
				return events, probeCase()
			},
		},
		{
			name: "usage that disagrees",
			want: "usage",
			build: func(t *testing.T) ([]wire.AssistantEvent, Case) {
				tc := probeCase()
				tc.Want.Usage = &wire.Usage{Input: 10, Output: 2}
				return goodStream(t), tc
			},
		},
		{
			name: "an error case that succeeds",
			want: `the terminal event is "done"; want "error"`,
			build: func(t *testing.T) ([]wire.AssistantEvent, Case) {
				tc := probeCase()
				tc.Want.Error = "boom"
				return goodStream(t), tc
			},
		},
		{
			name: "an error case whose message is wrong",
			want: "it must mention",
			build: func(t *testing.T) ([]wire.AssistantEvent, Case) {
				tc := probeCase()
				tc.Want.Text, tc.Want.Thinking, tc.Want.ToolCalls, tc.Want.Signature = "", "", nil, ""
				tc.Want.Error = "boom"
				frame := provider.NewFrame(probeSuite().Model)
				return []wire.AssistantEvent{frame.Start(), frame.Error(wire.StopError, "something else")}, tc
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			events, want := tc.build(t)
			problems := auditStream(observationOf(events), probeSuite(), want)
			if len(problems) == 0 {
				t.Fatalf("the defect %q went unreported", tc.name)
			}
			for _, problem := range problems {
				if strings.Contains(problem, tc.want) {
					return
				}
			}
			t.Fatalf("reported %v; none mentions %q", problems, tc.want)
		})
	}
}

// TestAuditRejectsAChangingPartial keeps the aliasing defect separate from the
// table because it needs the observation to outlive the events.
func TestAuditRejectsAChangingPartial(t *testing.T) {
	frame := provider.NewFrame(probeSuite().Model)
	start := frame.Start()
	end := frame.Done(wire.StopStop)
	obs := observation{api: probeAPI}
	obs.keep(start)
	obs.keep(end)

	// What an adapter does when it hands out its live message instead of a
	// clone: the event a caller kept now says something else.
	start.Partial.Content.Ensure(0).Text = "rewritten later"

	problems := auditStream(obs, probeSuite(), Case{Want: Want{}})
	if len(problems) == 0 {
		t.Fatal("a partial that changed after delivery went unreported")
	}
	for _, problem := range problems {
		if strings.Contains(problem, "changed afterwards") {
			return
		}
	}
	t.Fatalf("reported %v; none mentions the changed partial", problems)
}

func TestAuditAcceptsAWellFormedRequest(t *testing.T) {
	seen := []captured{{
		method: "POST",
		path:   probePath,
		header: http.Header{"Authorization": []string{"Bearer " + apiKey}},
		body: []byte(`{"model":"probe-model","stream":true,"messages":[{"role":"user","content":"say hi"}],` +
			`"tools":[{"type":"function","name":"read"}]}`),
	}}
	problems := auditRequest(seen, probeSuite(), probeCase())
	if len(problems) > 0 {
		t.Fatalf("a correct request was rejected:\n  %s", strings.Join(problems, "\n  "))
	}
}

func TestAuditRejectsABrokenRequest(t *testing.T) {
	good := func() captured {
		return captured{
			method: "POST",
			path:   probePath,
			header: http.Header{"Authorization": []string{"Bearer " + apiKey}},
			body: []byte(`{"model":"probe-model","stream":true,"messages":[{"role":"user","content":"say hi"}],` +
				`"tools":[{"type":"function","name":"read"}]}`),
		}
	}

	cases := []struct {
		name   string
		want   string
		mangle func(*captured)
	}{
		{"no request at all", "made 0 requests", func(*captured) {}},
		{"the wrong path", "the request path is", func(c *captured) { c.path = "/other" }},
		{"a missing key header", "must carry the API key", func(c *captured) { c.header.Del("Authorization") }},
		{"a GET", "the request method is", func(c *captured) { c.method = "GET" }},
		{"no stream flag", "does not ask for a stream", func(c *captured) {
			c.body = []byte(`{"model":"probe-model","messages":[{"role":"user","content":"say hi"}],"tools":[{"name":"read"}]}`)
		}},
		{"a swallowed model", "never mentions the model", func(c *captured) {
			c.body = []byte(`{"stream":true,"messages":[{"role":"user","content":"say hi"}],"tools":[{"name":"read"}]}`)
		}},
		{"a swallowed prompt", "never mentions the prompt", func(c *captured) {
			c.body = []byte(`{"model":"probe-model","stream":true,"messages":[],"tools":[{"name":"read"}]}`)
		}},
		{"a swallowed tool", "never mentions the tool", func(c *captured) {
			c.body = []byte(`{"model":"probe-model","stream":true,"messages":[{"role":"user","content":"say hi"}]}`)
		}},
		{"a body that is not JSON", "not JSON", func(c *captured) { c.body = []byte("not json") }},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			request := good()
			tc.mangle(&request)
			seen := []captured{request}
			if tc.name == "no request at all" {
				seen = nil
			}
			problems := auditRequest(seen, probeSuite(), probeCase())
			if len(problems) == 0 {
				t.Fatalf("the defect %q went unreported", tc.name)
			}
			for _, problem := range problems {
				if strings.Contains(problem, tc.want) {
					return
				}
			}
			t.Fatalf("reported %v; none mentions %q", problems, tc.want)
		})
	}
}
