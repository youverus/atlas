// Package conformance holds the promises every wire adapter must keep, in one
// place, so that a new protocol joins the binary by passing a set of assertions
// instead of by being read carefully.
//
// The bytes differ per protocol and so do the failure modes, but the shape of
// the answer must not differ: one opening event, blocks that grow and close,
// exactly one terminal event with a message whose blocks mean what the wire
// said. Each adapter keeps its own fixtures and calls Run from its own test
// file. That direction is deliberate: which packages know about a concrete
// adapter is a property tests/cli pins, and a test helper is no exception.
package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"pi-go/internal/provider"
	"pi-go/internal/wire"
)

const (
	// apiKey is sent with every case, so the header check has a value to look
	// for without knowing which header an adapter uses.
	apiKey = "sk-conformance"

	// defaultPrompt is the user text when a case does not name its own.
	defaultPrompt = "say hi"

	// systemPrompt is the prompt prefix. Its text is not asserted; it exists so
	// the request carries the leading system message the kernel always sends.
	systemPrompt = "You are the conformance harness."
)

// Case is one recorded exchange: the request the adapter should build and the
// answer the server gives back.
type Case struct {
	// Name titles the subtest.
	Name string

	// Prompt is the user text. Empty uses a fixed sentence.
	Prompt string

	// Tools are declared on the request. Nil declares one tool named read;
	// an empty non-nil slice declares none.
	Tools []wire.Tool

	// Options are passed through, with APIKey overwritten so the header check
	// has something to look for.
	Options provider.Options

	// Status is the HTTP status the server answers with. Zero is 200, which
	// means Body is a server-sent event stream; anything else means Body is an
	// error envelope and the adapter must report it as a terminal error
	// message rather than as a Go error.
	Status int

	// Body is the response body.
	Body string

	// Want is what the stream must assemble into.
	Want Want
}

// Want is the expected result of one stream. Empty text and thinking mean "no
// such block", not "skip": a stream that invents content is as wrong as one
// that loses it.
type Want struct {
	Text     string
	Thinking string

	// Signature is the signature recorded on the first thinking block. Empty
	// skips the check, because protocols disagree on whether one exists at all.
	Signature string

	// ToolCalls are the finished calls in content order. Arguments compare as
	// JSON, not as bytes.
	ToolCalls []ToolCall

	// Stop is the stop reason on the terminal message. Empty means stop.
	Stop wire.StopReason

	// Usage, when set, must equal the reported usage exactly.
	Usage *wire.Usage

	// Error is a substring of the terminal error message. Setting it makes the
	// case an error case: the stream must end in an error event instead of a
	// done event, and the message must carry ErrorStop.
	Error string

	// ErrorStop is the stop reason of an error case. Empty means error.
	ErrorStop wire.StopReason
}

// ToolCall is one expected finished call.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

// Suite is what an adapter declares about itself once, next to the cases it
// replays.
type Suite struct {
	// Model is the model every case asks for. BaseURL is overwritten with the
	// test server, and API must name the adapter's routing key.
	Model wire.Model

	// Path is the request path the adapter must post to, for example
	// /chat/completions. It is the one place a protocol's route is written
	// down outside the adapter.
	Path string

	// HeaderKey names the header that carries the API key, for example
	// Authorization or x-api-key.
	HeaderKey string

	// ModelInPath says the protocol names the model in the URL instead of the
	// body, so a request body that never mentions it is correct.
	ModelInPath bool

	// StreamInPath says the protocol selects streaming in the URL instead of
	// the body, so a body without a stream flag is correct.
	StreamInPath bool

	// Cases run in order, each against its own server.
	Cases []Case
}

// Run replays every case through the adapter and reports the promises each one
// broke. It also runs one case twice at once, because an adapter is shared with
// the side conversation and provider.Provider requires Stream to be safe to
// call concurrently.
func Run(t *testing.T, adapter func(wire.Model) provider.Provider, suite Suite) {
	t.Helper()
	if suite.Model.API == "" {
		t.Fatal("conformance: Suite.Model.API is empty; the terminal message is checked against it")
	}
	if suite.Path == "" || suite.HeaderKey == "" {
		t.Fatal("conformance: Suite.Path and Suite.HeaderKey are required")
	}
	// The adapter's own routing key must agree with the model it was handed,
	// otherwise the suite would be checking a stream nobody can reach.
	if api := adapter(suite.Model).API(); api != suite.Model.API {
		t.Fatalf("conformance: adapter reports API %q; Suite.Model.API is %q", api, suite.Model.API)
	}

	for _, tc := range suite.Cases {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			t.Helper()
			if tc.Name == "" {
				t.Fatal("conformance: case has no name")
			}
			ex, server := newExchange(t, tc)
			model := modelFor(suite.Model, server.URL)
			p := adapter(model)
			stream, err := p.Stream(context.Background(), requestFor(model, tc))
			if err != nil {
				t.Fatalf("Stream returned a Go error, so a caller has two failure paths to handle: %v", err)
			}
			obs := drain(p.API(), stream)
			for _, problem := range auditStream(obs, suite, tc) {
				t.Error(problem)
			}
			for _, problem := range auditRequest(ex.seen(), suite, tc) {
				t.Error(problem)
			}
		})
	}

	t.Run("concurrent streams", func(t *testing.T) {
		runConcurrent(t, adapter, suite)
	})
}

