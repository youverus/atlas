package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pi-go/internal/provider/faux"
	"pi-go/internal/session"
	"pi-go/internal/tool"
	"pi-go/internal/wire"
)

// newSessionAt creates a session for one project directory. A switch moves
// between real files, so the tests create them where a picker would list them:
// one cwd, one session directory.
func newSessionAt(t *testing.T, cwd, dir string) *session.Manager {
	t.Helper()
	manager, err := session.Create(cwd, dir, session.Options{})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return manager
}

// seedTarget gives a target session one exchange. Lazy creation leaves an
// entry-less session without a file on disk, and a switch between files needs
// the file.
func seedTarget(t *testing.T, manager *session.Manager) {
	t.Helper()
	application := newTestApp(t, manager, faux.New(faux.Response{Text: "seed"}))
	if _, err := application.Ask(context.Background(), wire.UserMessage("seed"), nil); err != nil {
		t.Fatalf("seed ask: %v", err)
	}
}

// /new abandons the conversation, and one the user never talked to leaves
// nothing behind: the file appears with the first message, not with the
// abandonment. Writing a header-only file here is what filled the session
// directory with empty sessions on every restart.
func TestNewSessionLeavesNoFileForAnEmptyConversation(t *testing.T) {
	application := newTestApp(t, newSession(t), faux.New())
	// The manager mutates in place on /new, so the old identity has to be
	// copied out as values first.
	beforeID := application.Session().ID()
	beforeFile := application.Session().File()

	if err := application.NewSession(); err != nil {
		t.Fatalf("new session: %v", err)
	}
	if _, err := os.Stat(beforeFile); !os.IsNotExist(err) {
		t.Fatalf("the abandoned empty conversation has a file at %s", beforeFile)
	}
	if got := application.Session().ID(); got == beforeID {
		t.Fatalf("id = %q, want a fresh one", got)
	}
}

// A conversation the user talked to already has its file, so /new only has to
// leave it alone: the startup session stays reachable through the picker.
func TestNewSessionKeepsATalkedConversationResumable(t *testing.T) {
	application := newTestApp(t, newSession(t), faux.New(faux.Response{Text: "answer"}))
	if _, err := application.Ask(context.Background(), wire.UserMessage("hello"), nil); err != nil {
		t.Fatalf("ask: %v", err)
	}
	beforeID := application.Session().ID()
	beforeFile := application.Session().File()

	if err := application.NewSession(); err != nil {
		t.Fatalf("new session: %v", err)
	}
	reopened, err := session.Open(beforeFile, "", "")
	if err != nil {
		t.Fatalf("open the abandoned session: %v", err)
	}
	if got := reopened.ID(); got != beforeID {
		t.Fatalf("id = %q, want %q", got, beforeID)
	}
	if got := reopened.EntryCount(); got == 0 {
		t.Fatal("the abandoned session lost its entries")
	}
}

// A switch is a jump, not a copy: the conversation continues in the file it
// was recorded in, and the next turn appends there.
func TestSwitchSessionContinuesTheTargetConversation(t *testing.T) {
	cwd, dir := t.TempDir(), t.TempDir()
	target := newSessionAt(t, cwd, dir)
	builder := newTestApp(t, target, faux.New(faux.Response{Text: "answer"}))
	if _, err := builder.Ask(context.Background(), wire.UserMessage("hello"), nil); err != nil {
		t.Fatalf("ask: %v", err)
	}
	targetFile := target.File()
	before, err := os.ReadFile(targetFile)
	if err != nil {
		t.Fatalf("read the target: %v", err)
	}

	current := newSessionAt(t, cwd, dir)
	application := newTestApp(t, current, faux.New(faux.Response{Text: "onward"}), func(c *Config) { c.Resolver = serveAll(faux.New(faux.Response{Text: "onward"})) })
	if err := application.SwitchSession(targetFile); err != nil {
		t.Fatalf("switch: %v", err)
	}
	if got := application.Session().ID(); got != target.ID() {
		t.Fatalf("id = %q, want the target's %q", got, target.ID())
	}
	if got := application.MessageCount(); got != 2 {
		t.Fatalf("messages = %d, want the restored conversation (2)", got)
	}
	if got := application.Messages()[1].Text(); got != "answer" {
		t.Fatalf("last message = %q, want the answer from the target session", got)
	}

	if _, err := application.Ask(context.Background(), wire.UserMessage("onward"), nil); err != nil {
		t.Fatalf("ask: %v", err)
	}
	after, err := os.ReadFile(targetFile)
	if err != nil {
		t.Fatalf("read the target: %v", err)
	}
	if !bytes.Contains(after, []byte("onward")) {
		t.Fatal("the turn after the switch was not written to the resumed session")
	}
	if !bytes.HasPrefix(after, before) {
		t.Fatal("the resumed session's earlier entries changed")
	}
}

