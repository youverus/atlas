package main

// The full-screen session's half of stopping a turn: what it hands back to the
// reader, and what it tells them afterwards.

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"pi-go/internal/app"
	"pi-go/internal/config"
	"pi-go/internal/provider/faux"
	"pi-go/internal/tui"
	"pi-go/internal/wire"
)

// homeSink records what a command said to the screen, and where it said it: a
// line in the transcript, an answer, or the one line the home page carries.
//
// A reset clears what came before it, which is what the screen does: the lines
// written while the command ran belong to the conversation it swapped out, so
// only what arrives afterwards is what the reader is left looking at.
type homeSink struct {
	notes   []string
	answers []string
	flashes []string
	resets  int
}

func (h *homeSink) sink() tui.Sink {
	return tui.Sink{
		Event:  func(wire.AgentEvent) {},
		Note:   func(text string) { h.notes = append(h.notes, text) },
		Answer: func(text string) { h.answers = append(h.answers, text) },
		View:   func(int) {},
		Reset: func(int) {
			h.resets++
			h.notes, h.answers = nil, nil
		},
		Flash: func(text string) { h.flashes = append(h.flashes, text) },
	}
}

// TestSessionCommandsThatEmptyTheSessionLandOnTheHomePage is the rule the
// screen now follows: a command that leaves a session with nothing in it puts
// the reader back on the page, and the page is the receipt. Saying it in the
// transcript instead would push the page away with the very line that reports
// the archive — the reader would be left looking at "archived: …" and a session
// id for a conversation that has not been written yet.
func TestSessionCommandsThatEmptyTheSessionLandOnTheHomePage(t *testing.T) {
	for _, tc := range []struct {
		command string
		flash   string // what the page has to say; empty means nothing at all
	}{
		{"/archive", "archived: session "}, // plus the id prefix, checked below
		{"/delete", "deleted: session "},
		{"/new", ""},
	} {
		cwd, dir := t.TempDir(), t.TempDir()
		manager := sessionAt(t, cwd, dir)
		application := commandApp(t, manager, cwd, faux.Response{Text: "answer"})
		if _, err := application.Ask(context.Background(), wire.UserMessage("hello"), nil); err != nil {
			t.Fatalf("ask: %v", err)
		}
		// /delete only accepts a conversation that was archived first, because
		// the archive is the second look before a transcript is destroyed. The
		// reader reaches that state by archiving one and opening it again.
		if tc.command == "/delete" {
			archived := application.Session().ID()
			if _, err := runCommand(t, application, "/archive"); err != nil {
				t.Fatalf("/archive: %v", err)
			}
			if _, err := runCommand(t, application, "/recover "+archived[:8]); err != nil {
				t.Fatalf("/recover: %v", err)
			}
		}
		id := application.Session().ID()

		recorder := &homeSink{}
		if err := runTUICommand(context.Background(), application, nil, mcpState{}, models{}, newSessionPicker(application, ""), emptyPrompts(), nil, tc.command, recorder.sink()); err != nil {
			t.Fatalf("%s: %v", tc.command, err)
		}

		if recorder.resets != 1 {
			t.Fatalf("%s reset the transcript %d times, want once", tc.command, recorder.resets)
		}
		if len(recorder.notes) != 0 || len(recorder.answers) != 0 {
			t.Fatalf("%s left %q in the transcript, want the page", tc.command, append(recorder.notes, recorder.answers...))
		}
		if application.Session().ID() == id {
			t.Fatalf("%s kept the session", tc.command)
		}
		if count := application.MessageCount(); count != 0 {
			t.Fatalf("%s left %d messages in the new session", tc.command, count)
		}

		if tc.flash == "" {
			if len(recorder.flashes) != 0 {
				t.Fatalf("%s said %q on the page, want nothing", tc.command, recorder.flashes)
			}
			continue
		}
		if len(recorder.flashes) != 1 {
			t.Fatalf("%s said %q on the page, want one line", tc.command, recorder.flashes)
		}
		if want := tc.flash + id[:8]; !strings.HasPrefix(recorder.flashes[0], want) {
			t.Fatalf("%s said %q on the page, want it to open with %q", tc.command, recorder.flashes[0], want)
		}
	}
}