// runConcurrent serves one successful case to two streams at once and requires
// both to assemble the same message. The adapter keeps no per-stream state, and
// this is what pins that.
func runConcurrent(t *testing.T, adapter func(wire.Model) provider.Provider, suite Suite) {
	t.Helper()
	tc, ok := concurrentCase(suite)
	if !ok {
		t.Skip("no successful case to replay concurrently")
	}
	_, server := newExchange(t, tc)
	model := modelFor(suite.Model, server.URL)
	p := adapter(model)

	const count = 2
	texts := make([]string, count)
	problems := make([][]string, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			stream, err := p.Stream(context.Background(), requestFor(model, tc))
			if err != nil {
				problems[i] = []string{err.Error()}
				return
			}
			obs := drain(p.API(), stream)
			problems[i] = auditStream(obs, suite, tc)
			if obs.final != nil {
				texts[i] = joinedText(obs.final.Content, wire.ContentText)
			}
		}()
	}
	wg.Wait()
	for i := 0; i < count; i++ {
		for _, problem := range problems[i] {
			t.Errorf("stream %d: %s", i+1, problem)
		}
	}
	for i := 1; i < count; i++ {
		if texts[i] != texts[0] {
			t.Errorf("stream %d assembled %q while stream 1 assembled %q; the adapter is not safe to share", i+1, texts[i], texts[0])
		}
	}
}

// concurrentCase picks the first case that succeeds and carries text. Cases
// that only exercise a failure path prove nothing about sharing.
func concurrentCase(suite Suite) (Case, bool) {
	for _, tc := range suite.Cases {
		if tc.Status == 0 && tc.Want.Error == "" && tc.Want.Text != "" {
			return tc, true
		}
	}
	return Case{}, false
}

func modelFor(model wire.Model, baseURL string) wire.Model {
	model.BaseURL = baseURL
	return model
}

// requestFor builds the request every case sends: the leading system message
// with the tool declarations, then one user turn.
func requestFor(model wire.Model, tc Case) provider.Request {
	prompt := tc.Prompt
	if prompt == "" {
		prompt = defaultPrompt
	}
	tools := tc.Tools
	if tools == nil {
		tools = defaultTools()
	}
	options := tc.Options
	options.APIKey = apiKey
	return provider.Request{
		Model:    model,
		Messages: wire.NormalizeContext(systemPrompt, tools, []wire.Message{wire.UserMessage(prompt)}),
		Options:  options,
	}
}

// defaultTools is one tool with a schema, so the declaration path is always
// exercised: an adapter that forgets to send tools fails the request check
// without every case having to say so.
func defaultTools() []wire.Tool {
	return []wire.Tool{{
		Name:        "read",
		Description: "Read one file.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`),
	}}
}

// captured is one request that reached the server. The *http.Request is not
// kept because its body is already consumed.
type captured struct {
	method string
	path   string
	header http.Header
	body   []byte
}

// exchange answers every request the same way and records what arrived. Every
// request gets the same answer, which is what lets the concurrent run share it.
type exchange struct {
	tc Case

	mu       sync.Mutex
	requests []captured
	readErr  error
}

func newExchange(t *testing.T, tc Case) (*exchange, *httptest.Server) {
	t.Helper()
	ex := &exchange{tc: tc}
	server := httptest.NewServer(http.HandlerFunc(ex.serve))
	t.Cleanup(server.Close)
	return ex, server
}

func (e *exchange) serve(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	e.mu.Lock()
	e.requests = append(e.requests, captured{
		method: r.Method,
		path:   r.URL.Path,
		header: r.Header.Clone(),
		body:   body,
	})
	if err != nil {
		e.readErr = err
	}
	e.mu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	status := e.tc.Status
	if status == 0 {
		status = http.StatusOK
	}
	if status == http.StatusOK {
		w.Header().Set("Content-Type", "text/event-stream")
	} else {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(status)
	io.WriteString(w, e.tc.Body)
}

// seen returns the requests in arrival order.
func (e *exchange) seen() []captured {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]captured, len(e.requests))
	copy(out, e.requests)
	return out
}

// observation is one stream, kept in the form the checks need: the events in
// order, the message the terminal event carried, and a snapshot of every
// partial taken when that event arrived.
type observation struct {
	api      string
	events   []wire.AssistantEvent
	final    *wire.Message
	partials []retained
	recvErr  error
}

// retained is one partial message pointer plus what it said at the time. An
// adapter that hands out a pointer into its live message and keeps mutating it
// rewrites history for every reader that kept an earlier event; comparing the
// snapshot with the current value is how that is caught.
type retained struct {
	at   int
	ptr  *wire.Message
	when string
}

// drain reads a stream to the end.
func drain(api string, stream provider.Stream) observation {
	defer stream.Close()
	obs := observation{api: api}
	for {
		event, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return obs
		}
		if err != nil {
			obs.recvErr = err
			return obs
		}
		obs.keep(event)
	}
}

// keep records one event and the state of its partial.
func (o *observation) keep(event wire.AssistantEvent) {
	if event.Partial != nil {
		o.partials = append(o.partials, retained{at: len(o.events), ptr: event.Partial, when: encodeMessage(*event.Partial)})
	}
	o.events = append(o.events, event)
	switch event.Type {
	case wire.EventDone:
		if event.Message != nil {
			message := event.Message.Clone()
			o.final = &message
		}
	case wire.EventError:
		if event.Error != nil {
			message := event.Error.Clone()
			o.final = &message
		}
	}
}

func encodeMessage(message wire.Message) string {
	data, err := json.Marshal(message)
	if err != nil {
		return "unencodable message: " + err.Error()
	}
	return string(data)
}
