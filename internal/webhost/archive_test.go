package webhost

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"pi-go/internal/app"
	"pi-go/internal/assembly"
	"pi-go/internal/provider/faux"
	"pi-go/internal/session"
	"pi-go/internal/wire"
)

func sessionHost(t *testing.T, answers ...string) (*Host, string, string) {
	t.Helper()
	if len(answers) == 0 {
		answers = []string{"answer"}
	}
	responses := make([]faux.Response, 0, len(answers))
	for _, text := range answers {
		responses = append(responses, faux.Response{Text: text})
	}
	cwd := t.TempDir()
	storeRoot := t.TempDir()
	sessionDir := func(dir string) (string, error) {
		return filepath.Join(storeRoot, filepath.Base(dir)), nil
	}
	dir, err := sessionDir(cwd)
	if err != nil {
		t.Fatalf("sessionDir: %v", err)
	}
	manager, err := session.Create(cwd, dir, session.Options{})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	kernel, err := app.New(app.Config{
		Model:        wire.Model{ID: "faux-1", Name: "Faux One", API: "faux", Provider: "faux"},
		Provider:     faux.New(responses...),
		SystemPrompt: app.StaticPrompt("base prompt"),
		SessionDir:   sessionDir,
	}, manager)
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	if _, err := kernel.Ask(context.Background(), wire.UserMessage("hello"), nil); err != nil {
		t.Fatalf("seed ask: %v", err)
	}
	projectsFile := filepath.Join(t.TempDir(), "projects.json")
	host := NewWith(&assembly.Kernel{App: kernel, Manager: manager, Cwd: cwd}, Options{Projects: projectsFile})
	return host, cwd, dir
}

func seedExtraSession(t *testing.T, cwd, dir, prompt string) string {
	t.Helper()
	other, err := session.Create(cwd, dir, session.Options{})
	if err != nil {
		t.Fatalf("create extra session: %v", err)
	}
	kernel, err := app.New(app.Config{
		Model:        wire.Model{ID: "faux-1", Name: "Faux One", API: "faux", Provider: "faux"},
		Provider:     faux.New(faux.Response{Text: "reply"}),
		SystemPrompt: app.StaticPrompt("base prompt"),
	}, other)
	if err != nil {
		t.Fatalf("new extra app: %v", err)
	}
	if _, err := kernel.Ask(context.Background(), wire.UserMessage(prompt), nil); err != nil {
		t.Fatalf("extra ask: %v", err)
	}
	return other.File()
}

// Archiving the currently open session must move the kernel off it onto a fresh
// conversation so that deleting it afterwards from the archive succeeds rather
// than failing with "refusing to delete the open session".
func TestArchiveCurrentSessionMovesOffItSoItCanBeDeleted(t *testing.T) {
	host, _, _ := sessionHost(t)
	first := host.kernel.App.Session().File()

	if err := host.ArchiveSession(first); err != nil {
		t.Fatalf("archive current session: %v", err)
	}
	if host.SessionArchived() {
		t.Fatal("kernel stayed parked on the archived session")
	}
	if sameSessionFile(host.kernel.App.Session().File(), first) {
		t.Fatalf("session file stayed %q after archiving", first)
	}

	activeList, err := host.Projects(false)
	if err != nil {
		t.Fatalf("projects(false): %v", err)
	}
	if len(activeList.Groups) != 1 || len(activeList.Groups[0].Sessions) != 0 {
		t.Fatalf("active sessions = %+v, want none", activeList)
	}

	archivedList, err := host.Projects(true)
	if err != nil {
		t.Fatalf("projects(true): %v", err)
	}
	if len(archivedList.Groups) != 1 || len(archivedList.Groups[0].Sessions) != 1 || archivedList.Groups[0].Sessions[0].Path != first {
		t.Fatalf("archived sessions = %+v, want [%s]", archivedList, first)
	}

	if err := host.DeleteSession(first); err != nil {
		t.Fatalf("delete archived session: %v", err)
	}
	for _, gone := range []string{first, session.ArchiveMarker(first)} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Fatalf("%s survived DeleteSession: %v", gone, err)
		}
	}
}

