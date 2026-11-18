package google_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"pi-go/internal/provider"
	"pi-go/internal/provider/google"
	"pi-go/internal/wire"
)

const testModel = "gemini-test"

// captured is one request that reached the test server. The http.Request is not
// kept because its body is already consumed.
type captured struct {
	method string
	path   string
	header http.Header
	body   []byte
}

// testServer replays a fixed response for every request and records what the
// adapter sent. Nothing here touches a network: httptest listens on loopback.
type testServer struct {
	*httptest.Server
	captured []captured
}

func newServer(t *testing.T, status int, body string) *testServer {
	t.Helper()
	server := &testServer{}
	server.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		data, err := io.ReadAll(req.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
			return
		}
		server.captured = append(server.captured, captured{
			method: req.Method,
			path:   req.URL.Path,
			header: req.Header.Clone(),
			body:   data,
		})
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)
	return server
}

// sseServer replays a recorded stream from testdata/google.
func sseServer(t *testing.T, name string) *testServer {
	t.Helper()
	return newServer(t, http.StatusOK, string(readFixture(t, name)))
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	path := filepath.Join("..", "..", "..", "testdata", "google", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

func (s *testServer) last(t *testing.T) captured {
	t.Helper()
	if len(s.captured) == 0 {
		t.Fatal("no request reached the server")
	}
	return s.captured[len(s.captured)-1]
}

// providerFor points a provider at the test server. The base URL carries no
// version segment because the recorded paths already are what the adapter
// builds.
func providerFor(server *testServer) *google.Provider {
	return google.New(google.Compat{BaseURL: server.URL})
}

// reasoningModel declares a model that thinks, which is what the thinking
// configuration needs before it travels.
func reasoningModel() wire.Model {
	return wire.Model{
		ID:        testModel,
		API:       google.APIName,
		Provider:  "google",
		Reasoning: true,
		MaxTokens: 4096,
	}
}

func collect(t *testing.T, p *google.Provider, req provider.Request) wire.Message {
	t.Helper()
	stream, err := p.Stream(context.Background(), req)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	message, _, err := provider.Collect(stream)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	return message
}

func messageRequest(messages ...wire.Message) provider.Request {
	return provider.Request{
		Model:    wire.Model{ID: testModel, API: google.APIName, Provider: "google"},
		Messages: messages,
	}
}

// decodeBody returns the request body as a generic JSON tree.
func decodeBody(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode request: %v\n%s", err, body)
	}
	return payload
}

// objectField returns a nested object from a decoded request.
func objectField(t *testing.T, payload map[string]any, key string) map[string]any {
	t.Helper()
	value, ok := payload[key].(map[string]any)
	if !ok {
		t.Fatalf("field %q is not an object: %#v", key, payload[key])
	}
	return value
}

// TestEndpointAndHeaders pins the route and the credential header. This API puts
// the model and the streaming choice in the URL, which is why the body carries
// neither.
func TestEndpointAndHeaders(t *testing.T) {
	server := sseServer(t, "text.sse")
	request := messageRequest(wire.UserMessage("hi"))
	request.Options.APIKey = "test-key"
	collect(t, providerFor(server), request)

	got := server.last(t)
	if got.method != http.MethodPost {
		t.Fatalf("method: got %s; want POST", got.method)
	}
	if want := "/models/" + testModel + ":streamGenerateContent"; got.path != want {
		t.Fatalf("path: got %s; want %s", got.path, want)
	}
	if key := got.header.Get("x-goog-api-key"); key != "test-key" {
		t.Fatalf("x-goog-api-key: got %q", key)
	}
	if accept := got.header.Get("Accept"); accept != "text/event-stream" {
		t.Fatalf("Accept: got %q", accept)
	}
}

// TestUserAgentFollowsTheDeclaration covers both halves of the choice: a
// declared identifier travels verbatim, and an empty one leaves net/http's own
// header rather than a made-up one.
func TestUserAgentFollowsTheDeclaration(t *testing.T) {
	server := sseServer(t, "text.sse")
	request := messageRequest(wire.UserMessage("hi"))
	request.Model.UserAgent = "declared/1.2.3"
	collect(t, providerFor(server), request)

	if got := server.last(t).header.Get("User-Agent"); got != "declared/1.2.3" {
		t.Fatalf("User-Agent: got %q; want declared/1.2.3", got)
	}

	plain := sseServer(t, "text.sse")
	collect(t, providerFor(plain), messageRequest(wire.UserMessage("hi")))
	if got := plain.last(t).header.Get("User-Agent"); got != "Go-http-client/1.1" {
		t.Fatalf("User-Agent with no declaration: got %q; want net/http's own", got)
	}
}

// TestTextStream assembles the plain answer and reads the usage counters, where
// the prompt total includes the cached part.
func TestTextStream(t *testing.T) {
	server := sseServer(t, "text.sse")
	message := collect(t, providerFor(server), messageRequest(wire.UserMessage("hi")))

	if got := message.Text(); got != "Hello world!" {
		t.Fatalf("text: got %q", got)
	}
	if message.StopReason != wire.StopStop {
		t.Fatalf("stop reason: got %q", message.StopReason)
	}
	usage := message.Usage
	if usage == nil {
		t.Fatal("usage is missing")
	}
	want := wire.Usage{Input: 8, Output: 7, CacheRead: 3, TotalTokens: 18}
	if usage.Input != want.Input || usage.Output != want.Output || usage.CacheRead != want.CacheRead || usage.TotalTokens != want.TotalTokens {
		t.Fatalf("usage: got %+v want %+v", *usage, want)
	}
}

// TestThinkingStream pins the sum of the thinking tokens into the output count
// and the signature a text part carries back.
func TestThinkingStream(t *testing.T) {
	server := sseServer(t, "thinking.sse")
	message := collect(t, providerFor(server), messageRequest(wire.UserMessage("hi")))

	if got := message.Text(); got != "Done" {
		t.Fatalf("text: got %q", got)
	}
	var thinking, signature string
	for _, block := range message.Content.Items() {
		switch block.Type {
		case wire.ContentThinking:
			thinking += block.Thinking
			signature = block.ThinkingSignature
		}
	}
	if thinking != "Let me think" {
		t.Fatalf("thinking: got %q", thinking)
	}
	if signature != "c2lnLWE=" {
		t.Fatalf("thinking signature: got %q", signature)
	}

	usage := message.Usage
	if usage == nil || usage.Reasoning == nil || *usage.Reasoning != 8 {
		t.Fatalf("reasoning tokens: got %+v", usage)
	}
	if usage.Output != 12 {
		t.Fatalf("output tokens: got %d want 12 (answer plus thoughts)", usage.Output)
	}
}

// TestToolCallStream pins that a call arriving whole becomes one block with its
// arguments intact, that the hidden signature is kept for replay, and that a
// STOP carrying a call reports a tool use.
func TestToolCallStream(t *testing.T) {
	server := sseServer(t, "tool_call.sse")
	message := collect(t, providerFor(server), messageRequest(wire.UserMessage("hi")))

	if message.StopReason != wire.StopToolUse {
		t.Fatalf("stop reason: got %q", message.StopReason)
	}
	var call *wire.Content
	for _, block := range message.Content.Items() {
		if block.Type == wire.ContentToolCall {
			block := block
			call = &block
		}
	}
	if call == nil {
		t.Fatalf("no tool call in %+v", message.Content.Items())
	}
	if call.ID != "read_1" || call.Name != "read" {
		t.Fatalf("call: got id %q name %q", call.ID, call.Name)
	}
	if string(call.Arguments) != `{"path":"a.go"}` {
		t.Fatalf("arguments: got %s", call.Arguments)
	}
	if call.ThoughtSignature != "c2lnLWM=" {
		t.Fatalf("thought signature: got %q", call.ThoughtSignature)
	}
}

// TestToolCallWithoutIdGetsOne pins the synthesis: this API only started
// requiring an id with Gemini 3, and a call without one cannot be paired with
// its result.
func TestToolCallWithoutIdGetsOne(t *testing.T) {
	const body = `data: {"candidates":[{"content":{"parts":[{"functionCall":{"name":"read","args":{}}}],"role":"model"},"finishReason":"STOP","index":0}]}`

	server := newServer(t, http.StatusOK, body)
	message := collect(t, providerFor(server), messageRequest(wire.UserMessage("hi")))

	for _, block := range message.Content.Items() {
		if block.Type != wire.ContentToolCall {
			continue
		}
		if block.ID == "" {
			t.Fatal("tool call has no id")
		}
		if string(block.Arguments) != "{}" {
			t.Fatalf("arguments: got %s want {}", block.Arguments)
		}
		return
	}
	t.Fatalf("no tool call in %+v", message.Content.Items())
}

// TestLengthStop pins MAX_TOKENS as a length stop rather than a failure.
func TestLengthStop(t *testing.T) {
	server := sseServer(t, "length.sse")
	message := collect(t, providerFor(server), messageRequest(wire.UserMessage("hi")))

	if message.StopReason != wire.StopLength {
		t.Fatalf("stop reason: got %q", message.StopReason)
	}
	if got := message.Text(); got != "Long" {
		t.Fatalf("text: got %q", got)
	}
}

// TestStreamErrorKeepsPartialText pins that a mid-stream error becomes the
// terminal message with the text assembled so far.
func TestStreamErrorKeepsPartialText(t *testing.T) {
	server := sseServer(t, "error.sse")
	message := collect(t, providerFor(server), messageRequest(wire.UserMessage("hi")))

	if message.StopReason != wire.StopError {
		t.Fatalf("stop reason: got %q", message.StopReason)
	}
	if message.ErrorMessage != "google: stream error (INTERNAL): Internal error" {
		t.Fatalf("error message: got %q", message.ErrorMessage)
	}
	if got := message.Text(); got != "Partial" {
		t.Fatalf("text: got %q", got)
	}
}

// TestTruncatedStreamFails pins that a stream closing without a finish reason is
// an error, not a silent success: the caller has to know the answer is partial.
func TestTruncatedStreamFails(t *testing.T) {
	server := sseServer(t, "truncated.sse")
	message := collect(t, providerFor(server), messageRequest(wire.UserMessage("hi")))

	if message.StopReason != wire.StopError {
		t.Fatalf("stop reason: got %q", message.StopReason)
	}
	if message.ErrorMessage != "google: stream ended without a finish reason" {
		t.Fatalf("error message: got %q", message.ErrorMessage)
	}
}

// TestServerError pins the non-200 path: the message names the status and the
// vendor's own explanation.
func TestServerError(t *testing.T) {
	server := newServer(t, http.StatusInternalServerError,
		`{"error":{"code":500,"message":"Internal error","status":"INTERNAL"}}`)
	message := collect(t, providerFor(server), messageRequest(wire.UserMessage("hi")))

	if message.StopReason != wire.StopError {
		t.Fatalf("stop reason: got %q", message.StopReason)
	}
	if message.ErrorMessage != "500 Internal Server Error: Internal error" {
		t.Fatalf("error message: got %q", message.ErrorMessage)
	}
}

// TestContextOverflowIsNamed pins that the vendor's overflow wording reaches the
// shared classifier: this is the signal the kernel's overflow recovery waits for.
func TestContextOverflowIsNamed(t *testing.T) {
	server := newServer(t, http.StatusBadRequest,
		`{"error":{"code":400,"message":"The input token count (1196265) exceeds the maximum number of tokens allowed (1048575)","status":"INVALID_ARGUMENT"}}`)
	message := collect(t, providerFor(server), messageRequest(wire.UserMessage("hi")))

	if message.StopReason != wire.StopError {
		t.Fatalf("stop reason: got %q", message.StopReason)
	}
	if message.ErrorCode != wire.ErrorCodeContextOverflow {
		t.Fatalf("error code: got %q want overflow", message.ErrorCode)
	}
}

// TestEmptyStreamFails pins the case where the connection closed before any
// chunk arrived.
func TestEmptyStreamFails(t *testing.T) {
	server := newServer(t, http.StatusOK, "")
	message := collect(t, providerFor(server), messageRequest(wire.UserMessage("hi")))

	if message.StopReason != wire.StopError {
		t.Fatalf("stop reason: got %q", message.StopReason)
	}
	if message.ErrorMessage != "google: response contained no stream chunks" {
		t.Fatalf("error message: got %q", message.ErrorMessage)
	}
}
