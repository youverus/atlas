package webhost

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"pi-go/internal/app"
	"pi-go/internal/assembly"
	"pi-go/internal/provider/faux"
	"pi-go/internal/session"
	"pi-go/internal/tool"
	"pi-go/internal/wire"
)

func TestHostMultiSessionSwitchesFreelyAndRunsConcurrently(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ATLAS_HOME", home)
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(cfgPath, []byte(`{
		"defaultModel": "p1/m1",
		"providers": {
			"p1": {
				"api": "openai-completions",
				"apiKey": "k1",
				"models": [{"id": "m1", "name": "One", "contextWindow": 8000}]
			}
		}
	}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cwd := t.TempDir()
	kernel, _, err := assembly.New(context.Background(), assembly.Options{
		ConfigPath: cfgPath,
		Workdir:    cwd,
		NoMCP:      true,
	})
	if err != nil {
		t.Fatalf("assembly.New: %v", err)
	}
	defer kernel.Close()

	if !kernel.CanSpawnSessions() {
		t.Fatal("CanSpawnSessions() = false, want true")
	}

	host := NewWith(kernel, Options{Projects: filepath.Join(t.TempDir(), "projects.json")})
	sessionA := host.CurrentSession()
	if sessionA == "" {
		t.Fatal("initial CurrentSession() is empty")
	}

	// Spawning a new session opens an independent slot without mutating session A.
	if err := host.NewSession(); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	sessionB := host.CurrentSession()
	if sessionB == "" || sameSessionFile(sessionA, sessionB) {
		t.Fatalf("sessionB = %q, want distinct from sessionA = %q", sessionB, sessionA)
	}

	// Install independent blocking faux apps on slots A and B to verify true concurrent execution.
	blockA := &blockingHostTool{started: make(chan struct{}), release: make(chan struct{})}
	blockB := &blockingHostTool{started: make(chan struct{}), release: make(chan struct{})}
	appA, mgrA := newBlockingTestApp(t, cwd, blockA, "reply A")
	appB, mgrB := newBlockingTestApp(t, cwd, blockB, "reply B")
	_ = mgrA
	_ = mgrB

	keyA := sessionSlotKey(appA.Session().File())
	keyB := sessionSlotKey(appB.Session().File())
	host.mu.Lock()
	host.slots = map[string]*sessionSlot{
		keyA: {live: &assembly.LiveSession{App: appA}},
		keyB: {live: &assembly.LiveSession{App: appB}},
	}
	host.activeKey = keyA
	host.mu.Unlock()

	errA := make(chan error, 1)
	go func() {
		_, err := host.SessionAsk(context.Background(), keyA, "ask A", nil)
		errA <- err
	}()
	<-blockA.started

	// Switching to session B while session A is actively running succeeds and keeps A running.
	if err := host.OpenSession(keyB); err != nil {
		t.Fatalf("OpenSession(B) while A runs failed: %v", err)
	}
	if !sameSessionFile(host.CurrentSession(), keyB) {
		t.Fatalf("CurrentSession = %q, want %q", host.CurrentSession(), keyB)
	}
	if !appA.Busy() {
		t.Fatal("session A stopped running after switching view to session B")
	}

	// Refusing to archive a running background session protects active work.
	if err := host.ArchiveSession(keyA); err == nil {
		t.Fatal("ArchiveSession(running A) succeeded, want error")
	}

	// Session B can run a turn concurrently while session A is still mid-turn.
	errB := make(chan error, 1)
	go func() {
		_, err := host.SessionAsk(context.Background(), keyB, "ask B", nil)
		errB <- err
	}()
	<-blockB.started

	if !appA.Busy() || !appB.Busy() {
		t.Fatalf("expected both sessions busy concurrently, got A=%v B=%v", appA.Busy(), appB.Busy())
	}

	// Switch back to session A while both run, then let both finish.
	if err := host.OpenSession(keyA); err != nil {
		t.Fatalf("OpenSession(A) while both run failed: %v", err)
	}
	close(blockB.release)
	close(blockA.release)
	if err := <-errA; err != nil {
		t.Fatalf("session A ask: %v", err)
	}
	if err := <-errB; err != nil {
		t.Fatalf("session B ask: %v", err)
	}

	transcriptA, err := host.Transcript()
	if err != nil {
		t.Fatalf("Transcript A: %v", err)
	}
	if len(transcriptA) < 2 {
		t.Fatalf("transcriptA = %+v, want prompt and answer", transcriptA)
	}
}

func newBlockingTestApp(t *testing.T, cwd string, toolImpl tool.Tool, answer string) (*app.App, *session.Manager) {
	t.Helper()
	prov := faux.New(
		faux.Response{ToolCalls: []wire.Content{wire.ToolCallContent("c1", "block", json.RawMessage(`{}`))}},
		faux.Response{Text: answer},
	)
	manager, err := session.Create(cwd, t.TempDir(), session.Options{})
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	application, err := app.New(app.Config{
		Model:        wire.Model{ID: "m1", Name: "One", API: "faux", Provider: "p1"},
		Provider:     prov,
		SystemPrompt: app.StaticPrompt("base"),
		Tools:        app.StaticTools([]tool.Tool{toolImpl}),
	}, manager)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	return application, manager
}
