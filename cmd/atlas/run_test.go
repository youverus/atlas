package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pi-go/internal/provider/anthropic"
	"pi-go/internal/provider/openai"
	"pi-go/internal/session"
	"pi-go/internal/wire"
)

// fixtureServer replays one recorded SSE stream and keeps what reached it. The
// listener is loopback only: nothing here leaves the machine.
type fixtureServer struct {
	*httptest.Server
	requests []*http.Request
	body     []byte
}

func newFixtureServer(t *testing.T, name string) *fixtureServer {
	t.Helper()
	response, err := os.ReadFile(filepath.Join("..", "..", "testdata", "sse", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	server := &fixtureServer{}
	server.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		// The body is spent when the handler returns, so it is drained here
		// rather than handed to the test.
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
			return
		}
		server.requests = append(server.requests, req.Clone(context.Background()))
		server.body = body
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, string(response))
	}))
	t.Cleanup(server.Close)
	return server
}

// writeConfig writes a one-provider config pointing at the test server.
func writeConfig(t *testing.T, api, baseURL string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	content := fmt.Sprintf(`{
  "defaultModel": "test/m1",
  "providers": {
    "test": {
      "api": %q,
      "baseUrl": %q,
      "apiKey": "test-key",
      "models": [{"id": "m1", "name": "M1", "contextWindow": 100000, "maxTokens": 2048}]
    }
  }
}`, api, baseURL)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func buffers() (streams, *bytes.Buffer, *bytes.Buffer) {
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	return streams{in: strings.NewReader(""), out: out, err: errOut}, out, errOut
}

func TestRunSendsOneTurn(t *testing.T) {
	server := newFixtureServer(t, "openai_text.sse")
	cfgPath := writeConfig(t, openai.APIName, server.URL)
	workdir := t.TempDir()
	sessionDir := filepath.Join(t.TempDir(), "sessions")

	stdio, out, errOut := buffers()
	opts := options{configPath: cfgPath, prompt: "hi", workdir: workdir, sessionDir: sessionDir}
	if err := run(context.Background(), opts, stdio); err != nil {
		t.Fatalf("run: %v", err)
	}

	if !strings.Contains(out.String(), "Hello world") {
		t.Fatalf("stdout = %q, want the assistant text", out.String())
	}
	if !strings.Contains(errOut.String(), "stop reason stop") {
		t.Fatalf("stderr = %q, want the turn summary", errOut.String())
	}

	// The credential comes from the config, through the adapter wrapper: the
	// kernel never builds it.
	if len(server.requests) != 1 {
		t.Fatalf("server saw %d requests, want 1", len(server.requests))
	}
	if got := server.requests[0].Header.Get("Authorization"); got != "Bearer test-key" {
		t.Fatalf("Authorization = %q, want the configured key", got)
	}

	// The system prompt and the tool declarations travel with the request, so
	// the file tools and bash are reachable from a real run.
	for _, want := range []string{"You are a coding agent", `"name":"bash"`, `"name":"read"`, `"name":"grep"`} {
		if !strings.Contains(string(server.body), want) {
			t.Fatalf("request body is missing %s", want)
		}
	}

	// The turn is on disk: the session file is the point of a persisted run.
	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		t.Fatalf("read session dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("session dir holds %d files, want 1", len(entries))
	}
	content, err := os.ReadFile(filepath.Join(sessionDir, entries[0].Name()))
	if err != nil {
		t.Fatalf("read session: %v", err)
	}
	if !strings.Contains(string(content), "Hello world") || !strings.Contains(string(content), `"hi"`) {
		t.Fatalf("session = %q", content)
	}
}

// TestRunRefusesAThinkingLevelTheModelDoesNotOffer: the check runs before the
// session is opened and before anything is sent, so a level the model cannot
// send costs one error instead of a request that quietly drops it.
func TestRunRefusesAThinkingLevelTheModelDoesNotOffer(t *testing.T) {
	cfgPath := writeConfig(t, openai.APIName, "http://127.0.0.1:1")
	sessionDir := filepath.Join(t.TempDir(), "sessions")
	stdio, _, _ := buffers()
	opts := options{configPath: cfgPath, prompt: "hi", thinking: "max", workdir: t.TempDir(), sessionDir: sessionDir}

	err := run(context.Background(), opts, stdio)
	if err == nil || !strings.Contains(err.Error(), "does not think") {
		t.Fatalf("error = %v, want the level refused", err)
	}
	// The model in the fixture cannot think, so the only level it can carry is
	// the switch.
	if !strings.Contains(err.Error(), "only off") {
		t.Fatalf("error = %v, want the one level it can send named", err)
	}
	if _, statErr := os.Stat(sessionDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("session dir = %v, want a run that stopped before the session", statErr)
	}
}

func TestRunReadsStdinWhenPiped(t *testing.T) {
	server := newFixtureServer(t, "openai_text.sse")
	cfgPath := writeConfig(t, openai.APIName, server.URL)

	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	stdio := streams{in: strings.NewReader("from stdin"), out: out, err: errOut, terminal: false}
	opts := options{configPath: cfgPath, workdir: t.TempDir(), sessionDir: filepath.Join(t.TempDir(), "s")}
	if err := run(context.Background(), opts, stdio); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out.String(), "Hello world") {
		t.Fatalf("stdout = %q", out.String())
	}
}

