package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"pi-go/internal/provider/faux"
	"pi-go/internal/session"
	"pi-go/internal/wire"
)

// seedConversation gives the session in effect one exchange, which is what
// puts a file on disk for /archive to mark.
func seedConversation(t *testing.T, application *App) {
	t.Helper()
	if _, err := application.Ask(context.Background(), wire.UserMessage("hello"), nil); err != nil {
		t.Fatalf("seed ask: %v", err)
	}
}

// otherSession creates a second live conversation in the directory this app
// writes into, which is a target a switch can actually reach.
func otherSession(t *testing.T, application *App) string {
	t.Helper()
	other, err := session.Create(application.Session().Cwd(), application.Session().Dir(), session.Options{})
	if err != nil {
		t.Fatalf("create other session: %v", err)
	}
	if _, err := other.AppendMessage(wire.UserMessage("other")); err != nil {
		t.Fatalf("append to the other session: %v", err)
	}
	return other.File()
}

// Archiving hides the conversation and hands the prompt a fresh one, because a
// user who just put a conversation away expects the next message to start a new
// one rather than reopen the file he closed.
func TestArchiveHidesTheConversationAndStartsANewOne(t *testing.T) {
	application := newTestApp(t, newSession(t), faux.New(faux.Response{Text: "answer"}))
	seedConversation(t, application)
	archivedID := application.Session().ID()
	archivedFile := application.Session().File()

	if err := application.Archive(); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if !session.Archived(archivedFile) {
		t.Fatal("the conversation was not marked archived")
	}
	if _, err := os.Stat(archivedFile); err != nil {
		t.Fatalf("the archived file is gone: %v", err)
	}
	if application.Session().ID() == archivedID {
		t.Fatal("archiving left the prompt on the archived conversation")
	}
	if application.Archived() {
		t.Fatal("the fresh conversation reports itself archived")
	}

	// The transcript is still readable, which is the whole point of archiving
	// rather than deleting.
	info, err := session.ReadInfo(archivedFile)
	if err != nil || info == nil {
		t.Fatalf("read info of the archived session: %+v, %v", info, err)
	}
	if info.MessageCount != 2 {
		t.Fatalf("archived message count = %d, want the two messages it had", info.MessageCount)
	}
}

// An empty conversation has no file, so there is nothing to hide. Saying so is
// better than reporting success while the prompt moves on.
func TestArchiveOfAnEmptyConversationIsRefused(t *testing.T) {
	application := newTestApp(t, newSession(t), faux.New())
	beforeID := application.Session().ID()

	if err := application.Archive(); !errors.Is(err, ErrNothingToArchive) {
		t.Fatalf("archive err = %v, want ErrNothingToArchive", err)
	}
	if application.Session().ID() != beforeID {
		t.Fatal("a refused archive moved the prompt to another conversation")
	}
}

