package webhost

import (
	"context"
	"testing"

	"pi-go/internal/app"
	"pi-go/internal/assembly"
	"pi-go/internal/provider/faux"
	"pi-go/internal/session"
	"pi-go/internal/wire"
)

// forkKernel builds the smallest kernel a fork needs: one conversation with one
// answer in it, so the branch has something to copy. The faux provider answers
// without a network, which is what makes this a unit test rather than a probe.
func forkKernel(t *testing.T) (*Host, *session.Manager) {
	t.Helper()
	manager, err := session.Create(t.TempDir(), t.TempDir(), session.Options{})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	kernel, err := app.New(app.Config{
		Model:        wire.Model{ID: "faux-1", Name: "Faux One", API: "faux", Provider: "faux"},
		Provider:     faux.New(faux.Response{Text: "answer"}),
		SystemPrompt: app.StaticPrompt("base prompt"),
	}, manager)
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	if _, err := kernel.Ask(context.Background(), wire.UserMessage("hi"), nil); err != nil {
		t.Fatalf("ask: %v", err)
	}
	return New(&assembly.Kernel{App: kernel, Manager: manager}), manager
}

// forkKernelTwice adds a second prompt, which is what a choice of fork points
// needs: with one message there is nothing to choose between.
func forkKernelTwice(t *testing.T) (*Host, *session.Manager) {
	t.Helper()
	host, manager := forkKernel(t)
	if _, err := host.kernel.App.Ask(context.Background(), wire.UserMessage("second"), nil); err != nil {
		t.Fatalf("ask again: %v", err)
	}
	return host, manager
}

// TestForkPointsStartWithTheEndOfTheConversation: the end is a row like any
// other, and it is the one that carries no entry id — that empty string is how
// the route spells "the whole conversation".
func TestForkPointsStartWithTheEndOfTheConversation(t *testing.T) {
	host, _ := forkKernelTwice(t)
	points := host.ForkPoints()

	if len(points) != 3 {
		t.Fatalf("points = %d, want the end plus two prompts", len(points))
	}
	if points[0].Index != 0 || points[0].EntryID != "" || points[0].Text != "" {
		t.Fatalf("first point = %+v, want the end of the conversation", points[0])
	}
	if points[1].Index != 1 || points[1].Text != "hi" {
		t.Fatalf("second point = %+v, want the first prompt", points[1])
	}
	if points[2].Index != 2 || points[2].Text != "second" {
		t.Fatalf("third point = %+v, want the second prompt", points[2])
	}
	if points[2].EntryID == "" {
		t.Fatal("a prompt point carries no entry id")
	}
}

// TestForkSessionCopiesAndNames is the whole reason this adapter has the method:
// the copy is the kernel's, the name is the reader's, and both have to land
// before the page is told the fork happened.
func TestForkSessionCopiesAndNames(t *testing.T) {
	host, manager := forkKernel(t)
	original := manager.ID()

	text, err := host.ForkSession("", "plan B")
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	if text != "" {
		t.Fatalf("draft = %q, want nothing left behind at the end of a conversation", text)
	}
	if manager.ID() == original {
		t.Fatalf("the kernel is still on the original session %q", original)
	}
	if got := manager.Name(); got != "plan B" {
		t.Fatalf("name = %q, want %q", got, "plan B")
	}
	if got := manager.Header().ParentSession; got == "" {
		t.Fatal("the fork did not record the session it came from")
	}
}

// TestForkSessionBeforeAMessageReturnsItAsDraft is the half the window did not
// have: branching in front of a prompt, which keeps the history before it and
// hands the prompt back so the reader can say it differently.
func TestForkSessionBeforeAMessageReturnsItAsDraft(t *testing.T) {
	host, manager := forkKernelTwice(t)
	points := host.ForkPoints()
	second := points[2]

	text, err := host.ForkSession(second.EntryID, "plan B")
	if err != nil {
		t.Fatalf("fork before: %v", err)
	}
	if text != "second" {
		t.Fatalf("draft = %q, want the message that was branched off", text)
	}
	// What the branch holds is the first prompt and the answer to it; the
	// entries behind those are bookkeeping (the name, the usage) and are not
	// asserted, because counting them here would make this a test about the
	// session format rather than about where the cut landed.
	kept := host.ForkPoints()
	if len(kept) != 2 {
		t.Fatalf("the fork offers %d points, want the end plus the first prompt", len(kept))
	}
	if kept[1].Text != "hi" {
		t.Fatalf("the fork kept %q, want the prompt before the cut", kept[1].Text)
	}
	if got := manager.Name(); got != "plan B" {
		t.Fatalf("name = %q, want %q", got, "plan B")
	}
}

// TestForkSessionKeepsTheNameItCopied covers the empty name: the page sends
// nothing when the reader does not care, and the copy keeps the name the branch
// carried rather than arriving blank.
func TestForkSessionKeepsTheNameItCopied(t *testing.T) {
	host, manager := forkKernel(t)
	if _, err := manager.AppendSessionInfo("inherited"); err != nil {
		t.Fatalf("name the original: %v", err)
	}

	if _, err := host.ForkSession("", "   "); err != nil {
		t.Fatalf("fork: %v", err)
	}
	if got := manager.Name(); got != "inherited" {
		t.Fatalf("name = %q, want the copied %q", got, "inherited")
	}
}

// TestForkSessionRefusesAnEmptyConversation keeps the kernel's refusal on the
// way out: forking costs a file and an id, so an empty session has nothing to
// branch from and the reader is told so.
func TestForkSessionRefusesAnEmptyConversation(t *testing.T) {
	manager, err := session.Create(t.TempDir(), t.TempDir(), session.Options{})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	kernel, err := app.New(app.Config{
		Model:        wire.Model{ID: "faux-1", API: "faux", Provider: "faux"},
		Provider:     faux.New(faux.Response{Text: "answer"}),
		SystemPrompt: app.StaticPrompt("base prompt"),
	}, manager)
	if err != nil {
		t.Fatalf("new app: %v", err)
	}

	host := New(&assembly.Kernel{App: kernel, Manager: manager})
	if _, err := host.ForkSession("", "plan B"); err == nil {
		t.Fatal("an empty conversation was forked")
	}
	if points := host.ForkPoints(); len(points) != 1 || points[0].Index != 0 {
		t.Fatalf("points = %+v, want only the end of an empty conversation", points)
	}
}