// What a session records wins on resume, exactly as it does at startup: the
// model and the thinking level come back from the file, not from whatever the
// abandoned conversation last used.
func TestSwitchSessionRestoresTheKnobs(t *testing.T) {
	cwd, dir := t.TempDir(), t.TempDir()
	target := newSessionAt(t, cwd, dir)
	prov := faux.New(faux.Response{Text: "answer"})
	builder := newTestApp(t, target, prov, func(c *Config) { c.Resolver = serveAll(prov) })
	if _, err := builder.Ask(context.Background(), wire.UserMessage("hi"), nil); err != nil {
		t.Fatalf("ask: %v", err)
	}
	if _, err := builder.SetModel("other", "other-1"); err != nil {
		t.Fatalf("set model: %v", err)
	}
	if err := builder.SetThinkingLevel("high"); err != nil {
		t.Fatalf("set level: %v", err)
	}

	current := newSessionAt(t, cwd, dir)
	application := newTestApp(t, current, prov, func(c *Config) { c.Resolver = serveAll(prov) })
	if err := application.SwitchSession(target.File()); err != nil {
		t.Fatalf("switch: %v", err)
	}
	if model := application.Model(); model.Provider != "other" || model.ID != "other-1" {
		t.Fatalf("model = %s/%s, want other/other-1", model.Provider, model.ID)
	}
	if got := application.ThinkingLevel(); got != "high" {
		t.Fatalf("thinking = %q, want high", got)
	}
}

// The name is part of the conversation, so the switch restores it too.
func TestSwitchSessionRestoresTheName(t *testing.T) {
	cwd, dir := t.TempDir(), t.TempDir()
	target := newSessionAt(t, cwd, dir)
	builder := newTestApp(t, target, faux.New(faux.Response{Text: "answer"}))
	if _, err := builder.Ask(context.Background(), wire.UserMessage("hi"), nil); err != nil {
		t.Fatalf("ask: %v", err)
	}
	if err := builder.SetName("alpha"); err != nil {
		t.Fatalf("set name: %v", err)
	}

	current := newSessionAt(t, cwd, dir)
	application := newTestApp(t, current, faux.New())
	if err := application.SwitchSession(target.File()); err != nil {
		t.Fatalf("switch: %v", err)
	}
	if got := application.Name(); got != "alpha" {
		t.Fatalf("name = %q, want the target's name", got)
	}
}

func TestSwitchSessionWhileBusy(t *testing.T) {
	prov := newGateProvider()
	application := newTestApp(t, newSession(t), prov)

	done := make(chan error, 1)
	go func() {
		_, err := application.Ask(context.Background(), wire.UserMessage("hi"), nil)
		done <- err
	}()
	<-prov.started

	if err := application.SwitchSession("whatever"); !errors.Is(err, ErrBusy) {
		t.Fatalf("err = %v, want ErrBusy", err)
	}

	close(prov.release)
	if err := <-done; err != nil {
		t.Fatalf("ask: %v", err)
	}
}

// A follow-up belongs to the conversation it was typed into: after a switch it
// must not fire into the one that was resumed.
func TestSwitchSessionDropsTheQueuedMessages(t *testing.T) {
	cwd, dir := t.TempDir(), t.TempDir()
	target := newSessionAt(t, cwd, dir)
	seedTarget(t, target)
	current := newSessionAt(t, cwd, dir)
	prov := newGateProvider()
	application := newTestApp(t, current, prov, func(c *Config) { c.Resolver = serveAll(prov) })

	done := make(chan error, 1)
	go func() {
		_, err := application.Ask(context.Background(), wire.UserMessage("hi"), nil)
		done <- err
	}()
	<-prov.started
	application.FollowUp(wire.UserMessage("leftover"))
	close(prov.release)
	if err := <-done; err != nil {
		t.Fatalf("ask: %v", err)
	}

	if err := application.SwitchSession(target.File()); err != nil {
		t.Fatalf("switch: %v", err)
	}
	if _, err := application.Ask(context.Background(), wire.UserMessage("next"), nil); err != nil {
		t.Fatalf("ask: %v", err)
	}
	if got := application.MessageCount(); got != 4 {
		t.Fatalf("messages = %d, want the seeded exchange plus one (4)", got)
	}
	for _, message := range application.Messages() {
		if strings.Contains(message.Text(), "leftover") {
			t.Fatal("the queued message arrived in the resumed conversation")
		}
	}
}