func TestRunRejectsEmptyStdin(t *testing.T) {
	cfgPath := writeConfig(t, openai.APIName, "http://127.0.0.1:1")
	stdio, _, _ := buffers()
	opts := options{configPath: cfgPath, workdir: t.TempDir(), sessionDir: t.TempDir()}
	err := run(context.Background(), opts, stdio)
	if err == nil || !strings.Contains(err.Error(), "no prompt") {
		t.Fatalf("error = %v, want a no-prompt message", err)
	}
}

func TestRunSwitchesAdapterByAPI(t *testing.T) {
	// One config, two adapters: the routing key is the only difference, which
	// is what "the kernel does not know the vendor" buys.
	server := newFixtureServer(t, "anthropic_text.sse")
	cfgPath := writeConfig(t, anthropic.APIName, server.URL)

	stdio, out, _ := buffers()
	opts := options{configPath: cfgPath, prompt: "hi", workdir: t.TempDir(), sessionDir: filepath.Join(t.TempDir(), "s")}
	if err := run(context.Background(), opts, stdio); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out.String(), "Hi there") {
		t.Fatalf("stdout = %q", out.String())
	}
	if got := server.requests[0].Header.Get("x-api-key"); got != "test-key" {
		t.Fatalf("x-api-key = %q", got)
	}
}

func TestRunFirstRunWritesATemplate(t *testing.T) {
	home := filepath.Join(t.TempDir(), "atlas")
	t.Setenv("ATLAS_HOME", home)

	stdio, _, _ := buffers()
	opts := options{prompt: "hi", workdir: t.TempDir()}
	err := run(context.Background(), opts, stdio)
	if err == nil || !strings.Contains(err.Error(), "created") {
		t.Fatalf("error = %v, want a first-run message", err)
	}
	if _, statErr := os.Stat(filepath.Join(home, "config.json")); statErr != nil {
		t.Fatalf("config not created: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(home, "config.example.json")); statErr != nil {
		t.Fatalf("example not created: %v", statErr)
	}

	// The second run must not offer to create it again: the file is there and
	// only the credential is missing.
	opts = options{listModels: true, workdir: t.TempDir()}
	out := &bytes.Buffer{}
	stdio = streams{in: strings.NewReader(""), out: out, err: io.Discard}
	if err := run(context.Background(), opts, stdio); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if !strings.Contains(out.String(), "DeepSeek Chat") {
		t.Fatalf("list = %q", out.String())
	}
}

func TestExplicitConfigIsNeverCreated(t *testing.T) {
	// A typed path that does not exist is the user's to fix; writing a
	// template there would make a typo look like a fresh install.
	missing := filepath.Join(t.TempDir(), "config.json")
	stdio, _, _ := buffers()
	opts := options{configPath: missing, prompt: "hi", workdir: t.TempDir()}
	err := run(context.Background(), opts, stdio)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("error = %v, want not exist", err)
	}
	if _, statErr := os.Stat(missing); !os.IsNotExist(statErr) {
		t.Fatalf("Stat = %v, want the file absent", statErr)
	}
}

