package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"pi-go/internal/httpapi/web"
	"pi-go/internal/wire"
)

// openStream connects to the event route and returns a reader plus a
// cancel that closes the connection, so a test can leave without waiting for
// the heartbeat.
func openStream(t *testing.T, server *Server) (*httptest.Server, *bufio.Reader, context.CancelFunc) {
	t.Helper()

	httpServer := httptest.NewServer(server.Handler())
	ctx, cancel := context.WithCancel(context.Background())
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, httpServer.URL+"/api/events", nil)
	if err != nil {
		cancel()
		httpServer.Close()
		t.Fatalf("new request: %v", err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		cancel()
		httpServer.Close()
		t.Fatalf("open stream: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		cancel()
		httpServer.Close()
		t.Fatalf("stream status = %d", response.StatusCode)
	}
	t.Cleanup(func() {
		cancel()
		response.Body.Close()
		httpServer.Close()
	})
	return httpServer, bufio.NewReader(response.Body), cancel
}

// readFrame reads one SSE frame and returns its event name and data. Comment
// lines and blank separators are skipped, so a test can talk about events.
func readFrame(t *testing.T, reader *bufio.Reader) (string, string) {
	t.Helper()

	var name, data string
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read frame: %v", err)
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case line == "":
			if name != "" || data != "" {
				return name, data
			}
		case strings.HasPrefix(line, "event: "):
			name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			data = strings.TrimPrefix(line, "data: ")
		case strings.HasPrefix(line, ":"):
			// A comment: retry advice or a heartbeat.
		}
	}
}

func TestTheStreamSendsTheFactsFirst(t *testing.T) {
	server := New(Options{Facts: Facts{Version: "1.2.3", Directory: "/tmp/work", Model: "faux"}})
	_, reader, _ := openStream(t, server)

	name, data := readFrame(t, reader)
	if name != "hello" {
		t.Fatalf("first event = %q, want hello", name)
	}
	var facts Facts
	if err := json.Unmarshal([]byte(data), &facts); err != nil {
		t.Fatalf("decode hello: %v", err)
	}
	if facts.Version != "1.2.3" || facts.Directory != "/tmp/work" || facts.Model != "faux" {
		t.Fatalf("facts = %+v", facts)
	}
}

func TestATurnPublishesItsEventsToEverySubscriber(t *testing.T) {
	// Two streams, because a window is not the only reader: a second window or
	// a reloaded page must not steal events from the first.
	release := make(chan struct{})
	server := New(Options{Turn: func(ctx context.Context, prompt string, emit func(wire.AgentEvent) error) (Receipt, error) {
		<-release
		return Receipt{}, emit(wire.AgentEvent{Type: wire.AgentEventMessageEnd})
	}})

	first, firstReader, _ := openStream(t, server)
	_, secondReader, _ := openStream(t, server)
	readFrame(t, firstReader)
	readFrame(t, secondReader)

	post(t, first, "/api/ask", `{"prompt":"hi"}`, http.StatusAccepted)
	close(release)

	for index, reader := range []*bufio.Reader{firstReader, secondReader} {
		name, _ := readFrame(t, reader)
		if name != "status" {
			t.Fatalf("subscriber %d first event = %q, want status", index, name)
		}
		if name, _ := readFrame(t, reader); name != "agent" {
			t.Fatalf("subscriber %d second event = %q, want agent", index, name)
		}
	}
}

func TestStatusBracketsTheTurn(t *testing.T) {
	release := make(chan struct{})
	server := New(Options{Turn: func(ctx context.Context, prompt string, emit func(wire.AgentEvent) error) (Receipt, error) {
		<-release
		return Receipt{}, errors.New("provider exploded")
	}})
	httpServer, reader, _ := openStream(t, server)
	readFrame(t, reader)

	post(t, httpServer, "/api/ask", `{"prompt":"hi"}`, http.StatusAccepted)
	close(release)

	var started, finished statusEvent
	if _, data := readFrame(t, reader); json.Unmarshal([]byte(data), &started) != nil {
		t.Fatalf("decode start status: %s", data)
	}
	if !started.Running || started.Error != "" {
		t.Fatalf("start status = %+v, want running", started)
	}
	if _, data := readFrame(t, reader); json.Unmarshal([]byte(data), &finished) != nil {
		t.Fatalf("decode end status: %s", data)
	}
	if finished.Running || finished.Error != "provider exploded" {
		t.Fatalf("end status = %+v, want stopped with the error", finished)
	}
}