// Restore is the way back: the conversation becomes visible to the pickers and
// writable again, without moving.
func TestRestoreMakesAnArchivedConversationWritableAgain(t *testing.T) {
	application := newTestApp(t, newSession(t), faux.New(faux.Response{Text: "answer"}))
	seedConversation(t, application)
	archivedFile := application.Session().File()
	if err := application.Archive(); err != nil {
		t.Fatalf("archive: %v", err)
	}
	// Switching to an archived session is how /recover lands on one: the guard
	// is about leaving an archived conversation, not entering it.
	if err := application.SwitchSession(archivedFile); err != nil {
		t.Fatalf("recover: %v", err)
	}
	if !application.Archived() {
		t.Fatal("the recovered conversation does not report itself archived")
	}

	if err := application.Restore(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if application.Archived() {
		t.Fatal("the conversation is still archived after a restore")
	}
	if got := application.Session().File(); got != archivedFile {
		t.Fatalf("file = %q, want to stay at %q", got, archivedFile)
	}
	// A restored conversation is a live one: asking again must append to the
	// same file.
	if _, err := application.Ask(context.Background(), wire.UserMessage("again"), nil); err != nil {
		t.Fatalf("ask after restore: %v", err)
	}
	info, err := session.ReadInfo(archivedFile)
	if err != nil || info == nil {
		t.Fatalf("read info: %+v, %v", info, err)
	}
	if info.MessageCount != 4 {
		t.Fatalf("message count = %d, want the restored conversation to keep growing", info.MessageCount)
	}
}

func TestRestoreAndDeleteRefuseALiveConversation(t *testing.T) {
	application := newTestApp(t, newSession(t), faux.New(faux.Response{Text: "answer"}))
	seedConversation(t, application)
	file := application.Session().File()

	if err := application.Restore(); !errors.Is(err, ErrNotArchived) {
		t.Fatalf("restore of a live session = %v, want ErrNotArchived", err)
	}
	// Deletion is refused on purpose: the archive is the second look before a
	// transcript is destroyed.
	if err := application.Delete(); !errors.Is(err, ErrNotArchived) {
		t.Fatalf("delete of a live session = %v, want ErrNotArchived", err)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("the live session file is gone: %v", err)
	}
}

func TestDeleteRemovesAnArchivedConversationAndStartsANewOne(t *testing.T) {
	application := newTestApp(t, newSession(t), faux.New(faux.Response{Text: "answer"}))
	seedConversation(t, application)
	archivedID := application.Session().ID()
	archivedFile := application.Session().File()
	if err := application.Archive(); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if err := application.SwitchSession(archivedFile); err != nil {
		t.Fatalf("recover: %v", err)
	}

	if err := application.Delete(); err != nil {
		t.Fatalf("delete: %v", err)
	}
	for _, gone := range []string{archivedFile, session.ArchiveMarker(archivedFile)} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Fatalf("%s survived the delete: %v", gone, err)
		}
	}
	if application.Session().ID() == archivedID {
		t.Fatal("deleting left the prompt on the deleted conversation")
	}
	if application.Archived() {
		t.Fatal("the fresh conversation reports itself archived")
	}
}

// Every write goes through the same barrier. The command layer already refuses
// these while on an archived conversation, but the app is what keeps the file
// safe from a caller that does not go through commands.
func TestAnArchivedConversationRefusesEveryWrite(t *testing.T) {
	application := newTestApp(t, newSession(t), faux.New(faux.Response{Text: "answer"}), func(c *Config) {
		// SetModel refuses a config without a resolver before it reaches any
		// session state, so the test needs one to observe the barrier at all.
		c.Resolver = serveAll(faux.New())
	})
	seedConversation(t, application)
	file := application.Session().File()
	entryID := application.Session().Entries()[0].ID
	target := otherSession(t, application)
	if err := application.Archive(); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if err := application.SwitchSession(file); err != nil {
		t.Fatalf("recover: %v", err)
	}
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	writes := []struct {
		name string
		run  func() error
	}{
		{"ask", func() error {
			_, err := application.Ask(context.Background(), wire.UserMessage("more"), nil)
			return err
		}},
		{"compact", func() error {
			_, err := application.Compact(context.Background(), "", nil)
			return err
		}},
		{"fork", func() error {
			_, err := application.ForkSession()
			return err
		}},
		{"fork at", func() error {
			_, err := application.ForkAt(entryID)
			return err
		}},
		{"clone", func() error {
			_, err := application.CloneSession()
			return err
		}},
		{"branch", func() error { return application.Branch(entryID) }},
		{"rename", func() error { return application.SetName("renamed") }},
		{"set model", func() error {
			_, err := application.SetModel("faux", "another")
			return err
		}},
		{"new", func() error { return application.NewSession() }},
		{"switch", func() error { return application.SwitchSession(target) }},
	}
	for _, write := range writes {
		t.Run(write.name, func(t *testing.T) {
			if err := write.run(); !errors.Is(err, ErrArchived) {
				t.Fatalf("%s on an archived conversation = %v, want ErrArchived", write.name, err)
			}
			if got := application.Session().File(); got != file {
				t.Fatalf("%s moved the conversation to %q", write.name, got)
			}
		})
	}

	after, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read after the refused writes: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("a refused write reached the archived file:\nbefore %q\nafter  %q", before, after)
	}
	if _, err := os.Stat(filepath.Clean(file)); err != nil {
		t.Fatalf("the archived file is gone: %v", err)
	}
}
