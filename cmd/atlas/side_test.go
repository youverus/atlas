package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pi-go/internal/app"
	"pi-go/internal/provider/faux"
	"pi-go/internal/tool"
	"pi-go/internal/tool/fs"
	"pi-go/internal/tui"
	"pi-go/internal/wire"
)

// sideApp is an app whose tool calls pass through the side gate and whose turns
// are scripted. The file tools are mounted as in a real session, because the
// gate is only worth testing against a call that would otherwise touch the disk.
func sideApp(t *testing.T, state *sideState, cwd string, responses ...faux.Response) *app.App {
	t.Helper()
	application, err := app.New(app.Config{
		Model:          wire.Model{ID: "faux-1", Name: "Faux", API: "faux", Provider: "faux"},
		Provider:       faux.New(responses...),
		SystemPrompt:   app.StaticPrompt("system prompt"),
		Tools:          app.StaticTools(fs.Tools(fs.Options{Cwd: cwd})),
		BeforeToolCall: state.beforeToolCall,
	}, memoryAt(t, cwd))
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	return application
}

// runSideCommand dispatches one slash command through the same table the REPL
// uses, with a side conversation available.
func runSideCommand(t *testing.T, application *app.App, state *sideState, line string) (string, error) {
	t.Helper()
	out := &bytes.Buffer{}
	render := newRenderer(out, out, false)
	done, err := replCommand(context.Background(), application, state, line, models{}, newSessionPicker(application, ""), nil, emptyPrompts(), out, render, mcpState{}, nil)
	if done {
		t.Fatalf("%s ended the session", line)
	}
	return out.String(), err
}

// noEvents drops the event stream; a test that only cares about the transcript
// does not need it.
func noEvents(wire.AgentEvent) error { return nil }

// toolCall is one call as the gate sees it: a name and the raw arguments.
func toolCall(name string) tool.Call {
	return tool.Call{Name: name, Arguments: json.RawMessage(`{}`)}
}

// refusedReason returns the text a call's result carried when it failed, which
// is how a blocked call reaches the model. Empty means the call went through.
func refusedReason(t *testing.T, events []wire.AgentEvent, id string) string {
	t.Helper()
	for _, event := range events {
		if event.Type != wire.AgentEventToolExecEnd || event.ToolCallID != id {
			continue
		}
		if event.IsError == nil || !*event.IsError {
			return ""
		}
		return strings.Join(resultTexts(event.Result), "\n")
	}
	t.Fatalf("no result for call %s", id)
	return ""
}

// TestSideInheritsTheMainConversation is the promise the notice makes: the side
// branch starts from what the main conversation had committed, and taking the
// snapshot does not touch the conversation it came from.
func TestSideInheritsTheMainConversation(t *testing.T) {
	dir := t.TempDir()
	state := newSideState()
	application := sideApp(t, state, dir, faux.Response{Text: "main answer"})
	if _, err := application.Ask(context.Background(), wire.UserMessage("main question"), noEvents); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	before := len(application.Messages())

	out, err := runSideCommand(t, application, state, "/side")
	if err != nil {
		t.Fatalf("/side: %v", err)
	}
	if !strings.Contains(out, "── side ── 2 messages inherited") {
		t.Fatalf("/side said %q, want the inherited count", out)
	}
	if !state.typingInSide() {
		t.Fatal("the prompt is still in the main conversation")
	}
	side := state.conversation()
	if side == nil {
		t.Fatal("no side conversation")
	}
	if got := len(side.Messages()); got != 2 {
		t.Fatalf("side inherited %d messages, want 2", got)
	}
	if got := len(application.Messages()); got != before {
		t.Fatalf("the main conversation grew from %d to %d messages", before, got)
	}
	if side.Session().Persisted() {
		t.Fatal("the side conversation is on disk")
	}
}