// TestTheReceiptRidesOnTheClosingStatus: the window must learn what the turn
// cost from the same frame that says it stopped. Sent as its own event, a
// window would have to decide which of the two came first, and the answer would
// differ depending on which one a slow reader lost.
func TestTheReceiptRidesOnTheClosingStatus(t *testing.T) {
	server := New(Options{Turn: func(context.Context, string, func(wire.AgentEvent) error) (Receipt, error) {
		return Receipt{Fields: []string{"48 tokens (in 40, out 8)", "4.0 tok/s", "took 2.0s", "ended at 15:03:11"}}, nil
	}})
	httpServer, reader, cancel := openStream(t, server)
	defer cancel()
	readFrame(t, reader)

	post(t, httpServer, "/api/ask", `{"prompt":"hi"}`, http.StatusAccepted)

	var started statusEvent
	if _, data := readFrame(t, reader); json.Unmarshal([]byte(data), &started) != nil {
		t.Fatalf("decode start status: %s", data)
	}
	if len(started.Receipt) != 0 {
		t.Fatalf("start receipt = %q, want none: nothing has been spent yet", started.Receipt)
	}

	var finished statusEvent
	if _, data := readFrame(t, reader); json.Unmarshal([]byte(data), &finished) != nil {
		t.Fatalf("decode end status: %s", data)
	}
	want := []string{"48 tokens (in 40, out 8)", "4.0 tok/s", "took 2.0s", "ended at 15:03:11"}
	if len(finished.Receipt) != len(want) {
		t.Fatalf("receipt = %q, want %q", finished.Receipt, want)
	}
	for index, field := range finished.Receipt {
		if field != want[index] {
			t.Fatalf("receipt[%d] = %q, want %q", index, field, want[index])
		}
	}
}

// TestATurnThatSpentNothingHasNoReceipt keeps an empty array off the wire: the
// page decides "no line" from the absence of the field, and an empty array is
// the same answer with more bytes.
func TestATurnThatSpentNothingHasNoReceipt(t *testing.T) {
	server := New(Options{Turn: func(context.Context, string, func(wire.AgentEvent) error) (Receipt, error) {
		return Receipt{}, nil
	}})
	httpServer, reader, cancel := openStream(t, server)
	defer cancel()
	readFrame(t, reader)

	post(t, httpServer, "/api/ask", `{"prompt":"hi"}`, http.StatusAccepted)
	readFrame(t, reader)

	if _, data := readFrame(t, reader); strings.Contains(data, "receipt") {
		t.Fatalf("status frame carries a receipt: %s", data)
	}
}

func TestAnEmptyPromptIsRefused(t *testing.T) {
	server := New(Options{Turn: func(context.Context, string, func(wire.AgentEvent) error) (Receipt, error) {
		t.Error("the turn should not run")
		return Receipt{}, nil
	}})
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	post(t, httpServer, "/api/ask", `{"prompt":"   "}`, http.StatusBadRequest)
	post(t, httpServer, "/api/ask", `{"prompt":""}`, http.StatusBadRequest)
	post(t, httpServer, "/api/ask", `not json`, http.StatusBadRequest)
}

func TestASecondAskIsRefusedWhileOneRuns(t *testing.T) {
	release := make(chan struct{})
	server := New(Options{Turn: func(ctx context.Context, prompt string, emit func(wire.AgentEvent) error) (Receipt, error) {
		<-release
		return Receipt{}, nil
	}})
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	post(t, httpServer, "/api/ask", `{"prompt":"first"}`, http.StatusAccepted)
	post(t, httpServer, "/api/ask", `{"prompt":"second"}`, http.StatusConflict)
	close(release)

	// The latch must come back, or the window is unusable after one turn.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !server.running.Load() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the turn latch never came back")
}

