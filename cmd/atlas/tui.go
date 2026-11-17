package main

// The full-screen session. It replaces the line-oriented loop and nothing else:
// the app, the side conversation, the command table and the approval gate are
// the same objects the inline interface drives. What changes is where their
// output goes — into a transcript the program owns instead of the terminal's
// scrollback.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"

	"pi-go/internal/app"
	"pi-go/internal/assembly"
	"pi-go/internal/config"
	"pi-go/internal/theme"
	"pi-go/internal/tui"
	"pi-go/internal/wire"
)

// runTUI runs the session on the alternate screen until the reader leaves.
func runTUI(ctx context.Context, application *app.App, side *sideState, servers mcpState, models models, resume *sessionPicker, prompts *assembly.PromptSource, bar *statusBar, cfg *config.Config, cwd string, stdio streams) error {
	styles, err := tuiStyles(cfg, cwd, stdio)
	if err != nil {
		return err
	}
	// This front end draws a page for the side view, so a side conversation it
	// opens does not also announce itself in a notice.
	side.home = true

	model := tui.New(tui.Config{
		Styles: styles,
		Layout: tuiLayout(cfg),
		Title:  "atlas " + version,
		// The home screen repeats three of the footer's facts. That is not a
		// second source of truth: the footer is one line under a conversation
		// that has already started, and the first thing a session shows is
		// where it is and who answers before anything has been asked.
		//
		// It is asked per frame because the side conversation has a page of its
		// own, and what it says — how much the snapshot inherited — is known
		// when it is opened rather than when the program starts.
		Welcome: func(view int) *tui.Welcome {
			if view == tui.ViewSide {
				return sideHome(side, application, bar)
			}
			return mainHome(application, bar)
		},
		// The footer is the one the inline interface already draws: copying it
		// would be a second implementation of "which model, how much context is
		// left". Only the width matters here; the view and the busy flag are
		// spelled by the prompt instead.
		Status: func(width int) string { return bar.line(width) },
		Run: func(ctx context.Context, line string, sink tui.Sink) error {
			return runTUILine(ctx, application, side, servers, models, resume, prompts, chooser(sink.Pick), line, sink)
		},
		Toggle: func() (int, string) {
			notes := &noteWriter{note: func(string) {}}
			if err := toggleSide(side, notes, newRenderer(notes, notes, false)); err != nil {
				return activeView(side), "error: " + err.Error()
			}
			return activeView(side), notes.take()
		},
		// Ctrl+C with nothing running steps out of the side conversation, and
		// only the main one treats it as the first half of leaving the session.
		// Backing out is the same move Ctrl+O makes, so it reports the same
		// line: the two keys differ in where they are pressed, not in what they
		// do.
		Back: func() (int, string, bool) {
			if !side.typingInSide() {
				return tui.ViewMain, "", false
			}
			notes := &noteWriter{note: func(string) {}}
			if err := leaveSide(side, notes, newRenderer(notes, notes, false)); err != nil {
				return tui.ViewMain, "", false
			}
			return activeView(side), notes.take(), true
		},
		Suggest: func(line string) []tui.Suggestion { return suggestionsFor(line, side, application.Archived()) },
		// The same palette, one provider further: while an "@path" is being
		// typed, the files it could become. It resolves against the session's
		// directory, which is the one the sender will use — a list built from a
		// different tree would offer names the next Enter cannot read.
		Attach: func(line string) []tui.Suggestion { return attachSuggestions(line, attachDir(application)) },
		// Stopping a turn hands back what was queued behind it: those messages
		// were written for the turn that is being abandoned, and the next one
		// must not open with them.
		Interrupt: func() []string { return interruptQueues(application, side) },
	})

	program := tea.NewProgram(model)
	// A signal or a caller cancelling the context is the one way out that does
	// not come through a key: without this the program would keep the terminal
	// until the process died.
	go func() {
		<-ctx.Done()
		program.Quit()
	}()
	// The release check runs on its own so a slow or unreachable network never
	// delays the first frame. A newer release is reported as a flash on the
	// home screen, where the update command is one line away; an older one and
	// a failed check both stay silent, because the session must not open with
	// an error nobody asked for.
	go func() {
		checkCtx, cancel := context.WithTimeout(ctx, updateTimeout)
		defer cancel()
		newest, err := checkUpdate(checkCtx, updateClient(), updateRepo)
		if err != nil || newest == "" {
			return
		}
		program.Send(tui.FlashMsg{Text: fmt.Sprintf("atlas %s is available — /update to install", newest)})
	}()
	_, err = program.Run()
	return err
}