// TestArchivingOnTheHomePageSaysHowToGetItBack: the page keeps the one hint the
// reader needs a keystroke later, because the transcript that carried it is the
// thing the reset erased.
func TestArchivingOnTheHomePageSaysHowToGetItBack(t *testing.T) {
	cwd, dir := t.TempDir(), t.TempDir()
	manager := sessionAt(t, cwd, dir)
	application := commandApp(t, manager, cwd, faux.Response{Text: "answer"})
	if _, err := application.Ask(context.Background(), wire.UserMessage("hello"), nil); err != nil {
		t.Fatalf("ask: %v", err)
	}

	recorder := &homeSink{}
	if err := runTUICommand(context.Background(), application, nil, mcpState{}, models{}, newSessionPicker(application, ""), emptyPrompts(), nil, "/archive", recorder.sink()); err != nil {
		t.Fatalf("/archive: %v", err)
	}
	if len(recorder.flashes) != 1 || !strings.Contains(recorder.flashes[0], "/recover opens it again") {
		t.Fatalf("the page says %q, want the way back", recorder.flashes)
	}
}

// TestResumingAConversationStillReplaysIt pins the other half of the branch: a
// session that arrives with history keeps its transcript, and the report is
// said there because the page is not what is on screen.
func TestResumingAConversationStillReplaysIt(t *testing.T) {
	application, picker, target := resumeSetup(t)

	recorder := &homeSink{}
	if err := runTUICommand(context.Background(), application, nil, mcpState{}, models{}, picker, emptyPrompts(), nil, "/resume "+target.ID()[:8], recorder.sink()); err != nil {
		t.Fatalf("/resume: %v", err)
	}
	if len(recorder.flashes) != 0 {
		t.Fatalf("the resumed conversation flashed %q, want the history", recorder.flashes)
	}
	if len(recorder.answers) != 1 {
		t.Fatalf("the resumed conversation replayed %d answers, want 1", len(recorder.answers))
	}
	if joined := strings.Join(recorder.notes, "\n"); !strings.Contains(joined, "resumed: alpha") {
		t.Fatalf("the report is missing:\n%s", joined)
	}
}

// TestInterruptQueuesHandsBackBothConversations pins the rule that nothing
// queued survives an interrupt: the screen runs one turn at a time, so a message
// waiting in either conversation was written for the turn being abandoned, and
// delivering it to the next one would say it into a conversation the reader had
// just decided to leave.
func TestInterruptQueuesHandsBackBothConversations(t *testing.T) {
	dir := t.TempDir()
	state := newSideState()
	application := sideApp(t, state, dir, faux.Response{Text: "main answer"})
	if _, err := runSideCommand(t, application, state, "/side"); err != nil {
		t.Fatalf("/side: %v", err)
	}
	side := state.conversation()
	if side == nil {
		t.Fatal("no side conversation")
	}

	application.Steer(wire.UserMessage("steer the main turn"))
	application.FollowUp(wire.UserMessage("and then follow up"))
	side.Steer(wire.UserMessage("steer the side turn"))

	lines := interruptQueues(application, state)
	want := []string{"steer the main turn", "and then follow up", "steer the side turn"}
	if len(lines) != len(want) {
		t.Fatalf("handed back %q, want %q", lines, want)
	}
	for i, line := range want {
		if lines[i] != line {
			t.Fatalf("handed back %q, want %q", lines, want)
		}
	}
	if pending := application.PendingCount(); pending != 0 {
		t.Fatalf("%d messages are still queued on the main conversation", pending)
	}
	if steering, followUp := side.ClearQueue(); len(steering)+len(followUp) != 0 {
		t.Fatalf("%d messages are still queued on the side conversation", len(steering)+len(followUp))
	}

	// Nothing left to hand back is the ordinary case, not an error.
	if again := interruptQueues(application, state); len(again) != 0 {
		t.Fatalf("a second interrupt handed back %q", again)
	}
}