func TestASlowReaderDoesNotStallTheTurn(t *testing.T) {
	// More events than the buffer holds, published to a subscriber nobody is
	// reading. The turn must finish anyway: a frozen window is not a reason to
	// freeze the model call.
	const burst = streamBuffer * 2
	done := make(chan error, 1)
	server := New(Options{Turn: func(ctx context.Context, prompt string, emit func(wire.AgentEvent) error) (Receipt, error) {
		for range burst {
			if err := emit(wire.AgentEvent{Type: wire.AgentEventMessageUpdate}); err != nil {
				return Receipt{}, err
			}
		}
		done <- nil
		return Receipt{}, nil
	}})

	// Subscribe directly so nothing drains the buffer.
	sub := server.broker.subscribe()
	defer server.broker.unsubscribe(sub)

	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	post(t, httpServer, "/api/ask", `{"prompt":"burst"}`, http.StatusAccepted)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("emit: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("publishing blocked on a subscriber that was not reading")
	}
	if len(sub.events) != streamBuffer {
		t.Fatalf("buffered = %d, want the buffer full at %d", len(sub.events), streamBuffer)
	}
}

func TestAStrangerHostIsRefused(t *testing.T) {
	// The server binds loopback, so this is not about reachability: a page
	// reached through DNS rebinding sends its own name and must be refused.
	server := New(Options{})
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	request, err := http.NewRequest(http.MethodGet, httpServer.URL+"/api/state", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	request.Host = "attacker.example:80"
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", response.StatusCode)
	}
}

func TestAForeignOriginIsRefused(t *testing.T) {
	server := New(Options{})
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	request, err := http.NewRequest(http.MethodPost, httpServer.URL+"/api/ask", strings.NewReader(`{"prompt":"hi"}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	request.Header.Set("Origin", "https://attacker.example")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", response.StatusCode)
	}
}

func TestThePageCarriesNoScriptFromElsewhere(t *testing.T) {
	// A local page that loads a remote script would hand the origin to someone
	// else, and this origin can run tools.
	//
	// The document is what must stay local; the route assertion moved to the
	// bundle, because the built app is where the calls live now.
	server := New(Options{})
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	response, err := http.Get(httpServer.URL + "/")
	if err != nil {
		t.Fatalf("get page: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read page: %v", err)
	}
	text := string(body)
	if strings.Contains(text, "src=\"http") || strings.Contains(text, "href=\"http") {
		t.Fatal("the page references a remote origin")
	}

	assets, err := fs.Glob(web.Assets(), "assets/*")
	if err != nil {
		t.Fatalf("glob assets: %v", err)
	}
	if len(assets) == 0 {
		t.Fatal("the build has no assets")
	}
	joined := ""
	for _, name := range assets {
		if strings.HasSuffix(name, ".map") {
			continue
		}
		chunk, err := fs.ReadFile(web.Assets(), name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		joined += string(chunk)
	}
	if strings.Contains(joined, "http://") || strings.Contains(joined, "https://") {
		// A bundle may legitimately carry a URL in a comment or a docs link, so
		// this only fails when one is loaded as a resource.
		if strings.Contains(joined, "src=\"http") || strings.Contains(joined, "href=\"http") {
			t.Fatal("the bundle loads a remote resource")
		}
	}
	if !strings.Contains(joined, "/api/events") || !strings.Contains(joined, "/api/ask") {
		t.Fatal("the bundle does not talk to the routes it is served with")
	}
}

func TestListenBindsLoopbackOnly(t *testing.T) {
	server := New(Options{})
	address, err := server.Listen()
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer server.Close()

	if !strings.HasPrefix(address, "127.0.0.1:") {
		t.Fatalf("address = %q, want 127.0.0.1", address)
	}
	if url := server.URL(); url != "http://"+address {
		t.Fatalf("URL = %q, want http://%s", url, address)
	}
	response, err := http.Get(server.URL() + "/api/state")
	if err != nil {
		t.Fatalf("get state: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
}

func TestCloseReleasesTheStream(t *testing.T) {
	server := New(Options{})
	if _, err := server.Listen(); err != nil {
		t.Fatalf("listen: %v", err)
	}
	if err := server.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := server.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if server.Addr() != "" && server.listener != nil {
		// Addr may still report the old address; what matters is that the
		// listener is closed, which the failed connection below proves.
		t.Log("listener retained for reporting")
	}
	if _, err := http.Get(server.URL() + "/api/state"); err == nil {
		t.Fatal("the server answered after Close")
	}
}

// post sends a body and asserts the status.
func post(t *testing.T, server *httptest.Server, path, body string, want int) {
	t.Helper()
	response, err := http.Post(server.URL+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post %s: %v", path, err)
	}
	defer response.Body.Close()
	if response.StatusCode != want {
		message, _ := io.ReadAll(response.Body)
		t.Fatalf("post %s = %d, want %d: %s", path, response.StatusCode, want, message)
	}
}
