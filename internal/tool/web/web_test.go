package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"pi-go/internal/tool"
)

// Every test in this file points the tools at a local stub. Nothing here can
// reach the network, which is the condition for the suite being runnable
// offline at all — and the endpoint is billed per request, so a test that
// escaped would cost money as well as time.

// seen is one request the stub received. The body is decoded to a map because
// the assertions are about which fields were sent, and the fields left out
// matter as much as the ones written.
type seen struct {
	path    string
	auth    string
	body    map[string]any
	request string
}

type reply struct {
	status int
	body   string
}

type stub struct {
	server *httptest.Server

	mu   sync.Mutex
	seen []seen
}

// newStub answers with the replies in order and repeats the last one, so a
// single reply describes a whole test and two replies describe one retry.
func newStub(t *testing.T, replies ...reply) *stub {
	t.Helper()
	s := &stub{}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var decoded map[string]any
		_ = json.Unmarshal(raw, &decoded)

		s.mu.Lock()
		s.seen = append(s.seen, seen{path: r.URL.Path, auth: r.Header.Get("Authorization"), body: decoded, request: string(raw)})
		index := len(s.seen) - 1
		s.mu.Unlock()
		if index >= len(replies) {
			index = len(replies) - 1
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(replies[index].status)
		_, _ = io.WriteString(w, replies[index].body)
	}))
	t.Cleanup(s.server.Close)
	return s
}

// run executes one tool by name against the stub.
func (s *stub) run(t *testing.T, name, arguments string) (tool.Result, error) {
	t.Helper()
	for _, candidate := range Tools(Options{
		BaseURL:    s.server.URL,
		APIKey:     "test-key",
		HTTPClient: s.server.Client(),
		// The retry path is worth testing, not worth waiting for.
		RetryDelay: time.Nanosecond,
	}) {
		if candidate.Name() == name {
			return candidate.Execute(context.Background(), tool.Call{
				ID:        "call_1",
				Name:      name,
				Arguments: json.RawMessage(arguments),
			}, func(tool.Partial) {})
		}
	}
	t.Fatalf("no tool named %q", name)
	return tool.Result{}, nil
}

// runWithSpill is run with a destination for output too long to show, which
// only the fetch tool has a use for.
func (s *stub) runWithSpill(t *testing.T, name, arguments string, spill *tool.Spill) (tool.Result, error) {
	t.Helper()
	for _, candidate := range Tools(Options{
		BaseURL:    s.server.URL,
		APIKey:     "test-key",
		HTTPClient: s.server.Client(),
		RetryDelay: time.Nanosecond,
		Spill:      spill,
	}) {
		if candidate.Name() == name {
			return candidate.Execute(context.Background(), tool.Call{
				ID:        "call_1",
				Name:      name,
				Arguments: json.RawMessage(arguments),
			}, func(tool.Partial) {})
		}
	}
	t.Fatalf("no tool named %q", name)
	return tool.Result{}, nil
}

func (s *stub) calls() []seen {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]seen(nil), s.seen...)
}

// text joins the result's text blocks.
func text(t *testing.T, result tool.Result) string {
	t.Helper()
	var builder strings.Builder
	for _, block := range result.Content {
		builder.WriteString(block.Text)
	}
	return builder.String()
}

const twoResults = `{"results": [
  {"rerank_score": 2.4, "title": "DeepSeek V4 模型发布", "url": "https://example.com/v4",
   "snippet": "short", "summary": "DeepSeek 发布了 V4，改进了工具调用。",
   "site_name": "Example News", "date": "2026-06-01T00:00:00+00:00", "authority": 3},
  {"rerank_score": 1.1, "title": "Second", "url": "https://example.com/2",
   "snippet": "fallback snippet", "summary": "", "site_name": null, "date": null, "authority": -1}
]}`