// TestInterruptedTurnIsReportedOnce covers the difference between the two front
// ends: the inline session prints "[stop reason aborted]" after every turn
// because its transcript is a log, while the full-screen session has to say it
// once, through the notice the interface draws for a cancellation.
func TestInterruptedTurnIsReportedOnce(t *testing.T) {
	application, err := app.New(app.Config{
		Model:    wire.Model{ID: "m1", API: "faux", Provider: "test"},
		Provider: cancelled{},
	}, inMemory(t))
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var notices []string
	sink := tui.Sink{
		Event: func(wire.AgentEvent) {},
		Note:  func(text string) { notices = append(notices, text) },
	}
	err = tuiMainTurn(ctx, application, "hi", sink)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the cancellation", err)
	}
	// The interface turns this error into one line; the turn itself contributes
	// no notice of its own, because the aborted answer is not a failure.
	for _, note := range notices {
		if note != "" {
			t.Fatalf("the interrupted turn noted %q", note)
		}
	}
}

// TestTheTurnNoticeSaysWhatItCost covers the line the full-screen session
// leaves after a turn: the parts the screen cannot show. The numbers are
// measured against a real clock here, so only their shape is asserted — the
// arithmetic belongs to cost_test.go.
func TestTheTurnNoticeSaysWhatItCost(t *testing.T) {
	application, err := app.New(app.Config{
		Model: wire.Model{ID: "m1", API: "faux", Provider: "test"},
		Provider: faux.New(faux.Response{
			Text:       "hi",
			StopReason: wire.StopStop,
			Usage:      wire.Usage{Input: 40, Output: 3, TotalTokens: 43},
		}),
	}, inMemory(t))
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}

	var notes []string
	sink := tui.Sink{
		Event: func(wire.AgentEvent) {},
		Note:  func(text string) { notes = append(notes, text) },
	}
	if err := tuiMainTurn(context.Background(), application, "hi", sink); err != nil {
		t.Fatalf("tuiMainTurn: %v", err)
	}
	if len(notes) != 1 {
		t.Fatalf("notices = %q, want one", notes)
	}

	parts := strings.Split(notes[0], " · ")
	if len(parts) != 4 {
		t.Fatalf("notice = %q, want four parts", notes[0])
	}
	if want := "43 tokens (in 40, out 3)"; parts[0] != want {
		t.Fatalf("tokens = %q, want %q", parts[0], want)
	}
	if !strings.HasSuffix(parts[1], " tok/s") {
		t.Fatalf("rate = %q, want a rate", parts[1])
	}
	if !strings.HasPrefix(parts[2], "took ") {
		t.Fatalf("duration = %q, want took", parts[2])
	}
	clock := strings.TrimPrefix(parts[3], "ended at ")
	if _, err := time.Parse("15:04:05", clock); err != nil || clock == parts[3] {
		t.Fatalf("ended = %q, want the wall clock", parts[3])
	}
}

// TestARejectedTurnIsNotNoticed covers the other half: a turn that spent
// nothing says nothing. The failure is already on screen in red, and a line
// under it measuring how long a rejected request took to be rejected is noise.
func TestARejectedTurnIsNotNoticed(t *testing.T) {
	application, err := app.New(app.Config{
		Model: wire.Model{ID: "m1", API: "faux", Provider: "test"},
		Provider: faux.New(faux.Response{
			StopReason: wire.StopError,
			Err:        errors.New("no credential for this provider"),
		}),
	}, inMemory(t))
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}

	var notices []string
	sink := tui.Sink{
		Event: func(wire.AgentEvent) {},
		Note:  func(text string) { notices = append(notices, text) },
	}
	if err := tuiMainTurn(context.Background(), application, "hi", sink); err == nil {
		t.Fatal("the rejected turn reported no error")
	}
	if len(notices) != 0 {
		t.Fatalf("notices = %q, want nothing", notices)
	}
}

// countOf and flagOf spell the optional fields of the config's layout, which
// store a pointer precisely so that "not written" and "written as zero" differ.
func countOf(value int) *int  { return &value }
func flagOf(value bool) *bool { return &value }