// The side conversation is closed by the switch: its manager belongs to the
// old session's snapshot, and a prompt sent to it afterwards would ask about a
// conversation that is gone.
func TestSwitchSessionClosesTheSide(t *testing.T) {
	cwd, dir := t.TempDir(), t.TempDir()
	target := newSessionAt(t, cwd, dir)
	seedTarget(t, target)
	current := newSessionAt(t, cwd, dir)
	application := newTestApp(t, current, faux.New())
	side, err := application.BeginSide()
	if err != nil {
		t.Fatalf("begin side: %v", err)
	}

	if err := application.SwitchSession(target.File()); err != nil {
		t.Fatalf("switch: %v", err)
	}
	if _, err := side.Ask(context.Background(), wire.UserMessage("q"), nil); !errors.Is(err, ErrSideClosed) {
		t.Fatalf("err = %v, want ErrSideClosed", err)
	}
}

// A path that does not exist must stay an error, not become a fresh session:
// resuming is how a user finds an old conversation, and a typo would silently
// abandon it.
func TestSwitchSessionReportsAMissingFile(t *testing.T) {
	application := newTestApp(t, newSession(t), faux.New())
	before := application.Session().ID()

	err := application.SwitchSession(filepath.Join(application.Session().Dir(), "nope.jsonl"))
	if err == nil || !strings.Contains(err.Error(), "no session file") {
		t.Fatalf("err = %v, want a missing-file error", err)
	}
	if got := application.Session().ID(); got != before {
		t.Fatalf("id = %q, want the failed switch to change nothing (%q)", got, before)
	}
}

// A session recorded in another project opens: what it records is the directory
// its tools ran in, and the app asks for the tools and the instructions of that
// directory rather than of the one the process started in. This is the whole
// point of /resume --all, so both halves are pinned: the set is asked for the
// new directory, and so is the prompt.
func TestSwitchSessionMovesToolsAndPromptToTheOtherProject(t *testing.T) {
	here, there := t.TempDir(), t.TempDir()
	other := newSessionAt(t, there, t.TempDir())
	seedTarget(t, other)

	var asked []string
	application := newTestApp(t, newSessionAt(t, here, t.TempDir()), faux.New(), func(cfg *Config) {
		cfg.Tools = func(cwd string) []tool.Tool {
			asked = append(asked, cwd)
			return nil
		}
		cfg.SystemPrompt = func(cwd string) (string, error) { return "prompt for " + cwd, nil }
	})

	if err := application.SwitchSession(other.File()); err != nil {
		t.Fatalf("switch: %v", err)
	}
	if got := application.Session().ID(); got != other.ID() {
		t.Fatalf("id = %q, want the target's %q", got, other.ID())
	}
	if got := application.Session().Cwd(); got != there {
		t.Fatalf("cwd = %q, want the target's %q", got, there)
	}
	if len(asked) == 0 || asked[len(asked)-1] != there {
		t.Fatalf("the tool set was asked for %v, want the target's directory %q", asked, there)
	}
	if got, want := application.SystemPrompt(), "prompt for "+there; got != want {
		t.Fatalf("prompt = %q, want %q", got, want)
	}
}