func TestDemoLeavesNoSession(t *testing.T) {
	home := filepath.Join(t.TempDir(), "atlas")
	t.Setenv("ATLAS_HOME", home)

	stdio, out, _ := buffers()
	opts := options{demo: true, prompt: "hi", workdir: t.TempDir()}
	if err := run(context.Background(), opts, stdio); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out.String(), "demo complete") {
		t.Fatalf("stdout = %q", out.String())
	}
	if _, err := os.Stat(filepath.Join(home, "sessions")); !os.IsNotExist(err) {
		t.Fatalf("Stat = %v, want no session directory", err)
	}
}

func TestDemoCanPersist(t *testing.T) {
	t.Setenv("ATLAS_HOME", filepath.Join(t.TempDir(), "atlas"))
	sessionDir := filepath.Join(t.TempDir(), "sessions")

	stdio, _, errOut := buffers()
	opts := options{demo: true, prompt: "hi", workdir: t.TempDir(), sessionDir: sessionDir}
	if err := run(context.Background(), opts, stdio); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(errOut.String(), "session: ") {
		t.Fatalf("stderr = %q, want the session path", errOut.String())
	}
	entries, err := os.ReadDir(sessionDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("session dir = %v, %v", entries, err)
	}
}

func TestJSONModeRefusesToInterleaveAPrompt(t *testing.T) {
	// The REPL prompt would appear between two JSON objects, which breaks a
	// caller that decodes the stream line by line.
	cfgPath := writeConfig(t, openai.APIName, "http://127.0.0.1:1")
	stdio := streams{in: strings.NewReader(""), out: io.Discard, err: io.Discard, terminal: true}
	opts := options{configPath: cfgPath, asJSON: true, workdir: t.TempDir(), sessionDir: t.TempDir()}
	err := run(context.Background(), opts, stdio)
	if err == nil || !strings.Contains(err.Error(), "-json") {
		t.Fatalf("error = %v, want a -json message", err)
	}
}

// -tui asks for a screen the caller cannot have. Saying so is the whole
// point: the alternative is escape sequences in a log file.
func TestTUIOnAPipeNamesTheProblem(t *testing.T) {
	cfgPath := writeConfig(t, openai.APIName, "http://127.0.0.1:1")
	stdio := streams{in: strings.NewReader("hi"), out: io.Discard, err: io.Discard}
	opts := options{configPath: cfgPath, tui: true, workdir: t.TempDir(), sessionDir: t.TempDir()}
	err := run(context.Background(), opts, stdio)
	if err == nil || !strings.Contains(err.Error(), "-tui") {
		t.Fatalf("error = %v, want a -tui message", err)
	}
}

// A resumed session keeps the directory it was recorded in for its tools, so a
// recorded directory that is gone has to fail at startup — before any tool ran —
// and name both ways out. -cwd is the one that keeps working.
func TestResumeReportsMissingCwd(t *testing.T) {
	t.Setenv("ATLAS_HOME", filepath.Join(t.TempDir(), "atlas"))
	recorded := filepath.Join(t.TempDir(), "gone")
	if err := os.MkdirAll(recorded, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	manager, err := session.Create(recorded, t.TempDir(), session.Options{})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := manager.AppendMessage(wire.UserMessage("hello")); err != nil {
		t.Fatalf("append: %v", err)
	}
	sessionFile := manager.File()
	if err := os.RemoveAll(recorded); err != nil {
		t.Fatalf("remove the recorded directory: %v", err)
	}
	current, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	stdio, _, _ := buffers()
	err = run(context.Background(), options{demo: true, prompt: "hi", resumePath: sessionFile}, stdio)
	if err == nil {
		t.Fatal("resuming a session whose directory is gone succeeded")
	}
	for _, want := range []string{recorded, current, "-cwd"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %v, want it to name %q", err, want)
		}
	}

	// The explicit override opens the session and says which directory it used.
	here := t.TempDir()
	stdio, out, errOut := buffers()
	opts := options{demo: true, prompt: "hi", resumePath: sessionFile, workdir: here}
	if err := run(context.Background(), opts, stdio); err != nil {
		t.Fatalf("run with -cwd: %v", err)
	}
	if note := errOut.String(); !strings.Contains(note, "note:") || !strings.Contains(note, here) {
		t.Fatalf("stderr = %q, want a note naming the directory now in use", note)
	}
	if !strings.Contains(out.String(), "demo complete") {
		t.Fatalf("stdout = %q, want the demo to finish", out.String())
	}
}
