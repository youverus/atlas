package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"pi-go/internal/provider/faux"
	"pi-go/internal/session"
	"pi-go/internal/wire"
)

// forkConversation asks one question and returns the app, the manager, and the
// answer, which is the state every fork test starts from.
func forkConversation(t *testing.T) (*App, *session.Manager) {
	t.Helper()
	manager := newSession(t)
	application := newTestApp(t, manager, faux.New(faux.Response{Text: "answer"}))
	if _, err := application.Ask(context.Background(), wire.UserMessage("hi"), nil); err != nil {
		t.Fatalf("ask: %v", err)
	}
	return application, manager
}

// A fork is a copy, not a move: the conversation continues under a new id and
// the file it came from is still on disk with the entries it had.
func TestForkSessionContinuesInANewSession(t *testing.T) {
	application, manager := forkConversation(t)
	oldID := manager.ID()
	oldFile := manager.File()
	before, err := os.ReadFile(oldFile)
	if err != nil {
		t.Fatalf("read the original session: %v", err)
	}

	id, err := application.ForkSession()
	if err != nil {
		t.Fatalf("fork: %v", err)
	}

	if id == oldID {
		t.Fatalf("fork kept the id %q", id)
	}
	if got := manager.ID(); got != id {
		t.Fatalf("manager id = %q, want the fork %q", got, id)
	}
	if got := manager.Header().ParentSession; got != oldFile {
		t.Fatalf("parent = %q, want the original file %q", got, oldFile)
	}
	if newFile := manager.File(); newFile == oldFile || newFile == "" {
		t.Fatalf("fork file = %q, want a new one", newFile)
	}
	after, err := os.ReadFile(oldFile)
	if err != nil {
		t.Fatalf("read the original session after the fork: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("the original session file changed during the fork")
	}
	if got := application.MessageCount(); got != 2 {
		t.Fatalf("messages = %d, want the branch to survive (2)", got)
	}
	if got := application.Messages()[1].Text(); got != "answer" {
		t.Fatalf("last message = %q, want the answer from the original session", got)
	}
}

// The fork is a real session from here on: what comes next is written to the
// new file and the original stays as it was.
func TestForkSessionWritesOnwardToTheNewFile(t *testing.T) {
	manager := newSession(t)
	application := newTestApp(t, manager, faux.New(
		faux.Response{Text: "answer"},
		faux.Response{Text: "after the fork"},
	))
	if _, err := application.Ask(context.Background(), wire.UserMessage("hi"), nil); err != nil {
		t.Fatalf("ask: %v", err)
	}
	oldFile := manager.File()
	before, err := os.ReadFile(oldFile)
	if err != nil {
		t.Fatalf("read the original session: %v", err)
	}

	if _, err := application.ForkSession(); err != nil {
		t.Fatalf("fork: %v", err)
	}
	newFile := manager.File()
	if _, err := application.Ask(context.Background(), wire.UserMessage("and now"), nil); err != nil {
		t.Fatalf("ask after the fork: %v", err)
	}

	original, err := os.ReadFile(oldFile)
	if err != nil {
		t.Fatalf("read the original session: %v", err)
	}
	if !bytes.Equal(before, original) {
		t.Fatal("the turn after the fork was written to the original file")
	}
	forked, err := os.ReadFile(newFile)
	if err != nil {
		t.Fatalf("read the fork: %v", err)
	}
	if !bytes.Contains(forked, []byte("and now")) {
		t.Fatal("the fork file does not hold the turn that followed it")
	}
}

// A name is part of the conversation, so the fork keeps it.
func TestForkSessionKeepsTheName(t *testing.T) {
	application, _ := forkConversation(t)
	if err := application.SetName("review"); err != nil {
		t.Fatalf("set name: %v", err)
	}
	if _, err := application.ForkSession(); err != nil {
		t.Fatalf("fork: %v", err)
	}
	if got := application.Name(); got != "review" {
		t.Fatalf("name = %q, want the fork to keep it", got)
	}
}

// Nothing to fork from: an empty session has no leaf, so the branch builder
// would answer with a not-found carrying an empty id. The state gets its own
// error, and the session is left exactly as it was.
func TestForkSessionNeedsAConversation(t *testing.T) {
	application := newTestApp(t, newSession(t), faux.New())
	before := application.Session().ID()

	if _, err := application.ForkSession(); !errors.Is(err, ErrNothingToFork) {
		t.Fatalf("err = %v, want ErrNothingToFork", err)
	}
	if got := application.Session().ID(); got != before {
		t.Fatalf("id = %q, want the empty session left alone (%q)", got, before)
	}
}

func TestForkSessionWhileBusy(t *testing.T) {
	prov := newGateProvider()
	application := newTestApp(t, newSession(t), prov)

	done := make(chan error, 1)
	go func() {
		_, err := application.Ask(context.Background(), wire.UserMessage("hi"), nil)
		done <- err
	}()
	<-prov.started

	if _, err := application.ForkSession(); !errors.Is(err, ErrBusy) {
		t.Fatalf("err = %v, want ErrBusy", err)
	}

	close(prov.release)
	if err := <-done; err != nil {
		t.Fatalf("ask: %v", err)
	}
}

// An in-memory conversation forks in memory: a new id and the same entries,
// with nothing written to disk.
func TestForkSessionStaysInMemory(t *testing.T) {
	manager, err := session.InMemory(t.TempDir(), session.Options{})
	if err != nil {
		t.Fatalf("in-memory session: %v", err)
	}
	application := newTestApp(t, manager, faux.New(faux.Response{Text: "answer"}))
	if _, err := application.Ask(context.Background(), wire.UserMessage("hi"), nil); err != nil {
		t.Fatalf("ask: %v", err)
	}
	oldID := manager.ID()

	if _, err := application.ForkSession(); err != nil {
		t.Fatalf("fork: %v", err)
	}
	if got := manager.ID(); got == oldID {
		t.Fatalf("id = %q, want a new one", got)
	}
	if manager.Persisted() {
		t.Fatal("the in-memory fork started writing files")
	}
	if got := application.MessageCount(); got != 2 {
		t.Fatalf("messages = %d, want the branch to survive", got)
	}
}

func TestForkPointsListAnswersOnePerTurn(t *testing.T) {
	manager := newSession(t)
	application := newTestApp(t, manager, faux.New(
		faux.Response{Text: "first reply"},
		faux.Response{Text: "second reply"},
	))

	if got := application.ForkPoints(); len(got) != 0 {
		t.Fatalf("empty fork points = %+v, want empty", got)
	}

	// An image-only prompt opens a turn but has no text of its own; the answer
	// that closes it is still a point.
	mustAppend(t, manager, wire.UserMessageBlocks(wire.ImageContent("AAAA", "image/png")))
	a0 := mustAppend(t, manager, wire.AssistantMessage(wire.TextContent("reply to the image")))
	mustAppend(t, manager, wire.UserMessage("first prompt"))
	mustAppend(t, manager, wire.AssistantMessage(wire.TextContent("first reply")))
	mustAppend(t, manager, wire.UserMessage("second prompt"))
	// An assistant message that still holds a tool call cannot end a turn: the
	// branch would carry a call with no result.
	mustAppend(t, manager, wire.AssistantMessage(
		wire.TextContent("let me look"),
		wire.ToolCallContent("call-1", "read", []byte(`{"path":"a.go"}`)),
	))
	a2 := mustAppend(t, manager, wire.AssistantMessage(wire.TextContent("second reply")))

	got := application.ForkPoints()
	if len(got) != 3 {
		t.Fatalf("fork points = %+v, want 3", got)
	}
	if got[0].EntryID != a0 || got[0].Turn != 1 || got[0].Text != "reply to the image" {
		t.Fatalf("first = %+v, want turn 1 / %q", got[0], "reply to the image")
	}
	if got[1].Turn != 2 || got[1].Text != "first reply" {
		t.Fatalf("second = %+v, want turn 2 / %q", got[1], "first reply")
	}
	// The tool-calling message is skipped, so the closing answer replaces it
	// rather than adding a second point to the same turn.
	if got[2].EntryID != a2 || got[2].Turn != 3 || got[2].Text != "second reply" {
		t.Fatalf("third = %+v, want turn 3 / %q", got[2], "second reply")
	}
}

func TestForkAtFirstAnswerKeepsTheTurn(t *testing.T) {
	manager := newSession(t)
	application := newTestApp(t, manager, faux.New(faux.Response{Text: "answer"}))
	if _, err := application.Ask(context.Background(), wire.UserMessage("only prompt"), nil); err != nil {
		t.Fatalf("ask: %v", err)
	}
	oldID := manager.ID()
	oldFile := manager.File()
	before, err := os.ReadFile(oldFile)
	if err != nil {
		t.Fatalf("read original: %v", err)
	}

	points := application.ForkPoints()
	if len(points) != 1 {
		t.Fatalf("fork points = %+v, want 1", points)
	}
	if err := application.ForkAt(points[0].EntryID); err != nil {
		t.Fatalf("fork at the first answer: %v", err)
	}
	if manager.ID() == oldID {
		t.Fatal("fork reused session id")
	}
	if manager.Header().ParentSession != oldFile {
		t.Fatalf("parentSession = %q, want %q", manager.Header().ParentSession, oldFile)
	}
	// The branch is taken after the answer, so both halves of the turn survive.
	messages := application.Messages()
	if len(messages) != 2 || messages[0].Text() != "only prompt" || messages[1].Text() != "answer" {
		t.Fatalf("messages = %+v, want [only prompt answer]", messages)
	}
	after, err := os.ReadFile(oldFile)
	if err != nil {
		t.Fatalf("read original after fork: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("original file modified by fork")
	}
}

func TestForkAtAnswerRestoresHistoryModelAndThinking(t *testing.T) {
	prov := faux.New(
		faux.Response{Text: "a1"},
		faux.Response{Text: "a2"},
	)
	manager := newSession(t)
	application := newTestApp(t, manager, prov, func(cfg *Config) {
		cfg.Resolver = serveAll(prov)
	})

	if _, err := application.Ask(context.Background(), wire.UserMessage("q1"), nil); err != nil {
		t.Fatalf("ask 1: %v", err)
	}
	if _, err := application.SetModel("other", "m-2"); err != nil {
		t.Fatalf("set model: %v", err)
	}
	if err := application.SetThinkingLevel("high"); err != nil {
		t.Fatalf("set thinking: %v", err)
	}
	if _, err := application.Ask(context.Background(), wire.UserMessage("q2"), nil); err != nil {
		t.Fatalf("ask 2: %v", err)
	}
	// Advance model and thinking after a2; forking at a2 must restore what was
	// active when it was produced (other/m-2, high), not what followed it.
	if _, err := application.SetModel("third", "m-3"); err != nil {
		t.Fatalf("set model 3: %v", err)
	}
	if err := application.SetThinkingLevel("low"); err != nil {
		t.Fatalf("set thinking low: %v", err)
	}
	application.Steer(wire.UserMessage("stale steer"))
	if _, err := application.BeginSide(); err != nil {
		t.Fatalf("begin side: %v", err)
	}

	points := application.ForkPoints()
	if len(points) != 2 {
		t.Fatalf("fork points = %+v, want 2", points)
	}
	oldFile := manager.File()
	if err := application.ForkAt(points[1].EntryID); err != nil {
		t.Fatalf("fork at the second answer: %v", err)
	}
	if manager.Header().ParentSession != oldFile {
		t.Fatalf("parentSession = %q, want %q", manager.Header().ParentSession, oldFile)
	}
	messages := application.Messages()
	if len(messages) != 4 || messages[2].Text() != "q2" || messages[3].Text() != "a2" {
		t.Fatalf("messages = %+v, want [q1 a1 q2 a2]", messages)
	}
	if got := application.Model(); got.Provider != "other" || got.ID != "m-2" {
		t.Fatalf("model = %+v, want other/m-2", got)
	}
	if got := application.ThinkingLevel(); got != "high" {
		t.Fatalf("thinking = %q, want high", got)
	}
	if application.PendingCount() != 0 {
		t.Fatalf("pending = %d, want cleared queue", application.PendingCount())
	}
	if application.Side() != nil {
		t.Fatal("side conversation survived fork")
	}
}

func TestForkAtRejectsInvalidEntriesAndBusy(t *testing.T) {
	manager := newSession(t)
	application := newTestApp(t, manager, faux.New(faux.Response{Text: "answer"}))
	if _, err := application.Ask(context.Background(), wire.UserMessage("hi"), nil); err != nil {
		t.Fatalf("ask: %v", err)
	}

	if err := application.ForkAt("missing"); !errors.Is(err, ErrInvalidForkEntry) {
		t.Fatalf("missing entry err = %v, want ErrInvalidForkEntry", err)
	}
	// A prompt is not a point: a branch is taken after an answer, so the user
	// message that opened the turn is rejected the way an answer used to be.
	userID := ""
	for _, entry := range manager.Entries() {
		if entry.Type != session.TypeMessage {
			continue
		}
		var probe struct {
			Role wire.Role `json:"role"`
		}
		if err := json.Unmarshal(entry.Message, &probe); err == nil && probe.Role == wire.RoleUser {
			userID = entry.ID
		}
	}
	if userID == "" {
		t.Fatal("the session has no user entry to reject")
	}
	if err := application.ForkAt(userID); !errors.Is(err, ErrInvalidForkEntry) {
		t.Fatalf("user entry err = %v, want ErrInvalidForkEntry", err)
	}

	// A state entry is not a message either.
	if err := application.SetThinkingLevel("high"); err != nil {
		t.Fatalf("set thinking: %v", err)
	}
	stateID := ""
	for _, entry := range manager.Entries() {
		if entry.Type != session.TypeMessage {
			stateID = entry.ID
		}
	}
	if stateID == "" {
		t.Fatal("the session has no state entry to reject")
	}
	if err := application.ForkAt(stateID); !errors.Is(err, ErrInvalidForkEntry) {
		t.Fatalf("state entry err = %v, want ErrInvalidForkEntry", err)
	}

	prov := newGateProvider()
	busyApp := newTestApp(t, newSession(t), prov)
	done := make(chan error, 1)
	go func() {
		_, err := busyApp.Ask(context.Background(), wire.UserMessage("hi"), nil)
		done <- err
	}()
	<-prov.started
	if err := busyApp.ForkAt("any"); !errors.Is(err, ErrBusy) {
		t.Fatalf("busy err = %v, want ErrBusy", err)
	}
	close(prov.release)
	if err := <-done; err != nil {
		t.Fatalf("ask: %v", err)
	}
}

func TestCloneSessionHandlesDiskInMemoryEmptyAndUnsaved(t *testing.T) {
	// Empty session cannot clone.
	emptyApp := newTestApp(t, newSession(t), faux.New())
	if _, err := emptyApp.CloneSession(); !errors.Is(err, ErrNothingToFork) {
		t.Fatalf("empty clone err = %v, want ErrNothingToFork", err)
	}

	// Disk-backed session with only a state entry (not saved yet) rejects clone.
	unsavedApp := newTestApp(t, newSession(t), faux.New())
	if err := unsavedApp.SetThinkingLevel("high"); err != nil {
		t.Fatalf("set thinking: %v", err)
	}
	if _, err := unsavedApp.CloneSession(); !errors.Is(err, ErrSessionNotSaved) {
		t.Fatalf("unsaved clone err = %v, want ErrSessionNotSaved", err)
	}

	// In-memory session with a state entry clones in memory.
	memManager, err := session.InMemory(t.TempDir(), session.Options{})
	if err != nil {
		t.Fatalf("in-memory session: %v", err)
	}
	memApp := newTestApp(t, memManager, faux.New(faux.Response{Text: "answer"}))
	if err := memApp.SetThinkingLevel("high"); err != nil {
		t.Fatalf("set thinking: %v", err)
	}
	oldMemID := memManager.ID()
	newMemID, err := memApp.CloneSession()
	if err != nil {
		t.Fatalf("in-memory clone: %v", err)
	}
	if newMemID == oldMemID || memManager.Persisted() {
		t.Fatalf("in-memory clone id=%q persisted=%v", newMemID, memManager.Persisted())
	}

	// Disk-backed session with messages clones to a new file pointing at oldFile.
	diskApp, diskManager := forkConversation(t)
	oldFile := diskManager.File()
	oldID := diskManager.ID()
	newID, err := diskApp.CloneSession()
	if err != nil {
		t.Fatalf("disk clone: %v", err)
	}
	if newID == oldID || diskManager.File() == oldFile || diskManager.Header().ParentSession != oldFile {
		t.Fatalf("cloned id=%q file=%q parent=%q", newID, diskManager.File(), diskManager.Header().ParentSession)
	}
	if diskApp.MessageCount() != 2 {
		t.Fatalf("messages = %d, want 2", diskApp.MessageCount())
	}
}