// interruptQueues drops everything queued for a turn that is being stopped and
// returns it as text, so the full-screen session can put it back in the input
// line.
//
// Both queues go, and both conversations: the screen runs one turn at a time,
// so nothing in either queue is on its way to a model, and a message written for
// the turn the reader just abandoned must not open the next one. The main
// conversation comes first because that is where the prompt lands.
func interruptQueues(application *app.App, side *sideState) []string {
	var lines []string
	collect := func(steering, followUp []wire.Message) {
		for _, message := range append(steering, followUp...) {
			if text := strings.TrimSpace(message.Text()); text != "" {
				lines = append(lines, text)
			}
		}
	}
	if application != nil {
		collect(application.ClearQueue())
	}
	if side != nil {
		if conversation := side.conversation(); conversation != nil {
			collect(conversation.ClearQueue())
		}
	}
	return lines
}

// activeView turns the session's own answer into a view index. Side state is the
// source of truth: a view that disagreed with it would send the next prompt to a
// conversation nobody is looking at.
func activeView(side *sideState) int {
	if side != nil && side.typingInSide() {
		return tui.ViewSide
	}
	return tui.ViewMain
}

// mainHome is the session's home screen: where this is and who answers.
func mainHome(application *app.App, bar *statusBar) *tui.Welcome {
	return &tui.Welcome{
		Version: version,
		Dir:     bar.dir,
		Model:   modelField(application.Model()),
		Note:    welcomeNote(application, bar),
	}
}

// sideHome is the home screen of the side conversation. It says what the
// snapshot inherited, because that count is the one fact that proves where the
// branch was taken from, and it says it in the page rather than in a notice:
// the page is what a view with nothing in it shows, and a notice would be the
// same sentence one frame later and a second thing to read.
//
// Nil when no side conversation is open: the side view is then a view of
// nothing, and it is only ever on screen for the frames a switch takes.
func sideHome(side *sideState, application *app.App, bar *statusBar) *tui.Welcome {
	conversation := side.conversation()
	if conversation == nil {
		return nil
	}
	return &tui.Welcome{
		Title: fmt.Sprintf("side conversation · %s", inherited(len(conversation.Messages()))),
		Model: modelField(application.Model()),
		Note:  welcomeNote(application, bar),
	}
}

// inherited names how much of the main conversation the snapshot carried.
func inherited(count int) string {
	if count == 1 {
		return "1 message inherited"
	}
	return fmt.Sprintf("%d messages inherited", count)
}

// welcomeNote is the tail of the home screen's second line: how hard the next
// request asks the model to think, and what the agent may touch. Both are footer
// fields, said once at the top of a session whose footer has nothing else in it
// yet.
func welcomeNote(application *app.App, bar *statusBar) string {
	parts := make([]string, 0, 2)
	if level := thinkingField(application.Model(), application.ThinkingLevel()); level != "" {
		parts = append(parts, level)
	}
	if grants := bar.grantsField(); grants != "" {
		parts = append(parts, grants)
	}
	return strings.Join(parts, " · ")
}

// runTUILine handles one submitted line: a slash command, a side turn, or a main
// turn. The order matters and is the same one the REPL uses.
func runTUILine(ctx context.Context, application *app.App, side *sideState, servers mcpState, models models, resume *sessionPicker, prompts *assembly.PromptSource, pick chooser, line string, sink tui.Sink) error {
	if side != nil && side.gate != nil && application != nil {
		// The question is rebuilt per turn because it closes over the turn's
		// sink; what a "yes, and stop asking" answer remembers is keyed by the
		// session open right now, so a session switch starts from no standing
		// answers. Installing it before commands and main turns lets both the
		// main permission ladder and the side conversation ask through the same
		// modal.
		side.gate.SetSession(func() string { return application.Session().ID() })
		side.setAsk(func(request assembly.Ask) assembly.Answer {
			reply := sink.Ask(tui.AskRequest{Subject: request.Subject, Tool: request.Tool})
			return assembly.Answer{Allowed: reply.Allowed, Always: reply.Always}
		}, sink.Note)
	}
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "/") {
		return runTUICommand(ctx, application, side, servers, models, resume, prompts, pick, trimmed, sink)
	}
	if conversation := side.active(); conversation != nil {
		return tuiSideTurn(ctx, conversation, line, sink)
	}
	return tuiMainTurn(ctx, application, line, sink)
}