// Open (unarchived) sessions cannot be deleted directly: deletion requires the
// session to be archived first.
func TestDeleteSessionRefusesUnarchivedSessions(t *testing.T) {
	host, cwd, dir := sessionHost(t)
	current := host.kernel.App.Session().File()
	other := seedExtraSession(t, cwd, dir, "second")

	if err := host.DeleteSession(current); err == nil {
		t.Fatal("deleting open current session succeeded")
	}
	if err := host.DeleteSession(other); err == nil {
		t.Fatal("deleting unarchived non-current session succeeded")
	}
	for _, file := range []string{current, other} {
		if _, err := os.Stat(file); err != nil {
			t.Fatalf("%s vanished after refused delete: %v", file, err)
		}
	}
}

// An archived session opened in the desktop viewer can be read, left for another
// or a new session, restored, or deleted directly while open, while rejecting
// new messages.
func TestOpenArchivedSessionCanBeViewedLeftRestoredAndDeleted(t *testing.T) {
	host, cwd, dir := sessionHost(t, "answer", "after-restore")
	archived := host.kernel.App.Session().File()
	live := seedExtraSession(t, cwd, dir, "live session")
	otherProject := t.TempDir()
	if err := host.AddProject(otherProject); err != nil {
		t.Fatalf("add project: %v", err)
	}

	if err := host.ArchiveSession(archived); err != nil {
		t.Fatalf("archive: %v", err)
	}

	// Open the archived session for viewing.
	if err := host.OpenSession(archived); err != nil {
		t.Fatalf("open archived: %v", err)
	}
	if !host.SessionArchived() {
		t.Fatal("SessionArchived() = false while viewing archived session")
	}
	blocks, err := host.Transcript()
	if err != nil {
		t.Fatalf("transcript: %v", err)
	}
	if len(blocks) == 0 {
		t.Fatal("expected archived transcript blocks")
	}
	if _, err := host.kernel.App.Ask(context.Background(), wire.UserMessage("more"), nil); !errors.Is(err, app.ErrArchived) {
		t.Fatalf("ask on archived = %v, want ErrArchived", err)
	}

	// Can switch back to another session or start a new session.
	if err := host.OpenSession(live); err != nil {
		t.Fatalf("switch out of archived to live: %v", err)
	}
	if host.SessionArchived() {
		t.Fatal("SessionArchived() = true after switching to live session")
	}

	if err := host.OpenSession(archived); err != nil {
		t.Fatalf("re-open archived: %v", err)
	}
	if err := host.NewSession(); err != nil {
		t.Fatalf("new session while viewing archived: %v", err)
	}
	if host.SessionArchived() {
		t.Fatal("SessionArchived() = true after NewSession")
	}

	if err := host.OpenSession(archived); err != nil {
		t.Fatalf("re-open archived: %v", err)
	}
	if err := host.NewSessionIn(otherProject); err != nil {
		t.Fatalf("new session in project while viewing archived: %v", err)
	}
	if host.SessionArchived() {
		t.Fatal("SessionArchived() = true after NewSessionIn")
	}
	if got := host.SessionDirectory(); got != otherProject {
		t.Fatalf("SessionDirectory() = %q, want %q", got, otherProject)
	}

	// Can restore while viewing it, making it writable again.
	if err := host.OpenSession(archived); err != nil {
		t.Fatalf("re-open archived for restore: %v", err)
	}
	if err := host.RestoreSession(archived); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if host.SessionArchived() {
		t.Fatal("SessionArchived() = true after restore")
	}
	if _, err := host.kernel.App.Ask(context.Background(), wire.UserMessage("follow-up"), nil); err != nil {
		t.Fatalf("ask after restore: %v", err)
	}

	// Can also delete an archived session directly while viewing it.
	if err := host.ArchiveSession(archived); err != nil {
		t.Fatalf("re-archive: %v", err)
	}
	if err := host.OpenSession(archived); err != nil {
		t.Fatalf("re-open archived for delete: %v", err)
	}
	if err := host.DeleteSession(archived); err != nil {
		t.Fatalf("delete while viewing archived: %v", err)
	}
	if host.SessionArchived() {
		t.Fatal("SessionArchived() = true after deleting open archived session")
	}
	if sameSessionFile(host.kernel.App.Session().File(), archived) {
		t.Fatal("kernel still points to deleted session file")
	}
	for _, gone := range []string{archived, session.ArchiveMarker(archived)} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Fatalf("%s survived delete: %v", gone, err)
		}
	}
}