// TestSideParentKeepsAndSideReturns pins the two-step round trip: /parent leaves
// the conversation alone, and /side comes back to the same one instead of
// taking a second snapshot.
func TestSideParentKeepsAndSideReturns(t *testing.T) {
	dir := t.TempDir()
	state := newSideState()
	application := sideApp(t, state, dir, faux.Response{Text: "main answer"})
	if _, err := application.Ask(context.Background(), wire.UserMessage("main question"), noEvents); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if _, err := runSideCommand(t, application, state, "/side"); err != nil {
		t.Fatalf("/side: %v", err)
	}
	kept := state.conversation()

	out, err := runSideCommand(t, application, state, "/parent")
	if err != nil {
		t.Fatalf("/parent: %v", err)
	}
	if !strings.Contains(out, "── main ── the side conversation is kept") {
		t.Fatalf("/parent said %q", out)
	}
	if state.typingInSide() {
		t.Fatal("the prompt is still in the side conversation")
	}
	if state.conversation() != kept {
		t.Fatal("/parent dropped the conversation it promised to keep")
	}

	out, err = runSideCommand(t, application, state, "/side")
	if err != nil {
		t.Fatalf("/side again: %v", err)
	}
	if strings.Contains(out, "inherited") {
		t.Fatalf("/side took a second snapshot: %q", out)
	}
	if state.conversation() != kept {
		t.Fatal("/side opened a different conversation")
	}
}

// TestSideParentOutsideASideConversationIsAnError: the command has one meaning
// and says so instead of quietly doing nothing.
func TestSideParentOutsideASideConversationIsAnError(t *testing.T) {
	state := newSideState()
	application := sideApp(t, state, t.TempDir())
	if _, err := runSideCommand(t, application, state, "/parent"); err == nil {
		t.Fatal("/parent outside a side conversation reported success")
	}
}

// TestSideOffDiscards: the conversation is gone from both the terminal and the
// app, and there is nothing left to discard. Closing from the side view says
// where the prompt went, because the command did not submit a turn the user
// could have read the destination from.
func TestSideOffDiscards(t *testing.T) {
	dir := t.TempDir()
	state := newSideState()
	application := sideApp(t, state, dir)
	if _, err := runSideCommand(t, application, state, "/side"); err != nil {
		t.Fatalf("/side: %v", err)
	}
	out, err := runSideCommand(t, application, state, "/side off")
	if err != nil {
		t.Fatalf("/side off: %v", err)
	}
	if !strings.Contains(out, "── main ── side closed") {
		t.Fatalf("/side off in the side view said %q", out)
	}
	if state.conversation() != nil || state.typingInSide() {
		t.Fatal("the terminal still holds a discarded conversation")
	}
	if application.Side() != nil {
		t.Fatal("the app still holds a discarded conversation")
	}
	if _, err := runSideCommand(t, application, state, "/side off"); err == nil {
		t.Fatal("a second /side off reported success")
	}
}

// TestSideOffIsOnlyAnArgumentOnItsOwn: a question that happens to start with
// "off" is a question, not a request to throw the conversation away.
func TestSideOffIsOnlyAnArgumentOnItsOwn(t *testing.T) {
	dir := t.TempDir()
	state := newSideState()
	application := sideApp(t, state, dir, faux.Response{Text: "off by one, as you suspect"})
	out, err := runSideCommand(t, application, state, "/side off by one?")
	if err != nil {
		t.Fatalf("/side off by one?: %v", err)
	}
	if strings.Contains(out, "discarded") {
		t.Fatalf("a question discarded the conversation: %q", out)
	}
	side := state.conversation()
	if side == nil || len(side.Messages()) != 2 {
		t.Fatalf("the question did not run: %+v", side)
	}
}

// TestSideTakesTheQuestionOnTheSameLine: "/side <question>" and "/btw
// <question>" are the two names for one action, and the answer lands in the
// side conversation rather than the main one.
func TestSideTakesTheQuestionOnTheSameLine(t *testing.T) {
	for _, name := range []string{"/side", "/btw"} {
		dir := t.TempDir()
		state := newSideState()
		application := sideApp(t, state, dir, faux.Response{Text: "side answer"})
		before := len(application.Messages())
		if _, err := runSideCommand(t, application, state, name+" what changed?"); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		side := state.conversation()
		if side == nil {
			t.Fatalf("%s opened no conversation", name)
		}
		// One question and one answer on top of an empty snapshot.
		if got := len(side.Messages()); got != 2 {
			t.Fatalf("%s left %d side messages, want 2", name, got)
		}
		if got := len(application.Messages()); got != before {
			t.Fatalf("%s wrote into the main conversation: %d messages, was %d", name, got, before)
		}
	}
}