// runTUICommand runs one slash command and turns its output into notices.
//
// The command table is the one the inline interface uses, unchanged: it writes
// to an io.Writer and reads session state, which is exactly the contract a notice
// writer can satisfy.
func runTUICommand(ctx context.Context, application *app.App, side *sideState, servers mcpState, models models, resume *sessionPicker, prompts *assembly.PromptSource, pick chooser, line string, sink tui.Sink) error {
	// Each line goes to whichever conversation is the prompt's home when it is
	// written, and the switch is announced first. /side asks its question before
	// the command returns, so a stream that were pinned to the old view would put
	// the whole answer in the transcript the reader just left.
	notes := &noteWriter{note: func(line string) {
		sink.View(activeView(side))
		sink.Note(line)
	}}
	render := newRenderer(notes, notes, false)
	sessionBefore := application.Session().ID()
	// Nil when this front end has no side view at all (a pipe, a test): only a
	// screen has one, and only a screen has a transcript to drop there.
	var sideBefore *app.Side
	if side != nil {
		sideBefore = side.conversation()
	}

	// A command that runs a turn streams it into the answer block, the same way
	// a typed prompt does: the interruption the reader is resuming was drawn
	// there, and continuing it anywhere else would answer in a second place.
	turn := func(turnCtx context.Context, run func(context.Context, func(wire.AgentEvent) error) (app.Result, error)) (app.Result, error) {
		return tuiTurn(turnCtx, sink, run)
	}

	done, err := replCommand(ctx, application, side, line, models, resume, pick, prompts, notes, render, servers, sink.Compose, turn)
	notes.Flush()
	// A command may have moved the prompt (/side, /parent, /side off) or swapped
	// the session out from under the transcript (/new, /resume, /fork).
	sink.View(activeView(side))
	if side != nil && sideBefore != nil && side.conversation() != sideBefore {
		// A side conversation was replaced or discarded: the lines left in the
		// side view belong to a conversation that is gone, and the next one must
		// not start on top of them. Opening the first one is not a replacement —
		// the view is empty by construction (nothing else ever draws there), and
		// a question typed on the same line (/side <question>) has already
		// answered into that view by the time this line runs: resetting here
		// would erase the only copy of the turn.
		sink.Reset(tui.ViewSide)
	}
	if application.Session().ID() != sessionBefore {
		// Everything the command said belongs to the conversation that is gone:
		// "resumed: …", the note about a recorded directory, the fork's two
		// lines. The transcript is dropped, so that report is said again after
		// the reset and before the replayed history — otherwise the one line
		// that says which conversation this now is would be the line the reset
		// erased.
		report := notes.takeLines()
		// The session commands are refused in the side view, so the conversation
		// they swapped is always the one the main prompt holds.
		sink.Reset(tui.ViewMain)
		if application.MessageCount() == 0 {
			// The command landed on a session with nothing in it, so the screen
			// is now the home page — and the home page is the receipt: it says
			// "an empty session, as asked" better than any sentence about a new
			// id would, and that is the whole point of going back to it. Notes
			// cannot be used for this, because the transcript is what the page
			// replaces: one line in it would push the page away and leave the
			// reader looking at a receipt for a session that does not exist
			// yet. Only the headline survives, on the page.
			if flash := homeFlash(line, report); flash != "" {
				sink.Flash(flash)
			}
		} else {
			for _, line := range report {
				sink.Note(line)
			}
			// The answer is handed over as an answer, not as lines: the
			// interface wraps it inside the bar, which is the shape the live
			// turn had.
			writeTranscript(notes, application.Messages(), func(text string) {
				sink.Answer(text)
			})
			notes.Flush()
		}
	}
	if err != nil {
		return err
	}
	if done {
		return tui.ErrExit
	}
	return nil
}

// homeFlash is the one line the home page says about the command that sent the
// reader back to it.
//
// Each of these commands writes a report for a reader who stays put: the
// headline, then lines about the file and about the session that replaced the
// one put away. Two of those lines are wrong on a page. The session that
// replaced it needs no announcing — the page is the announcement, and it is
// empty precisely because nothing has been said in it yet — and the file path
// is a receipt for a file the reader is not looking at. The headline is what
// survives, and /archive keeps its hint because /recover is the way back and
// the page is the last place the reader stands before the decision.
//
// /new says nothing at all: it left nothing behind to report.
func homeFlash(line string, report []string) string {
	fields := strings.Fields(line)
	if len(fields) == 0 || len(report) == 0 {
		return ""
	}
	if fields[0] == "/new" {
		return ""
	}
	if fields[0] == "/archive" {
		return report[0] + " · /recover opens it again"
	}
	return report[0]
}

