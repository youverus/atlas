package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"pi-go/internal/app"
	"pi-go/internal/provider/faux"
	"pi-go/internal/session"
	"pi-go/internal/wire"
)

// archiveSetup builds a conversation with one exchange and archives it, which
// leaves the app on a fresh session and the file in the archived half.
func archiveSetup(t *testing.T) (*app.App, *sessionPicker, string, string) {
	t.Helper()
	cwd, dir := t.TempDir(), t.TempDir()
	manager := sessionAt(t, cwd, dir)
	application := commandApp(t, manager, cwd, faux.Response{Text: "answer"})
	if _, err := application.Ask(context.Background(), wire.UserMessage("hello"), nil); err != nil {
		t.Fatalf("ask: %v", err)
	}
	if err := application.SetName("alpha"); err != nil {
		t.Fatalf("set name: %v", err)
	}
	id, file := manager.ID(), manager.File()

	out, err := runCommand(t, application, "/archive")
	if err != nil {
		t.Fatalf("/archive: %v", err)
	}
	if !strings.Contains(out, "archived: alpha") {
		t.Fatalf("output = %q, want the archived session named", out)
	}
	if !strings.Contains(out, "hidden from /resume; /recover opens it again") {
		t.Fatalf("output = %q, want the way to find it again", out)
	}
	if application.Session().ID() == id {
		t.Fatal("/archive left the prompt on the archived conversation")
	}
	return application, newSessionPicker(application, ""), id, file
}

// Archiving is the difference between the two lists: /resume stops showing the
// session, /recover shows it, and the file stays where it was.
func TestArchiveHidesTheSessionFromResumeAndShowsItToRecover(t *testing.T) {
	_, resume, _, file := archiveSetup(t)

	if _, err := os.Stat(file); err != nil {
		t.Fatalf("the archived file is gone: %v", err)
	}

	out := &strings.Builder{}
	if err := resume.table(false, out); err != nil {
		t.Fatalf("list: %v", err)
	}
	if got := out.String(); strings.Contains(got, "alpha") {
		t.Fatalf("/resume still shows the archived session: %q", got)
	}

	recovered := newRecoverPicker(resume)
	if recovered == nil {
		t.Fatal("no recover picker")
	}
	out.Reset()
	if err := recovered.table(false, out); err != nil {
		t.Fatalf("list archived: %v", err)
	}
	text := out.String()
	if !strings.Contains(text, "alpha") {
		t.Fatalf("/recover = %q, want the archived session", text)
	}
	if !strings.Contains(text, "recover one with /recover <id prefix>") {
		t.Fatalf("/recover = %q, want its own footer", text)
	}
	if strings.Contains(text, "resume one with") {
		t.Fatalf("/recover = %q, want no resume wording", text)
	}

	// /recover has the wide form too: archiving a session in one project and
	// looking for it from another is the same question as resuming one, and a
	// flag that only half the store answers would send the reader to /resume.
	out.Reset()
	if err := recovered.table(true, out); err != nil {
		t.Fatalf("list every archived session: %v", err)
	}
	wide := out.String()
	if !strings.Contains(wide, "alpha") {
		t.Fatalf("/recover --all = %q, want the archived session", wide)
	}
	if !strings.Contains(wide, "recover one with /recover --all <id prefix>") {
		t.Fatalf("/recover --all = %q, want the wide footer", wide)
	}
}

// An id prefix opens an archived session through /recover and not through
// /resume, and the session that opens takes no prompts until it is restored.
func TestRecoverOpensAnArchivedSessionReadOnly(t *testing.T) {
	application, _, id, file := archiveSetup(t)

	// The same prefix must not find it under /resume: a session the user put
	// away is not one of the conversation this command continues.
	if _, err := runCommand(t, application, "/resume "+id); err == nil || !strings.Contains(err.Error(), "no session matches") {
		t.Fatalf("err = %v, want /resume to miss the archived session", err)
	}

	out, err := runCommand(t, application, "/recover "+id)
	if err != nil {
		t.Fatalf("/recover: %v", err)
	}
	if !strings.Contains(out, "recovered: alpha") {
		t.Fatalf("output = %q, want the recovery reported in its own verb", out)
	}
	if !strings.Contains(out, "file: "+file) {
		t.Fatalf("output = %q, want the file named", out)
	}
	if !strings.Contains(out, archivedLocked) {
		t.Fatalf("output = %q, want the read-only note", out)
	}
	if !application.Archived() {
		t.Fatal("the recovered session is not archived")
	}

	// Everything that would write is refused, with the way out in the error.
	for _, line := range []string{"/new", "/compact", "/fork", "/rename beta", "/model faux-1"} {
		if _, err := runCommand(t, application, line); !errors.Is(err, app.ErrArchived) {
			t.Fatalf("%s on an archived session = %v, want ErrArchived", line, err)
		}
	}
	// So is a switch: leaving halfway is the same as abandoning the record.
	live := sessionAt(t, application.Session().Cwd(), application.Session().Dir())
	if _, err := live.AppendMessage(wire.UserMessage("live")); err != nil {
		t.Fatalf("append: %v", err)
	}
	if _, err := runCommand(t, application, "/resume "+live.File()); !errors.Is(err, app.ErrArchived) {
		t.Fatalf("switch out of an archived session = %v, want ErrArchived", err)
	}
	// A prompt is not a command, and the prompt handler is another door onto
	// the same file.
	if _, err := application.Ask(context.Background(), wire.UserMessage("more"), nil); !errors.Is(err, app.ErrArchived) {
		t.Fatalf("ask = %v, want ErrArchived", err)
	}

	// /help still runs, and says which commands do.
	out, err = runCommand(t, application, "/help")
	if err != nil {
		t.Fatalf("/help: %v", err)
	}
	if !strings.Contains(out, archivedHelp) {
		t.Fatalf("/help = %q, want the archived note", out)
	}
}