// Opening a session from another project is a move, not a copy: the directory
// it recorded is where the next tool call runs, and the one it left keeps its
// own tools.
func TestSwitchSessionCarriesTheWorkspaceBack(t *testing.T) {
	here, there := t.TempDir(), t.TempDir()
	dir := t.TempDir()
	target := newSessionAt(t, there, dir)
	seedTarget(t, target)
	start := newSessionAt(t, here, dir)
	prov := faux.New(faux.Response{Text: "answer"})
	application := newTestApp(t, start, prov, func(cfg *Config) {
		cfg.Tools = func(cwd string) []tool.Tool { return nil }
		cfg.SystemPrompt = func(cwd string) (string, error) { return "prompt for " + cwd, nil }
	})
	startFile := start.File()
	if _, err := application.Ask(context.Background(), wire.UserMessage("mine"), nil); err != nil {
		t.Fatalf("ask: %v", err)
	}

	if err := application.SwitchSession(target.File()); err != nil {
		t.Fatalf("switch to the other project: %v", err)
	}
	if got := application.SystemPrompt(); got != "prompt for "+there {
		t.Fatalf("prompt = %q, want the other project's", got)
	}
	if err := application.SwitchSession(startFile); err != nil {
		t.Fatalf("switch back: %v", err)
	}
	if got := application.Session().Cwd(); got != here {
		t.Fatalf("cwd = %q, want %q", got, here)
	}
	if got := application.SystemPrompt(); got != "prompt for "+here {
		t.Fatalf("prompt = %q, want this project's again", got)
	}
}

// A session that carries its own instructions keeps them when it is opened from
// another project: the source is not asked, the same rule as at startup.
func TestSwitchSessionKeepsAStoredPromptAcrossProjects(t *testing.T) {
	there := t.TempDir()
	target := newSessionAt(t, there, t.TempDir())
	mustAppend(t, target, wire.SystemMessage("prompt from the session"))
	seedTarget(t, target)

	reads := 0
	application := newTestApp(t, newSession(t), faux.New(), func(cfg *Config) {
		cfg.SystemPrompt = func(string) (string, error) {
			reads++
			return "prompt from the source", nil
		}
	})
	before := reads
	if err := application.SwitchSession(target.File()); err != nil {
		t.Fatalf("switch: %v", err)
	}
	if got := application.SystemPrompt(); got != "prompt from the session" {
		t.Fatalf("prompt = %q, want the one the session recorded", got)
	}
	if reads != before {
		t.Fatalf("the source was asked %d time(s) for a session that carries its own prompt", reads-before)
	}
}

// A process with no source keeps the fixed text it was given: there is nothing
// to ask about the directory it opens, so the instructions stay the ones the
// front end handed in.
func TestSwitchSessionKeepsAFixedPromptWithoutASource(t *testing.T) {
	there := t.TempDir()
	target := newSessionAt(t, there, t.TempDir())
	seedTarget(t, target)

	application := newTestApp(t, newSession(t), faux.New())
	if err := application.SetSystemPrompt("prompt from the caller"); err != nil {
		t.Fatalf("set prompt: %v", err)
	}
	if err := application.SwitchSession(target.File()); err != nil {
		t.Fatalf("switch: %v", err)
	}
	if got := application.Session().Cwd(); got != there {
		t.Fatalf("cwd = %q, want the target's %q", got, there)
	}
	if got := application.SystemPrompt(); got != "prompt from the caller" {
		t.Fatalf("prompt = %q, want the fixed text to survive the switch", got)
	}
}

// A prompt that cannot be read refuses the switch instead of leaving half of one
// behind: the fields are only touched after every fallible step has run.
func TestSwitchSessionRefusesAPromptThatCannotBeRead(t *testing.T) {
	there := t.TempDir()
	target := newSessionAt(t, there, t.TempDir())
	seedTarget(t, target)

	application := newTestApp(t, newSession(t), faux.New(), func(cfg *Config) {
		cfg.SystemPrompt = func(cwd string) (string, error) {
			if cwd == there {
				return "", errors.New("context file: permission denied")
			}
			return "prompt for " + cwd, nil
		}
	})
	beforeID, beforePrompt := application.Session().ID(), application.SystemPrompt()

	err := application.SwitchSession(target.File())
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("err = %v, want the read failure", err)
	}
	if got := application.Session().ID(); got != beforeID {
		t.Fatalf("id = %q, want the failed switch to change nothing (%q)", got, beforeID)
	}
	if got := application.SystemPrompt(); got != beforePrompt {
		t.Fatalf("prompt = %q, want the failed switch to leave %q", got, beforePrompt)
	}
}

// Switching away from a session the user never talked to leaves no file: it
// was never a conversation, and inventing one for it is what put an empty
// session in the directory on every switch.
func TestSwitchSessionLeavesNoFileForAnEmptyConversation(t *testing.T) {
	cwd, dir := t.TempDir(), t.TempDir()
	target := newSessionAt(t, cwd, dir)
	seedTarget(t, target)
	current := newSessionAt(t, cwd, dir)
	application := newTestApp(t, current, faux.New())

	if err := application.SwitchSession(target.File()); err != nil {
		t.Fatalf("switch: %v", err)
	}
	if _, err := os.Stat(current.File()); !os.IsNotExist(err) {
		t.Fatalf("the abandoned empty conversation has a file at %s", current.File())
	}
	if got := application.Session().ID(); got != target.ID() {
		t.Fatalf("id = %q, want the target's %q", got, target.ID())
	}
}