func tuiMainTurn(ctx context.Context, application *app.App, line string, sink tui.Sink) error {
	// A reference that cannot be attached stops the turn here: the model would
	// read the sentence as prose and answer about a file it never saw, which is
	// the one failure a reader cannot detect from the answer.
	message, err := attachMessage(line, attachDir(application))
	if err != nil {
		return err
	}
	if _, err := tuiTurn(ctx, sink, func(turnCtx context.Context, emit func(wire.AgentEvent) error) (app.Result, error) {
		return application.Ask(turnCtx, message, emit)
	}); err != nil {
		return err
	}
	// A successful user turn hands back to the goal: the reader's message was
	// the answer the model was waiting for, and the goal keeps working unless
	// the model decided it is done.
	g, err := activeGoal(application)
	if err != nil {
		return err
	}
	if g != nil {
		turn := func(turnCtx context.Context, run func(context.Context, func(wire.AgentEvent) error) (app.Result, error)) (app.Result, error) {
			return tuiTurn(turnCtx, sink, run)
		}
		if err := goalLoop(ctx, application, g, sink.Note, turn, goalContinuePrompt); err != nil {
			return err
		}
	}
	return nil
}

func tuiSideTurn(ctx context.Context, conversation *app.Side, line string, sink tui.Sink) error {
	message, err := attachMessage(line, conversation.Session().Cwd())
	if err != nil {
		return err
	}
	if _, err := tuiTurn(ctx, sink, func(turnCtx context.Context, emit func(wire.AgentEvent) error) (app.Result, error) {
		return conversation.Ask(turnCtx, message, emit)
	}); err != nil {
		return err
	}
	return nil
}

// tuiTurn runs one turn and reports what the events cannot say: what it cost,
// and the failure behind a rejection.
//
// It is runTurn's shape without the signal handler. bubbletea owns the terminal
// and turns Ctrl-C into a key, so the cancellation arrives on the context the
// model created rather than as os.Interrupt. The result travels back for the
// callers that read it — the goal loop accounts tokens from it.
func tuiTurn(ctx context.Context, sink tui.Sink, run func(context.Context, func(wire.AgentEvent) error) (app.Result, error)) (app.Result, error) {
	timer := newTurnTimer()
	result, err := run(ctx, func(event wire.AgentEvent) error {
		timer.observe(event)
		sink.Event(event)
		return nil
	})

	// The inline session prints "[stop reason stop]" after every turn because
	// its transcript is a log and nothing else records that the turn ended. A
	// full-screen session does record it — the answer block closes and the
	// prompt comes back — so only the part the screen cannot show is worth a
	// notice: the tokens, the rate they arrived at, and when the turn stopped.
	//
	// A turn that spent nothing gets no notice. The failure that spent nothing
	// is already on screen in red, and a line under it measuring how long a
	// rejected request took to be rejected says nothing a reader wants.
	if usage := result.Usage; usage.TotalTokens > 0 {
		sink.Note(timer.cost().line(usage))
	}

	if err != nil {
		if ctx.Err() != nil {
			// Cancelled: the kernel reports the cancellation as the turn's
			// error, and it is one. Returning it is what puts the single
			// "interrupted" line in the transcript — the screen cannot tell a
			// stop the reader asked for from one it never saw, and silence
			// there looks like a turn that quietly did nothing.
			return result, ctx.Err()
		}
		return result, err
	}
	if result.StopReason != wire.StopError {
		return result, nil
	}
	if result.ErrorMessage != "" {
		return result, errors.New(result.ErrorMessage)
	}
	return result, errors.New("the turn failed without a reason")
}

// noteWriter turns a stream of text into transcript notices: one line in, one
// notice out. It exists because the command table and the summary write to an
// io.Writer, and the interface has no scrollback to write into.
type noteWriter struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	lines []string
	note  func(string)
}

func (w *noteWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf.Write(p)
	for {
		line, err := w.buf.ReadString('\n')
		if err != nil {
			// The tail is a partial line: put it back and wait for the rest.
			w.buf.Reset()
			w.buf.WriteString(line)
			return len(p), nil
		}
		w.emit(strings.TrimRight(line, "\n"))
	}
}