// TestSideRefusesCommandsThatMoveTheSession: /new or /model inside a side
// conversation would leave the terminal describing a conversation it is not
// talking to, so they are refused rather than obeyed.
func TestSideRefusesCommandsThatMoveTheSession(t *testing.T) {
	dir := t.TempDir()
	state := newSideState()
	application := sideApp(t, state, dir)
	if _, err := runSideCommand(t, application, state, "/side"); err != nil {
		t.Fatalf("/side: %v", err)
	}
	id := application.Session().ID()
	for _, line := range []string{"/new", "/model", "/fork", "/compact", "/init"} {
		_, err := runSideCommand(t, application, state, line)
		if err == nil {
			t.Fatalf("%s ran in a side conversation", line)
		}
		if !strings.Contains(err.Error(), "does not run in a side conversation") {
			t.Fatalf("%s failed with %q, want the side-conversation refusal", line, err)
		}
	}
	if got := application.Session().ID(); got != id {
		t.Fatalf("the session changed from %s to %s", id, got)
	}
}

// TestSideHelpSaysWhatStillRuns: the table lists every command atlas has, so
// the one place a user looks for answers has to say which of them apply here.
func TestSideHelpSaysWhatStillRuns(t *testing.T) {
	dir := t.TempDir()
	state := newSideState()
	application := sideApp(t, state, dir)
	if _, err := runSideCommand(t, application, state, "/side"); err != nil {
		t.Fatalf("/side: %v", err)
	}
	out, err := runSideCommand(t, application, state, "/help")
	if err != nil {
		t.Fatalf("/help: %v", err)
	}
	if !strings.Contains(out, sideHelp) {
		t.Fatalf("/help in a side conversation said %q", out)
	}
	if _, err := runSideCommand(t, application, state, "/parent"); err != nil {
		t.Fatalf("/parent: %v", err)
	}
	out, err = runSideCommand(t, application, state, "/help")
	if err != nil {
		t.Fatalf("/help: %v", err)
	}
	if strings.Contains(out, sideHelp) {
		t.Fatalf("/help in the main conversation said %q", out)
	}
}

// TestSideGateBlocksAWriteWithoutApproval is the behaviour the terminal owns
// rather than the prompt: a write in a side conversation does not happen unless
// the user says yes, and the model is told why.
func TestSideGateBlocksAWriteWithoutApproval(t *testing.T) {
	dir := t.TempDir()
	state := newSideState()
	asked := 0
	state.approve = func(tool.Call) bool {
		asked++
		return false
	}
	application := sideApp(t, state, dir,
		faux.Response{ToolCalls: []wire.Content{
			wire.ToolCallContent("c1", "write", json.RawMessage(`{"path":"pwned.txt","content":"hi"}`)),
		}},
		faux.Response{Text: "I could not write it"},
	)
	side, err := application.BeginSide()
	if err != nil {
		t.Fatalf("BeginSide: %v", err)
	}
	state.enter(side)

	var events []wire.AgentEvent
	if _, err := side.Ask(context.Background(), wire.UserMessage("write it"), func(event wire.AgentEvent) error {
		events = append(events, event)
		return nil
	}); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if asked != 1 {
		t.Fatalf("the gate asked %d times, want 1", asked)
	}
	if _, err := os.Stat(filepath.Join(dir, "pwned.txt")); !os.IsNotExist(err) {
		t.Fatalf("the write ran without approval: %v", err)
	}
	if got := refusedReason(t, events, "c1"); !strings.Contains(got, "declined") {
		t.Fatalf("the model was told %q, want the refusal", got)
	}
}