// A conversation the user talked to keeps the file its first message created,
// so the app can come back to it and the next turn appends there.
func TestSwitchSessionKeepsATalkedConversationResumable(t *testing.T) {
	cwd, dir := t.TempDir(), t.TempDir()
	target := newSessionAt(t, cwd, dir)
	seedTarget(t, target)
	current := newSessionAt(t, cwd, dir)
	prov := faux.New(faux.Response{Text: "mine"})
	application := newTestApp(t, current, prov, func(c *Config) { c.Resolver = serveAll(prov) })
	if _, err := application.Ask(context.Background(), wire.UserMessage("mine"), nil); err != nil {
		t.Fatalf("ask: %v", err)
	}
	currentFile := current.File()

	if err := application.SwitchSession(target.File()); err != nil {
		t.Fatalf("switch: %v", err)
	}
	if err := application.SwitchSession(currentFile); err != nil {
		t.Fatalf("switch back: %v", err)
	}
	if got := application.Session().ID(); got != current.ID() {
		t.Fatalf("id = %q, want the session it left %q", got, current.ID())
	}
	if got := application.MessageCount(); got != 2 {
		t.Fatalf("messages = %d, want the restored conversation (2)", got)
	}

	if _, err := application.Ask(context.Background(), wire.UserMessage("hello"), nil); err != nil {
		t.Fatalf("ask: %v", err)
	}
	body, err := os.ReadFile(currentFile)
	if err != nil {
		t.Fatalf("read the file: %v", err)
	}
	if !bytes.Contains(body, []byte("hello")) {
		t.Fatal("the turn after coming back was not written to the session's file")
	}
}

// Without a resolver the recorded model cannot come back, and the configured
// one stays with a note, the same as at startup.
func TestSwitchSessionKeepsTheModelWithoutAResolver(t *testing.T) {
	cwd, dir := t.TempDir(), t.TempDir()
	target := newSessionAt(t, cwd, dir)
	prov := faux.New(faux.Response{Text: "answer"})
	builder := newTestApp(t, target, prov, func(c *Config) { c.Resolver = serveAll(prov) })
	if _, err := builder.Ask(context.Background(), wire.UserMessage("hi"), nil); err != nil {
		t.Fatalf("ask: %v", err)
	}
	if _, err := builder.SetModel("other", "other-1"); err != nil {
		t.Fatalf("set model: %v", err)
	}

	current := newSessionAt(t, cwd, dir)
	application := newTestApp(t, current, prov)
	if err := application.SwitchSession(target.File()); err != nil {
		t.Fatalf("switch: %v", err)
	}
	if got := application.Model().ID; got != "faux-1" {
		t.Fatalf("model = %q, want the configured faux-1", got)
	}
	if note := application.RestoreNote(); !strings.Contains(note, "other-1") {
		t.Fatalf("note = %q, want it to name the model that was kept", note)
	}
}

// A session whose recorded directory is gone is refused by the switch, before
// any tool runs: the tools are built for the directory the session records, so a
// project that was deleted has nowhere to put them and no override to fall back
// on here.
func TestSwitchSessionRefusesAMissingCwd(t *testing.T) {
	recorded, dir := t.TempDir(), t.TempDir()
	target := newSessionAt(t, recorded, dir)
	seedTarget(t, target)
	if err := os.RemoveAll(recorded); err != nil {
		t.Fatalf("remove the recorded directory: %v", err)
	}

	application := newTestApp(t, newSessionAt(t, t.TempDir(), dir), faux.New())
	before := application.Session().ID()
	err := application.SwitchSession(target.File())
	if err == nil {
		t.Fatal("switching to a session whose directory is gone succeeded")
	}
	if !strings.Contains(err.Error(), recorded) || !strings.Contains(err.Error(), "no longer exists") {
		t.Fatalf("err = %v, want the recorded directory named", err)
	}
	if got := application.Session().ID(); got != before {
		t.Fatalf("id = %q, want the failed switch to leave the session alone", got)
	}
}
