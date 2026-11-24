package main

// A fork point is somewhere in the conversation, and the whole command is about
// naming one: the menu, the printed table and the number after the name are
// three ways to say the same thing. These tests are about all three, and about
// the point being an answer rather than a prompt: a branch continues from what
// the agent just said.

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"pi-go/internal/app"
	"pi-go/internal/provider/faux"
	"pi-go/internal/session"
	"pi-go/internal/tui"
	"pi-go/internal/wire"
)

// forkable is a session with two answered prompts in it, which is the smallest
// conversation that has a middle to fork at.
func forkable(t *testing.T) (*app.App, *session.Manager) {
	t.Helper()
	cwd := t.TempDir()
	manager, err := session.Create(cwd, t.TempDir(), session.Options{})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	application := commandApp(t, manager, cwd,
		faux.Response{Text: "first answer"}, faux.Response{Text: "second answer"})
	for _, prompt := range []string{"first question", "second question"} {
		if _, err := application.Ask(context.Background(), wire.UserMessage(prompt), nil); err != nil {
			t.Fatalf("ask %q: %v", prompt, err)
		}
	}
	return application, manager
}

func TestForkWithNoMenuPrintsThePoints(t *testing.T) {
	application, _ := forkable(t)
	out := &bytes.Buffer{}
	if err := forkCommand(application, "", nil, out); err != nil {
		t.Fatalf("ForkCommand: %v", err)
	}
	printed := out.String()
	for _, want := range []string{
		"0  the whole conversation",
		"1  first answer",
		"2  second answer",
		"fork at one of them with /fork <number>",
	} {
		if !strings.Contains(printed, want) {
			t.Fatalf("the table is missing %q:\n%s", want, printed)
		}
	}
}

// TestForkNumberBranchesAfterThatAnswer: the number names an answer, and the new
// session holds every message up to and including it.
func TestForkNumberBranchesAfterThatAnswer(t *testing.T) {
	application, manager := forkable(t)
	oldID := manager.ID()

	out := &bytes.Buffer{}
	if err := forkCommand(application, "1", nil, out); err != nil {
		t.Fatalf("ForkCommand: %v", err)
	}
	if manager.ID() == oldID {
		t.Fatal("/fork 1 kept the session id")
	}
	if got := application.MessageCount(); got != 2 {
		t.Fatalf("the fork holds %d messages, want the first turn (prompt and answer)", got)
	}
	if got := application.Messages()[1].Text(); got != "first answer" {
		t.Fatalf("the fork ends with %q, want the chosen answer", got)
	}
	if !strings.Contains(out.String(), "fork: "+manager.ID()) {
		t.Fatalf("output = %q, want the new session id", out.String())
	}
}

// TestForkZeroTakesTheWholeConversation: the end of the conversation is a point
// like any other, so it needs no spelling of its own.
func TestForkZeroTakesTheWholeConversation(t *testing.T) {
	application, _ := forkable(t)
	if err := forkCommand(application, "0", nil, &bytes.Buffer{}); err != nil {
		t.Fatalf("ForkCommand: %v", err)
	}
	if got := application.MessageCount(); got != 4 {
		t.Fatalf("the fork holds %d messages, want the whole conversation", got)
	}
}

// TestForkMenuTakesTheChosenPoint: the menu is the same list the table prints,
// and the row the reader takes is the point.
func TestForkMenuTakesTheChosenPoint(t *testing.T) {
	application, _ := forkable(t)
	points := application.ForkPoints()
	if len(points) != 2 {
		t.Fatalf("the session reports %d answers, want two", len(points))
	}
	var asked tui.PickRequest
	taken := chooser(func(req tui.PickRequest) tui.PickAnswer {
		asked = req
		return tui.PickAnswer{Taken: true, Value: points[0].EntryID}
	})

	if err := forkCommand(application, "", taken, &bytes.Buffer{}); err != nil {
		t.Fatalf("ForkCommand: %v", err)
	}
	if asked.Title != "fork after" {
		t.Fatalf("the menu is titled %q", asked.Title)
	}
	if len(asked.Items) != 3 {
		t.Fatalf("the menu offers %d rows, want the whole conversation and two answers", len(asked.Items))
	}
	if got := application.MessageCount(); got != 2 {
		t.Fatalf("the fork holds %d messages, want the first turn only", got)
	}
	if got := application.Messages()[1].Text(); got != "first answer" {
		t.Fatalf("the fork ends with %q, want the chosen answer", got)
	}
}

// TestAClosedMenuLeavesTheSession: Esc is the reader deciding not to say it
// differently after all, and a fork is a new file.
func TestAClosedMenuLeavesTheSession(t *testing.T) {
	application, manager := forkable(t)
	oldID := manager.ID()
	declined := chooser(func(tui.PickRequest) tui.PickAnswer { return tui.PickAnswer{} })

	out := &bytes.Buffer{}
	if err := forkCommand(application, "", declined, out); err != nil {
		t.Fatalf("ForkCommand: %v", err)
	}
	if manager.ID() != oldID {
		t.Fatal("a closed menu forked the session anyway")
	}
	if out.Len() != 0 {
		t.Fatalf("a closed menu said %q", out.String())
	}
}

// TestForkRefusesAPointThatIsNotThere: a number out of range names nothing, and
// the answer says how to find the numbers rather than forking the wrong point.
func TestForkRefusesAPointThatIsNotThere(t *testing.T) {
	application, manager := forkable(t)
	oldID := manager.ID()
	for _, arg := range []string{"9", "-1", "two"} {
		err := forkCommand(application, arg, nil, &bytes.Buffer{})
		if err == nil {
			t.Fatalf("/fork %s was accepted", arg)
		}
		if !strings.Contains(err.Error(), "/fork lists them") {
			t.Fatalf("/fork %s said %q", arg, err)
		}
	}
	if manager.ID() != oldID {
		t.Fatal("a refused number forked the session anyway")
	}
}

// TestForkOfNothingSaysSo: an empty session has one point — the end of it — and
// forking there would only rename the file the reader is looking at.
func TestForkOfNothingSaysSo(t *testing.T) {
	cwd := t.TempDir()
	manager, err := session.Create(cwd, t.TempDir(), session.Options{})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	application := commandApp(t, manager, cwd)
	err = forkCommand(application, "", nil, &bytes.Buffer{})
	if err == nil {
		t.Fatal("forking an empty session was allowed")
	}
	if !strings.Contains(err.Error(), "nothing to fork") {
		t.Fatalf("the answer is %q", err)
	}
}