// TestTheLayoutComesFromTheConfigFile pins the merge: the file names the fields
// it wants changed and everything else keeps its default. A file that mentions
// one gap must not reset the other eight to zero, which is the failure mode the
// pointers exist to avoid in the other direction.
func TestTheLayoutComesFromTheConfigFile(t *testing.T) {
	// Every field, each to something that is not the default, so a field the
	// merge forgets cannot pass by accident.
	layout := tuiLayout(&config.Config{UI: config.UI{Layout: config.Layout{
		Blocks: config.Blocks{
			Transcript: config.TranscriptBlock{Gap: countOf(2), Frames: flagOf(false)},
			Composer:   config.ComposerBlock{GapAbove: countOf(0), Rows: countOf(4), Frame: flagOf(false)},
			Palette:    config.PaletteBlock{Rows: countOf(2)},
			Status:     config.StatusBlock{GapAbove: countOf(3)},
			Menu:       config.MenuBlock{Rows: countOf(6), Width: countOf(100)},
			Approval:   config.ApprovalBlock{Width: countOf(50)},
			Welcome:    config.WelcomeBlock{On: flagOf(false)},
		},
		Roles: config.Roles{Thinking: config.ThinkingRole{Rows: countOf(3)}},
	}}})
	want := tui.Layout{
		ComposerGap: 0, StatusGap: 3, BlockGap: 2, ComposerRows: 4, ComposerFrame: false,
		TranscriptFrames: false,
		PaletteRows:      2, MenuRows: 6, MenuWidth: 100, BoxWidth: 50, ThinkingRows: 3,
		HomePage: false,
	}
	if layout != want {
		t.Fatalf("layout = %+v, want %+v", layout, want)
	}

	// And the other half: a file that mentions one field leaves the rest alone.
	partial := tuiLayout(&config.Config{UI: config.UI{Layout: config.Layout{
		Blocks: config.Blocks{Composer: config.ComposerBlock{GapAbove: countOf(0)}},
	}}})
	defaults := tui.DefaultLayout()
	defaults.ComposerGap = 0
	if partial != defaults {
		t.Fatalf("a file naming one field changed the others: %+v", partial)
	}

	// No config at all is no opinion, not an empty screen.
	if tuiLayout(nil) != tui.DefaultLayout() {
		t.Fatalf("tuiLayout(nil) = %+v, want the defaults", tuiLayout(nil))
	}
	if tuiLayout(&config.Config{}) != tui.DefaultLayout() {
		t.Fatalf("an empty ui block = %+v, want the defaults", tuiLayout(&config.Config{}))
	}
}

// TestAColorInTheConfigOverridesTheTheme: the theme stays the palette and the
// config is what the reader wanted different about it, so a color in the file
// has to reach the styles the frame is drawn with.
func TestAColorInTheConfigOverridesTheTheme(t *testing.T) {
	t.Setenv("ATLAS_HOME", t.TempDir())
	styles, err := tuiStyles(
		&config.Config{UI: config.UI{Colors: map[string]string{"accent": "#ff0000"}}},
		t.TempDir(),
		streams{in: strings.NewReader(""), out: io.Discard, err: io.Discard},
	)
	if err != nil {
		t.Fatalf("tuiStyles: %v", err)
	}
	// Exactly what the terminal is asked to paint the accent with: the point is
	// not that a style exists but that the pixels move.
	// The numbers, not the whole escape: lipgloss writes one cell's foreground
	// and background together, and the background is the theme's screen color.
	if rendered := styles.Accent.Render("x"); !strings.Contains(rendered, "38;2;255;0;0") {
		t.Fatalf("accent rendered %q, want the configured red", rendered)
	}
}

// TestAColorTheThemeDoesNotKnowStopsTheInterface is where the color block is
// checked: internal/config cannot reach the palette's vocabulary, so the
// failure lands here, before the first frame, and it has to name the token the
// reader wrote rather than the startup that gave up.
func TestAColorTheThemeDoesNotKnowStopsTheInterface(t *testing.T) {
	t.Setenv("ATLAS_HOME", t.TempDir())
	stdio := streams{in: strings.NewReader(""), out: io.Discard, err: io.Discard}
	for _, tc := range []struct{ name, token, value, expect string }{
		{"unknown token", "acccent", "#ff0000", "acccent"},
		{"named color", "accent", "blue", "accent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tuiStyles(
				&config.Config{UI: config.UI{Colors: map[string]string{tc.token: tc.value}}},
				t.TempDir(), stdio,
			)
			if err == nil {
				t.Fatalf("tuiStyles accepted %s = %q", tc.token, tc.value)
			}
			if !strings.Contains(err.Error(), tc.expect) || !strings.Contains(err.Error(), "tui: colors") {
				t.Fatalf("error = %v, want it to name %s under tui: colors", err, tc.expect)
			}
		})
	}
}