// TestSideGateRunsTheWriteWhenApproved: a yes is the whole difference, and the
// call then behaves exactly as it does in the main conversation.
func TestSideGateRunsTheWriteWhenApproved(t *testing.T) {
	dir := t.TempDir()
	state := newSideState()
	state.approve = func(tool.Call) bool { return true }
	application := sideApp(t, state, dir,
		faux.Response{ToolCalls: []wire.Content{
			wire.ToolCallContent("c1", "write", json.RawMessage(`{"path":"kept.txt","content":"hi"}`)),
		}},
		faux.Response{Text: "written"},
	)
	side, err := application.BeginSide()
	if err != nil {
		t.Fatalf("BeginSide: %v", err)
	}
	state.enter(side)
	if _, err := side.Ask(context.Background(), wire.UserMessage("write it"), noEvents); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "kept.txt")); err != nil {
		t.Fatalf("the approved write did not run: %v", err)
	}
}

// TestSideGateReadsWithoutAsking: the allowlist is what makes a side
// conversation useful — checking a file is the reason it exists.
func TestSideGateReadsWithoutAsking(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("hello\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	state := newSideState()
	asked := 0
	state.approve = func(tool.Call) bool {
		asked++
		return false
	}
	application := sideApp(t, state, dir,
		faux.Response{ToolCalls: []wire.Content{
			wire.ToolCallContent("c1", "read", json.RawMessage(`{"path":"notes.txt"}`)),
		}},
		faux.Response{Text: "it says hello"},
	)
	side, err := application.BeginSide()
	if err != nil {
		t.Fatalf("BeginSide: %v", err)
	}
	state.enter(side)
	var events []wire.AgentEvent
	if _, err := side.Ask(context.Background(), wire.UserMessage("what does it say"), func(event wire.AgentEvent) error {
		events = append(events, event)
		return nil
	}); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if asked != 0 {
		t.Fatalf("the gate asked %d times about a read, want 0", asked)
	}
	if got := refusedReason(t, events, "c1"); got != "" {
		t.Fatalf("the read failed with %q", got)
	}
}

// TestSideGateLeavesTheMainConversationAlone: the gate is the side
// conversation's, and a main turn keeps the ungated behaviour it has always had.
func TestSideGateLeavesTheMainConversationAlone(t *testing.T) {
	dir := t.TempDir()
	state := newSideState()
	asked := 0
	state.approve = func(tool.Call) bool {
		asked++
		return false
	}
	application := sideApp(t, state, dir,
		faux.Response{ToolCalls: []wire.Content{
			wire.ToolCallContent("c1", "write", json.RawMessage(`{"path":"main.txt","content":"hi"}`)),
		}},
		faux.Response{Text: "written"},
	)
	if _, err := application.Ask(context.Background(), wire.UserMessage("write it"), noEvents); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if asked != 0 {
		t.Fatalf("the gate asked %d times in the main conversation", asked)
	}
	if _, err := os.Stat(filepath.Join(dir, "main.txt")); err != nil {
		t.Fatalf("the main conversation's write did not run: %v", err)
	}
}

// TestSideGateDeniesWithoutATerminal: a caller with nobody to ask cannot approve
// anything, so the call is refused rather than waved through.
func TestSideGateDeniesWithoutATerminal(t *testing.T) {
	state := newSideState()
	state.enter(nil)
	if state.approve != nil {
		t.Fatal("the test set an approver")
	}
	decision, err := state.beforeToolCall(context.Background(), toolCall("bash"))
	if err != nil {
		t.Fatalf("beforeToolCall: %v", err)
	}
	if decision == nil || !decision.Block {
		t.Fatal("a call that changes state was allowed with nobody to ask")
	}
	if !strings.Contains(decision.Reason, "no terminal") {
		t.Fatalf("the reason was %q", decision.Reason)
	}
}

// TestCtrlOMovesThePromptBothWays: the key is a view switch, so it must not
// open a conversation and it must not drop the one it leaves.
func TestCtrlOMovesThePromptBothWays(t *testing.T) {
	dir := t.TempDir()
	state := newSideState()
	application := sideApp(t, state, dir)
	if _, err := runSideCommand(t, application, state, "/side"); err != nil {
		t.Fatalf("/side: %v", err)
	}
	opened := state.conversation()

	out := &bytes.Buffer{}
	render := newRenderer(out, out, false)
	if err := toggleSide(state, out, render); err != nil {
		t.Fatalf("Ctrl+O to main: %v", err)
	}
	if state.typingInSide() || state.conversation() != opened {
		t.Fatalf("after Ctrl+O: typing=%v conversation kept=%v, want the main view and the conversation kept", state.typingInSide(), state.conversation() == opened)
	}
	if !strings.Contains(out.String(), "Ctrl+O or /side returns to it") {
		t.Fatalf("notice = %q, want it to say how to come back", out.String())
	}

	out.Reset()
	if err := toggleSide(state, out, render); err != nil {
		t.Fatalf("Ctrl+O back: %v", err)
	}
	if !state.typingInSide() || state.conversation() != opened {
		t.Fatal("the second Ctrl+O did not come back to the same conversation")
	}
	if !strings.Contains(out.String(), sideKeys) {
		t.Fatalf("notice = %q, want the keys of the side view", out.String())
	}
}

// TestCtrlOWithNoSideConversationSaysSo: a keystroke never takes a snapshot, so
// with nothing to switch to it says what would open one instead of doing it —
// and says it as a notice, because a key landing on nothing is not a failure.
func TestCtrlOWithNoSideConversationSaysSo(t *testing.T) {
	state := newSideState()
	out := &bytes.Buffer{}
	render := newRenderer(out, out, false)
	if err := toggleSide(state, out, render); err != nil {
		t.Fatalf("Ctrl+O with no conversation = %v, want a notice", err)
	}
	if got := out.String(); !strings.Contains(got, "no side conversation; /side opens one") {
		t.Fatalf("notice = %q, want it to name /side", got)
	}
	if state.conversation() != nil {
		t.Fatal("a view switch opened a conversation")
	}
}

// TestTheSidePageSaysWhatTheNoticeUsedTo: a front end with a page for the side
// view says how much was inherited there, in the same frame the conversation
// opens, so the fresh /side stays silent. A conversation that already has lines
// has no page left to say it on, and that notice is how the reader sees where
// the prompt went — it stays.
func TestTheSidePageSaysWhatTheNoticeUsedTo(t *testing.T) {
	state := newSideState()
	state.home = true
	application := sideApp(t, state, t.TempDir(), faux.Response{Text: "main answer"}, faux.Response{Text: "side answer"})
	if _, err := application.Ask(context.Background(), wire.UserMessage("main question"), noEvents); err != nil {
		t.Fatalf("Ask: %v", err)
	}

	out, err := runSideCommand(t, application, state, "/side")
	if err != nil {
		t.Fatalf("/side: %v", err)
	}
	if out != "" {
		t.Fatalf("/side said %q with a page to say it on", out)
	}
	if !state.typingInSide() {
		t.Fatal("the prompt is still in the main conversation")
	}

	if _, err := runSideCommand(t, application, state, "/parent"); err != nil {
		t.Fatalf("/parent: %v", err)
	}
	out, err = runSideCommand(t, application, state, "/side")
	if err != nil {
		t.Fatalf("/side again: %v", err)
	}
	if !strings.Contains(out, "── side ── 2 messages") {
		t.Fatalf("the re-entry said %q, want the inherited count", out)
	}

	// A question on the same line is the one case where the page cannot say
	// it: the answer's first token arrives before the first frame, so the page
	// is gone by the time anyone looks. The notice stays.
	if _, err := runSideCommand(t, application, state, "/side off"); err != nil {
		t.Fatalf("/side off: %v", err)
	}
	out, err = runSideCommand(t, application, state, "/side what changed?")
	if err != nil {
		t.Fatalf("/side <question>: %v", err)
	}
	if !strings.Contains(out, "── side ── 2 messages") {
		t.Fatalf("a question on the same line said %q, want the inherited count", out)
	}
}

// TestReplacingTheSideConversationClearsTheSideView: the side view is a
// transcript like the main one, and nothing in it is versioned, so a different
// conversation behind it has to start clean — otherwise the next side
// conversation is drawn on top of the one that was discarded. Moving the prompt
// (/parent, /side) is not a replacement and erases nothing.
func TestReplacingTheSideConversationClearsTheSideView(t *testing.T) {
	state := newSideState()
	application := sideApp(t, state, t.TempDir(), faux.Response{Text: "main answer"})
	if _, err := application.Ask(context.Background(), wire.UserMessage("main question"), noEvents); err != nil {
		t.Fatalf("Ask: %v", err)
	}

	resets := 0
	sink := tui.Sink{
		Note:   func(string) {},
		Answer: func(string) {},
		View:   func(int) {},
		Reset: func(view int) {
			if view != tui.ViewSide {
				t.Fatalf("resetting view %d, want the side one", view)
			}
			resets++
		},
	}
	run := func(line string, want int) {
		t.Helper()
		if err := runTUICommand(context.Background(), application, state, mcpState{}, models{}, newSessionPicker(application, ""), emptyPrompts(), nil, line, sink); err != nil {
			t.Fatalf("%s: %v", line, err)
		}
		if resets != want {
			t.Fatalf("%s left %d resets, want %d", line, resets, want)
		}
	}

	run("/side", 0)     // nothing existed, so nothing is erased: the view starts empty
	run("/parent", 0)   // the prompt moved back; the side view keeps what it has
	run("/side", 0)     // and forward again: same conversation
	run("/side off", 1) // discarded, so its lines go with it
	run("/side", 1)     // the new one starts on the view that was just cleared
	run("/parent", 1)   // still nothing replaced
}

// TestTheOnlySideTurnIsNotErasedByItsOwnCommand: /side <question> answers into
// the side view while the command is still running, so the "a different side
// conversation owns this view now" reset must not fire for the first one — it
// would drop the only copy of that turn a frame after it was drawn.
func TestTheOnlySideTurnIsNotErasedByItsOwnCommand(t *testing.T) {
	state := newSideState()
	application := sideApp(t, state, t.TempDir(), faux.Response{Text: "side answer"})
	resets, notes := 0, 0
	sink := tui.Sink{
		Note:   func(string) { notes++ },
		Answer: func(string) {},
		View:   func(int) {},
		Reset:  func(int) { resets++ },
	}
	if err := runTUICommand(context.Background(), application, state, mcpState{}, models{}, newSessionPicker(application, ""), emptyPrompts(), nil, "/side what changed?", sink); err != nil {
		t.Fatalf("/side <question>: %v", err)
	}
	if resets != 0 {
		t.Fatalf("the side view was reset %d times, want none", resets)
	}
	if notes == 0 {
		t.Fatal("the answer never reached the view; there would be nothing on screen")
	}
}

// TestClosingTheSideConversationFromTheMainViewIsQuiet: /side off from the main
// conversation changes no view, so the transcript gets one notice and nothing
// else.
func TestClosingTheSideConversationFromTheMainViewIsQuiet(t *testing.T) {
	state := newSideState()
	application := sideApp(t, state, t.TempDir())
	if _, err := runSideCommand(t, application, state, "/side"); err != nil {
		t.Fatalf("/side: %v", err)
	}
	if err := toggleSide(state, &bytes.Buffer{}, newRenderer(&bytes.Buffer{}, &bytes.Buffer{}, false)); err != nil {
		t.Fatalf("Ctrl+O to main: %v", err)
	}

	out := &bytes.Buffer{}
	render := newRenderer(out, out, false)
	if err := discardSide(application, state, out, render); err != nil {
		t.Fatalf("/side off: %v", err)
	}
	if strings.Count(out.String(), "\n") != 1 || !strings.Contains(out.String(), "side discarded") {
		t.Fatalf("output = %q, want the notice", out.String())
	}
}