// TestSearchSendsTheQueryAndRendersTheRanking pins the whole exchange: what goes
// out, and the four facts the model needs back. The score and the grade are on
// the header line because selecting which url to fetch is the only thing a
// ranking is for.
func TestSearchSendsTheQueryAndRendersTheRanking(t *testing.T) {
	s := newStub(t, reply{status: 200, body: twoResults})
	result, err := s.run(t, "websearch", `{"query": "DeepSeek V4 模型"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	calls := s.calls()
	if len(calls) != 1 {
		t.Fatalf("requests: got %d, want 1", len(calls))
	}
	if calls[0].path != "/v1/tools/search" {
		t.Fatalf("path: got %q, want /v1/tools/search", calls[0].path)
	}
	if calls[0].auth != "Bearer test-key" {
		t.Fatalf("authorization: got %q", calls[0].auth)
	}
	if got := calls[0].body["query"]; got != "DeepSeek V4 模型" {
		t.Fatalf("query: got %v", got)
	}
	if got := calls[0].body["count"]; got != float64(10) {
		t.Fatalf("count: got %v, want the default 10", got)
	}
	// need_content is pinned off: page text is a second, separate call.
	if _, present := calls[0].body["need_content"]; present {
		t.Fatalf("need_content was sent: %s", calls[0].request)
	}

	output := text(t, result)
	for _, want := range []string{
		"1. DeepSeek V4 模型发布 (score 2.4, authority 3)",
		"https://example.com/v4",
		"Example News · 2026-06-01",
		"DeepSeek 发布了 V4，改进了工具调用。",
		// An unknown authority is written as ?, not -1: "-1" would read as the
		// least trustworthy source instead of an unstated one.
		"2. Second (score 1.1, authority ?)",
		// A result without a summary falls back to the snippet rather than
		// leaving the block empty.
		"fallback snippet",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output is missing %q:\n%s", want, output)
		}
	}
}

// TestSearchClampsTheCount keeps an overreaching parameter from failing a turn.
// The answer the model wanted is the same one it gets back.
func TestSearchClampsTheCount(t *testing.T) {
	for _, testCase := range []struct {
		arguments string
		want      float64
	}{
		{`{"query": "x", "count": 500}`, 50},
		{`{"query": "x", "count": 0}`, 10},
		{`{"query": "x", "count": 5}`, 5},
	} {
		s := newStub(t, reply{status: 200, body: `{"results": []}`})
		if _, err := s.run(t, "websearch", testCase.arguments); err != nil {
			t.Fatalf("%s: %v", testCase.arguments, err)
		}
		if got := s.calls()[0].body["count"]; got != testCase.want {
			t.Fatalf("%s: count = %v, want %v", testCase.arguments, got, testCase.want)
		}
	}
}

// TestSearchRefusesAnEmptyQuery and the other local checks exist so an obvious
// mistake costs nothing: the endpoint bills a successful call, and a 400 that
// never left the machine costs neither money nor a round trip.
func TestSearchRefusesAnEmptyQuery(t *testing.T) {
	s := newStub(t, reply{status: 200, body: `{"results": []}`})
	if _, err := s.run(t, "websearch", `{"query": "   "}`); err == nil {
		t.Fatal("an empty query was accepted")
	}
	if calls := s.calls(); len(calls) != 0 {
		t.Fatalf("a rejected query still made %d request(s)", len(calls))
	}
}

// TestSearchSaysWhenNothingWasFound keeps an empty answer from reading as a
// failure: no results is a fact about the query, and the model should rephrase
// rather than retry.
func TestSearchSaysWhenNothingWasFound(t *testing.T) {
	s := newStub(t, reply{status: 200, body: `{"results": []}`})
	result, err := s.run(t, "websearch", `{"query": "nothing at all"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := text(t, result); got != "(no results)" {
		t.Fatalf("output: got %q", got)
	}
}

// TestFetchWrapsThePage pins the marker. A page is the one thing a model reads
// here that a stranger wrote, so it arrives labelled as a source.
func TestFetchWrapsThePage(t *testing.T) {
	s := newStub(t, reply{status: 200, body: `{"title": "Title", "content": "# Title\n\nBody text."}`})
	result, err := s.run(t, "webfetch", `{"url": "https://example.com/x"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	calls := s.calls()
	if len(calls) != 1 || calls[0].path != "/v1/tools/fetch" {
		t.Fatalf("requests: %#v", calls)
	}
	if got := calls[0].body["url"]; got != "https://example.com/x" {
		t.Fatalf("url: got %v", got)
	}
	output := text(t, result)
	if !strings.Contains(output, `<page url="https://example.com/x">`) {
		t.Fatalf("output has no page marker:\n%s", output)
	}
	if !strings.Contains(output, "Body text.") {
		t.Fatalf("output lost the body:\n%s", output)
	}
	if !strings.HasSuffix(output, "</page>") {
		t.Fatalf("output is not closed:\n%s", output)
	}
}

// TestFetchRefusesWhatTheEndpointWould covers the parameter checks that save a
// round trip. file:// is the interesting one: it looks like a url and would
// describe a local file if anything ever honoured it.
func TestFetchRefusesWhatTheEndpointWould(t *testing.T) {
	for _, raw := range []string{
		`{"url": ""}`,
		`{"url": "file:///etc/passwd"}`,
		`{"url": "https://user:pass@example.com/x"}`,
		`{"url": "not a url"}`,
	} {
		s := newStub(t, reply{status: 200, body: `{"content": "x"}`})
		if _, err := s.run(t, "webfetch", raw); err == nil {
			t.Fatalf("%s was accepted", raw)
		}
		if calls := s.calls(); len(calls) != 0 {
			t.Fatalf("%s made %d request(s)", raw, len(calls))
		}
	}
}

// TestNoCredentialMeansNoTools is the config contract: without a key the model
// never sees the tools, rather than seeing two it cannot use.
func TestNoCredentialMeansNoTools(t *testing.T) {
	if set := Tools(Options{BaseURL: "http://127.0.0.1:1"}); set != nil {
		t.Fatalf("Tools with no key returned %d tool(s)", len(set))
	}
}

// TestServerErrorsAreRetriedOnce covers the half of the error table that is
// worth another attempt. Five hundred and three is what a busy endpoint says,
// and the second call is what turns it into an answer.
func TestServerErrorsAreRetriedOnce(t *testing.T) {
	s := newStub(t,
		reply{status: 503, body: `{"error": {"message": "busy"}}`},
		reply{status: 200, body: twoResults},
	)
	if _, err := s.run(t, "websearch", `{"query": "x"}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if calls := s.calls(); len(calls) != 2 {
		t.Fatalf("requests: got %d, want 2", len(calls))
	}
}

// TestAClientErrorIsNotRetried is the other half. A page that cannot be read
// will not become readable, and the vendor says so explicitly.
func TestAClientErrorIsNotRetried(t *testing.T) {
	s := newStub(t, reply{status: 404, body: `{"error": {"message": "no content"}}`})
	_, err := s.run(t, "webfetch", `{"url": "https://example.com/gone"}`)
	if err == nil {
		t.Fatal("a 404 was reported as success")
	}
	if calls := s.calls(); len(calls) != 1 {
		t.Fatalf("requests: got %d, want 1", len(calls))
	}
	if !strings.Contains(err.Error(), "no readable page") {
		t.Fatalf("error does not say what to do: %v", err)
	}
}

// TestAnEmptyBalanceIsNamedAsSuch separates the failures the reader can fix
// from the ones the model can. This is the first: nothing the model tries will
// work, so the line says whose problem it is — and it must not quote the key.
func TestAnEmptyBalanceIsNamedAsSuch(t *testing.T) {
	s := newStub(t, reply{status: 402, body: `{"error": {"message": "insufficient balance"}}`})
	_, err := s.run(t, "websearch", `{"query": "x"}`)
	if err == nil {
		t.Fatal("a 402 was reported as success")
	}
	if !strings.Contains(err.Error(), "no balance") {
		t.Fatalf("error: %v", err)
	}
	if strings.Contains(err.Error(), "test-key") {
		t.Fatalf("the credential leaked into an error: %v", err)
	}
	if calls := s.calls(); len(calls) != 1 {
		t.Fatalf("a 402 was retried: %d requests", len(calls))
	}
}

// TestACancelledTurnDoesNotRetry guards the one retry that would be wrong: the
// turn is gone, so the second attempt would spend the budget of a conversation
// nobody is reading.
func TestACancelledTurnDoesNotRetry(t *testing.T) {
	s := newStub(t, reply{status: 503, body: `{}`})
	set := Tools(Options{
		BaseURL:    s.server.URL,
		APIKey:     "test-key",
		HTTPClient: s.server.Client(),
		RetryDelay: time.Nanosecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := set[0].Execute(ctx, tool.Call{Name: "websearch", Arguments: json.RawMessage(`{"query": "x"}`)}, func(tool.Partial) {}); err == nil {
		t.Fatal("a cancelled turn reported success")
	}
	if calls := s.calls(); len(calls) != 0 {
		t.Fatalf("a cancelled turn made %d request(s)", len(calls))
	}
}

// A page longer than the byte limit is the one truncation a second fetch cannot
// undo: the endpoint extracts the same text and hits the same limit. So the rest
// goes to a file and the notice names it, rather than saying "the page continues"
// with no way to continue.
func TestFetchSpillsThePageItCannotShow(t *testing.T) {
	body := strings.Repeat("line of text\n", 5000) // 65KB, over the 50KB limit
	payload, err := json.Marshal(map[string]string{"title": "Long", "content": body})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	spillDir := t.TempDir()
	spill := tool.NewSpill(func() string { return spillDir })

	s := newStub(t, reply{status: 200, body: string(payload)})
	result, err := s.runWithSpill(t, "webfetch", `{"url": "https://example.com/long"}`, spill)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := text(t, result)

	if !strings.Contains(got, "the page continues beyond this point") {
		t.Fatalf("notice lost its warning:\n%s", tail(got))
	}
	marker := "the full output is in "
	index := strings.Index(got, marker)
	if index < 0 {
		t.Fatalf("notice names no file:\n%s", tail(got))
	}
	// The notice is a bracketed line inside <page>, so the path ends at the first
	// closing bracket rather than at the end of the answer.
	rest := got[index+len(marker):]
	path, _, found := strings.Cut(rest, "]")
	if !found {
		t.Fatalf("the notice has no closing bracket:\n%s", tail(got))
	}

	if dir := filepath.Dir(path); dir != spillDir {
		t.Fatalf("spill in %q, want %q", dir, spillDir)
	}
	full, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read spill: %v", err)
	}
	if string(full) != strings.TrimSpace(body) {
		t.Fatalf("spill holds %d bytes, want the whole page (%d)", len(full), len(body))
	}
	// The answer is the head of the file, so the two halves describe one page:
	// "the rest is in this file" is only true if the file starts where the answer
	// stopped. The notice is cut at its own bracket rather than by spelling out
	// its size, because the limit that binds here is the line count.
	shown := got
	shown = shown[strings.Index(shown, "\n")+1:]
	if cut := strings.LastIndex(shown, "\n\n["); cut >= 0 {
		shown = shown[:cut]
	}
	if !strings.HasPrefix(string(full), shown) {
		t.Fatalf("the answer is not the head of the file: len(shown)=%d len(full)=%d first diff at %d",
			len(shown), len(full), firstDiff(shown, string(full)))
	}
}

// firstDiff is where two strings stop agreeing, so a failed prefix assertion
// reports the byte that differs instead of two long openings that look the same.
func firstDiff(a, b string) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return min(len(a), len(b))
}

// head is the first n characters, so a failed assertion prints a page's opening
// instead of the page.
func head(text string, n int) string {
	if len(text) <= n {
		return text
	}
	return text[:n] + "..."
}

// tail is the last few hundred characters, so a failed notice assertion prints
// the end of the answer instead of the whole page.
func tail(text string) string {
	if len(text) <= 400 {
		return text
	}
	return "..." + text[len(text)-400:]
}