// Flush emits whatever the last Write left without a newline.
func (w *noteWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.buf.Len() == 0 {
		return
	}
	line := w.buf.String()
	w.buf.Reset()
	w.emit(line)
}

// take returns everything collected and forgets it. It is what a view switch
// uses: the notice has to land in the transcript of the view being entered, so it
// is carried there rather than written.
func (w *noteWriter) take() string {
	w.Flush()
	w.mu.Lock()
	defer w.mu.Unlock()
	out := strings.Join(w.lines, "\n")
	w.lines = nil
	return out
}

// takeLines returns what was collected as separate lines and forgets it, without
// writing it again. A session switch needs the lines as lines: they are replayed
// into the reset transcript one notice at a time.
func (w *noteWriter) takeLines() []string {
	w.Flush()
	w.mu.Lock()
	defer w.mu.Unlock()
	out := w.lines
	w.lines = nil
	return out
}

func (w *noteWriter) emit(line string) {
	line = strings.TrimRight(line, "\r")
	if strings.TrimSpace(line) == "" {
		return
	}
	w.lines = append(w.lines, line)
	if w.note != nil {
		w.note(line)
	}
}

// tuiStyles resolves the configured theme, then applies the config's own color
// overrides on top of it.
//
// The dark/light question is answered the same way the inline interface answers
// it — by assuming dark — until the terminal is asked directly; a theme file
// supplies its own contrast either way.
func tuiStyles(cfg *config.Config, cwd string, stdio streams) (tui.Styles, error) {
	home, err := config.DefaultHome()
	if err != nil {
		return tui.Styles{}, err
	}
	library, err := theme.NewLibrary(theme.Dirs(home, cwd), true)
	if err != nil {
		return tui.Styles{}, err
	}
	for _, broken := range library.Broken() {
		fmt.Fprintf(stdio.err, "theme: %s: %v\n", broken.Name, broken.Err)
	}
	resolved, err := library.Resolve(cfg.UI.Theme)
	if err != nil {
		return tui.Styles{}, err
	}
	// A color in the config is a change to the theme rather than a second
	// theme, so it is merged here: /theme still names one palette, and the
	// override is what the reader wanted different about it.
	painted, err := resolved.Override(cfg.UI.Colors)
	if err != nil {
		return tui.Styles{}, fmt.Errorf("config: tui: colors: %w", err)
	}
	return tui.NewStyles(painted), nil
}

// tuiLayout turns the config's geometry into the interface's.
//
// It starts from the defaults and changes only what the file named, because
// every field of the file is optional: a config that mentions one gap must not
// reset the other eight to zero. That is also why the config holds pointers and
// this does not — "not written" ends here.
//
// The file groups its numbers by the block they belong to and the interface
// keeps one flat value, so this walk is where the two shapes meet. The
// flattening is mechanical on purpose: the regions are read through the block
// name the reader wrote, so a rule about the composer moves ComposerRows
// without a name that has to be matched back by hand.
func tuiLayout(cfg *config.Config) tui.Layout {
	layout := tui.DefaultLayout()
	if cfg == nil {
		return layout
	}
	blocks := cfg.UI.Blocks
	if value := blocks.Transcript.Gap; value != nil {
		layout.BlockGap = *value
	}
	if value := blocks.Transcript.Frames; value != nil {
		layout.TranscriptFrames = *value
	}
	if value := blocks.Composer.GapAbove; value != nil {
		layout.ComposerGap = *value
	}
	if value := blocks.Composer.Rows; value != nil {
		layout.ComposerRows = *value
	}
	if value := blocks.Composer.Frame; value != nil {
		layout.ComposerFrame = *value
	}
	if value := blocks.Palette.Rows; value != nil {
		layout.PaletteRows = *value
	}
	if value := blocks.Status.GapAbove; value != nil {
		layout.StatusGap = *value
	}
	if value := blocks.Menu.Rows; value != nil {
		layout.MenuRows = *value
	}
	if value := blocks.Menu.Width; value != nil {
		layout.MenuWidth = *value
	}
	if value := blocks.Approval.Width; value != nil {
		layout.BoxWidth = *value
	}
	if value := blocks.Welcome.On; value != nil {
		layout.HomePage = *value
	}
	// Reasoning is the one survivor of the flat list that is not a region: it
	// lives inside the transcript, so it is a role rather than a block.
	if value := cfg.UI.Roles.Thinking.Rows; value != nil {
		layout.ThinkingRows = *value
	}
	return layout
}