// /restore is the way back, and the conversation keeps its place and its
// content.
func TestRestorePutsTheSessionBackInResume(t *testing.T) {
	application, resume, id, file := archiveSetup(t)
	if _, err := runCommand(t, application, "/recover "+id); err != nil {
		t.Fatalf("/recover: %v", err)
	}

	out, err := runCommand(t, application, "/restore")
	if err != nil {
		t.Fatalf("/restore: %v", err)
	}
	if !strings.Contains(out, "restored: alpha") {
		t.Fatalf("output = %q, want the restore reported", out)
	}
	if application.Archived() {
		t.Fatal("the session is still archived")
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("the restored file is gone: %v", err)
	}

	listed := &strings.Builder{}
	if err := resume.table(false, listed); err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(listed.String(), "alpha") {
		t.Fatalf("/resume = %q, want the restored session", listed.String())
	}
	// A restored session takes prompts again, which is the whole promise.
	if _, err := application.Ask(context.Background(), wire.UserMessage("again"), nil); err != nil {
		t.Fatalf("ask after restore: %v", err)
	}

	// Restoring a live conversation is refused, and says why.
	if _, err := runCommand(t, application, "/restore"); !errors.Is(err, app.ErrNotArchived) {
		t.Fatalf("restore of a live session = %v, want ErrNotArchived", err)
	}
}

// /delete is the only command in atlas that destroys a transcript, so it is
// reachable only from an archived one and reports the file it removed.
func TestDeleteRemovesAnArchivedSession(t *testing.T) {
	application, resume, id, file := archiveSetup(t)

	// A live conversation cannot be deleted: the archive is the second look.
	if _, err := runCommand(t, application, "/delete"); !errors.Is(err, app.ErrNotArchived) {
		t.Fatalf("/delete of a live session = %v, want ErrNotArchived", err)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("the live file is gone after a refused delete: %v", err)
	}

	if _, err := runCommand(t, application, "/recover "+id); err != nil {
		t.Fatalf("/recover: %v", err)
	}
	out, err := runCommand(t, application, "/delete")
	if err != nil {
		t.Fatalf("/delete: %v", err)
	}
	if !strings.Contains(out, "deleted: alpha") {
		t.Fatalf("output = %q, want the deletion reported", out)
	}
	if !strings.Contains(out, file+" (removed)") {
		t.Fatalf("output = %q, want the receipt to name the file", out)
	}
	for _, gone := range []string{file, session.ArchiveMarker(file)} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Fatalf("%s survived the delete: %v", gone, err)
		}
	}
	if application.Session().ID() == id {
		t.Fatal("deleting left the prompt on the deleted conversation")
	}
	if application.Archived() {
		t.Fatal("the fresh conversation reports itself archived")
	}

	// The session is gone from both halves of the picker.
	for _, picker := range []*sessionPicker{resume, newRecoverPicker(resume)} {
		listOut := &strings.Builder{}
		if err := picker.table(false, listOut); err != nil {
			t.Fatalf("list: %v", err)
		}
		if strings.Contains(listOut.String(), "alpha") {
			t.Fatalf("%s still lists the deleted session: %q", picker.trigger(), listOut.String())
		}
	}
}

// The palette has to agree with the dispatcher: a suggestion the dispatcher
// then refuses is worse than no suggestion.
func TestThePaletteShrinksInAnArchivedSession(t *testing.T) {
	application, _, id, _ := archiveSetup(t)
	if _, err := runCommand(t, application, "/recover "+id); err != nil {
		t.Fatalf("/recover: %v", err)
	}
	if !application.Archived() {
		t.Fatal("the recovered session is not archived")
	}

	picks := suggestionsFor("/", nil, true)
	if len(picks) != len(archivedCommands) {
		t.Fatalf("suggestions = %d, want the %d commands that run here", len(picks), len(archivedCommands))
	}
	for _, pick := range picks {
		if !archivedCommands[pick.Name] {
			t.Fatalf("%q is offered in an archived session but does not run there", pick.Name)
		}
	}
	for _, name := range []string{"/restore", "/delete"} {
		found := false
		for _, pick := range picks {
			if pick.Name == name {
				found = true
			}
		}
		if !found {
			t.Fatalf("%q is missing from the palette of an archived session", name)
		}
	}
	// Every command atlas has stays in the table: /help is the one place that
	// explains why the others were dropped.
	text := helpText()
	for _, c := range commands {
		if !strings.Contains(text, c.name) {
			t.Fatalf("/help does not mention %s", c.name)
		}
	}
	if !strings.Contains(helpText(), "/archive") {
		t.Fatal("/help does not mention /archive")
	}
}

// Archiving a conversation nobody talked to has nothing to hide, and the
// command has to say that instead of moving the prompt on.
func TestArchiveOfAnEmptySessionSaysSo(t *testing.T) {
	cwd, dir := t.TempDir(), t.TempDir()
	application := commandApp(t, sessionAt(t, cwd, dir), cwd, faux.Response{Text: "answer"})
	before := application.Session().ID()

	_, err := runCommand(t, application, "/archive")
	if !errors.Is(err, app.ErrNothingToArchive) {
		t.Fatalf("err = %v, want ErrNothingToArchive", err)
	}
	if application.Session().ID() != before {
		t.Fatal("a refused archive moved the prompt")
	}
}
