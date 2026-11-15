package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"pi-go/internal/tool"
)

// httpFake records what a streamable HTTP server saw, so the assertions can be
// about the wire rather than about our own state.
type httpFake struct {
	mu       sync.Mutex
	accepts  []string
	sessions []string
	versions []string
	methods  []string
	deletes  int
}

func (f *httpFake) record(r *http.Request, method string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.accepts = append(f.accepts, r.Header.Get("Accept"))
	f.sessions = append(f.sessions, r.Header.Get(sessionHeader))
	f.versions = append(f.versions, r.Header.Get(versionHeader))
	f.methods = append(f.methods, method)
}

func (f *httpFake) snapshot() (methods, sessions, versions, accepts []string, deletes int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.methods...), append([]string(nil), f.sessions...),
		append([]string(nil), f.versions...), append([]string(nil), f.accepts...), f.deletes
}

// TestStreamableHTTPSession covers the four things the transport has to get
// right at once: both content types offered, the session id carried forward, the
// negotiated version on every later request, and an answer that arrives as an
// event stream the client stops reading when it has what it asked for.
func TestStreamableHTTPSession(t *testing.T) {
	fake := &httpFake{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			// The stream channel is not what this test is about. Refusing it is
			// what a server without one does, and it keeps the assertions below
			// about POSTs only.
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.Method == http.MethodDelete {
			fake.mu.Lock()
			fake.deletes++
			session := r.Header.Get(sessionHeader)
			fake.mu.Unlock()
			if session != "s-1" {
				t.Errorf("DELETE session = %q, want s-1", session)
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
			return
		}
		var message incoming
		if err := json.Unmarshal(body, &message); err != nil {
			t.Errorf("decode body: %v", err)
			return
		}
		fake.record(r, message.Method)

		switch message.Method {
		case "initialize":
			w.Header().Set(sessionHeader, "s-1")
			writeJSON(w, message.ID, map[string]any{
				"protocolVersion": protocolVersion,
				"serverInfo":      map[string]any{"name": "http-fake", "version": "2.0.0"},
			})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			writeJSON(w, message.ID, map[string]any{"tools": []any{
				map[string]any{"name": "ping", "description": "say pong", "inputSchema": map[string]any{"type": "object"}},
			}})
		case "tools/call":
			// The stream stays open after the answer, which is legal: a server
			// may use it for progress. The client has to stop reading when it
			// has the response instead of waiting for the end of the stream.
			w.Header().Set("Content-Type", "text/event-stream")
			encoded, err := json.Marshal(response(*message.ID, map[string]any{
				"content": []any{map[string]any{"type": "text", "text": "pong"}},
			}))
			if err != nil {
				t.Errorf("encode: %v", err)
				return
			}
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", encoded)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		default:
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	defer server.Close()

	set := Open(context.Background(), []Spec{{Name: "remote", URL: server.URL}}, Identity{Name: "atlas", Version: "0.7.0"})
	if err := set.Servers()[0].Err; err != nil {
		t.Fatalf("connect: %v", err)
	}
	if got := set.Servers()[0].Version; got != "2.0.0" {
		t.Fatalf("server version = %q, want 2.0.0", got)
	}

	proxy := findTool(t, set, "mcp__remote__ping")
	result, err := proxy.Execute(context.Background(), tool.Call{Arguments: json.RawMessage(`{}`)}, nil)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if got := result.Content[0].Text; got != "pong" {
		t.Fatalf("result = %q, want pong from the event stream", got)
	}

	set.Close()

	methods, sessions, versions, accepts, deletes := fake.snapshot()
	if deletes != 1 {
		t.Fatalf("DELETE requests = %d, want the session ended once", deletes)
	}
	for index, method := range methods {
		if method == "initialize" || method == "notifications/initialized" {
			continue
		}
		if sessions[index] != "s-1" {
			t.Fatalf("%s session = %q, want s-1", method, sessions[index])
		}
		if versions[index] != protocolVersion {
			t.Fatalf("%s version header = %q, want %q", method, versions[index], protocolVersion)
		}
	}
	for index, accept := range accepts {
		if methods[index] == "" {
			continue
		}
		if !strings.Contains(accept, "application/json") || !strings.Contains(accept, "text/event-stream") {
			t.Fatalf("%s Accept = %q, want both content types", methods[index], accept)
		}
	}
}

// TestStreamableHTTPReinitializesOnExpiry is the one repair this client has: a
// 404 for a request that carried a session id means the server forgot the
// session, and the spec says to handshake again. The retried request must carry
// the new session, and the retry must happen exactly once.
func TestStreamableHTTPReinitializesOnExpiry(t *testing.T) {
	var mu sync.Mutex
	initialized := 0
	listed := 0
	var successfulSession string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
			return
		}
		var message incoming
		if err := json.Unmarshal(body, &message); err != nil {
			t.Errorf("decode body: %v", err)
			return
		}

		switch message.Method {
		case "initialize":
			mu.Lock()
			initialized++
			session := fmt.Sprintf("s-%d", initialized)
			mu.Unlock()
			w.Header().Set(sessionHeader, session)
			writeJSON(w, message.ID, map[string]any{
				"protocolVersion": protocolVersion,
				"serverInfo":      map[string]any{"name": "expiring", "version": "1"},
			})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			mu.Lock()
			listed++
			first := listed == 1
			mu.Unlock()
			if first {
				// The session the client is holding is gone. A body is not
				// needed: the status is the whole message.
				w.WriteHeader(http.StatusNotFound)
				return
			}
			mu.Lock()
			successfulSession = r.Header.Get(sessionHeader)
			mu.Unlock()
			writeJSON(w, message.ID, map[string]any{"tools": []any{
				map[string]any{"name": "after_expiry", "inputSchema": map[string]any{"type": "object"}},
			}})
		default:
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	defer server.Close()

	set := Open(context.Background(), []Spec{{Name: "remote", URL: server.URL}}, Identity{Name: "atlas", Version: "0.7.0"})
	t.Cleanup(set.Close)

	if err := set.Servers()[0].Err; err != nil {
		t.Fatalf("connect: %v", err)
	}
	if initialized != 2 {
		t.Fatalf("initialize requests = %d, want 2 (the original and the repair)", initialized)
	}
	if successfulSession != "s-2" {
		t.Fatalf("the successful list carried session %q, want s-2", successfulSession)
	}
	if len(set.Servers()[0].Tools) != 1 {
		t.Fatalf("tools = %d, want the one the second attempt returned", len(set.Servers()[0].Tools))
	}
}

// TestStreamableHTTPRefusesMissingCredential turns a 401 into advice rather than
// a status code: the two ways to get a token are a header in the config and
// "auth": "oauth", and the message names both because which one applies is the
// server's decision rather than something this client can discover.
func TestStreamableHTTPRefusesMissingCredential(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	set := Open(context.Background(), []Spec{{Name: "remote", URL: server.URL}}, Identity{Name: "atlas", Version: "0.7.0"})
	t.Cleanup(set.Close)

	err := set.Servers()[0].Err
	if err == nil {
		t.Fatal("connect succeeded, want a 401 refusal")
	}
	if !strings.Contains(err.Error(), "headers") {
		t.Fatalf("error = %v, want it to point at the headers field", err)
	}
}

// writeJSON answers one request the way the spec's JSON shape does.
func writeJSON(w http.ResponseWriter, id *int64, result any) {
	encoded, err := json.Marshal(result)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":%s}`, *id, encoded)
}
