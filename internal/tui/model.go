package tui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"

	"pi-go/internal/wire"
)

// The two conversations the interface can show. They are the same shape and
// differ only in which transcript is on screen: the side view is a second buffer
// over the same program, not a second program.
const (
	ViewMain = 0
	ViewSide = 1
)

// Sink is how a running turn reports back to the interface. All three functions
// are safe to call from the turn's own goroutine: each one only posts a message.
type Sink struct {
	// Event is one agent event.
	Event func(wire.AgentEvent)
	// Note is one line of ordinary output: the answer to a slash command, a
	// notice the host wants in the transcript.
	Note func(text string)
	// Answer puts a recorded answer into the transcript. It is separate from
	// Note because the transcript draws the two shapes differently — an answer
	// gets the bar down its left edge and wraps inside it — and a replayed turn
	// has to arrive looking like the live turn it was.
	Answer func(text string)
	// Ask blocks until the reader answers a question about a call that would
	// change something. The answer has three states rather than two, and the
	// third one is scoped by the request: the reader says "not this once, but
	// every time for this tool until the session ends", and it is the host that
	// remembers it, because forgetting it at the end of a session is a decision
	// about a session.
	Ask func(req AskRequest) AskAnswer
	// Pick asks the reader to take one row from a list, and blocks until they
	// do. It reports Taken=false when the list was dismissed or nobody could
	// answer: both mean the command has nothing to do, so the caller cannot
	// tell them apart and does not need to.
	Pick func(req PickRequest) PickAnswer
	// View moves the prompt to the conversation a command just opened or
	// closed. The host owns that state; the interface follows it.
	View func(index int)
	// Reset drops one conversation's transcript. A command that swapped the
	// session out from under it needs this: the lines belong to a conversation
	// that is no longer open, and that conversation is not always the one the
	// command was typed in — /side replaces the side view from the main prompt.
	Reset func(view int)
	// Flash puts one line on the home screen: the receipt for a command that
	// left a session with nothing in it. It is separate from Note for the same
	// reason the page is separate from the transcript — a note becomes a block,
	// and a block is what the page is not — and it is spent by the next line
	// the reader submits, because by then the page's last event is the turn
	// that is running.
	Flash func(text string)
	// Compose puts text in the input line, ready to be edited. It is not a
	// note: a command that forks before a message hands that message back as a
	// draft, because the reason to fork there is to say it differently, and a
	// copy in the transcript would be a copy the reader has to retype. The
	// draft replaces what was in the box: the two are answers to the same
	// question — what am I about to send — and a command that appended to a
	// half-typed sentence would produce one nobody wrote.
	Compose func(text string)
}

// ErrExit ends the session. A turn returns it when the line it ran was the one
// that means "leave", so that the decision stays with the host and the interface
// only has to stop.
var ErrExit = errors.New("exit")

// Runner performs one submitted line. cmd/atlas binds it to the command table
// and to app.Ask; the interface knows neither.
type Runner func(ctx context.Context, prompt Prompt, sink Sink) error

// SubagentRuntime provides session identity and result-turn admission to the screen.
type SubagentRuntime struct {
	Session func() (id, file string)
	Ready   func() bool
	Run     func(context.Context, Sink) error
}

// Config is what the interface needs to start.
type Config struct {
	// Context is the lifetime of the screen's turns.
	Context context.Context
	Styles  Styles

	// Layout is the geometry — the gaps, the boxes, how much reasoning is
	// shown. An all-zero value is DefaultLayout(), so a host with no opinion
	// leaves it out; a host with one starts from DefaultLayout() and changes
	// the fields it means, because a field it leaves zero is taken as zero.
	Layout Layout

	Title string

	// Status draws the bottom line: the facts about the session, without the
	// busy marker. That marker is not here because only this package has the
	// turn's clock — a host that was handed a boolean could say "working" but
	// not "working for four seconds" — and it is appended by statusLine after
	// this returns, using the width that is left.
	Status func(width int) string

	// Run handles one submitted line.
	Run Runner

	// Subagents supports background updates and internally initiated main turns.
	Subagents *SubagentRuntime

	// Toggle moves the prompt to the other conversation. It reports which one it
	// is now, and the line the switch is worth saying: the host owns the
	// decision, because which conversation the prompt is in is a fact about the
	// session rather than about a buffer.
	Toggle func() (view int, note string)

	// Back steps the prompt out of the conversation it is in, one level: the
	// side conversation goes back to the main one. It is what an idle Ctrl+C
	// means, and it reports ok=false when there is nowhere to step back to —
	// the main conversation — where the key keeps its other meaning of "leave
	// the session after two presses".
	//
	// Leaving is not discarding: the host keeps the conversation, and each view
	// keeps its own half-typed line, so a press that lands on the wrong thing
	// costs a keystroke to undo rather than a conversation.
	Back func() (view int, note string, ok bool)

	// Suggest returns the commands the line could become, with the one line that
	// says what each does. It is a callback rather than a table because the
	// command table lives in the host: the interface has never known a command
	// name, and a copy here would be the second answer to "what exists".
	Suggest func(line string) []Suggestion

	// Attach returns the files the reference the reader is typing could become.
	// It is the same palette as Suggest — this is the second provider feeding
	// it, not a second list — and it gets the first look: a host that reports
	// candidates here is saying the end of the line is a file reference right
	// now, and that the command table has nothing to say about it.
	//
	// The interface does not know what a reference looks like. The "@" is the
	// host's, and so is what counts as a path; all this side needs is the
	// suggestions and how much of the line each one replaces.
	Attach func(line string) []Suggestion

	// IsImage checks the file behind a selected suggestion before staging it.
	IsImage func(path string) bool

	// Welcome is the home screen for a view: what it says about this
	// conversation before the first line is typed. It is asked per frame
	// rather than handed over once because the side conversation knows what it
	// inherited only when it is opened, after the program is already running.
	// Nil leaves the screen empty, which is what a transcript with nothing in
	// it already looks like.
	Welcome func(view int) *Welcome

	// Interrupt runs after the turn's context is cancelled, and returns the
	// lines the host was still holding for it. Queued steering and follow-up
	// messages belong to the turn that was just abandoned: delivering them to
	// the next one would say words the reader had already decided to take back,
	// so the interface puts them in the composer instead of dropping them.
	Interrupt func() []string

	// Placeholder is the dim text in an empty composer.
	Placeholder string
}

// EventMsg carries one agent event into the update loop.
type EventMsg struct {
	View  int
	Event wire.AgentEvent
}

// NoteMsg carries one line of ordinary output into the update loop.
//
// A note belongs to the conversation the reader is looking at, not to the one the
// turn started in: a command that moves the prompt between conversations — /side,
// /parent, Ctrl+O — says so in the conversation it moved to, and the line would
// land in a transcript nobody is reading if it were pinned to the old view.
type NoteMsg struct {
	Text string
	view *int
}

type subagentMsg struct{ update wire.SubagentUpdate }

type checkSubagentResultsMsg struct{}

// AnswerMsg replays one answer into the transcript. It carries the text of an
// answer that happened before this screen existed, so the block it appends is
// already closed: nothing streams into it.
//
// It is the replay path's message — the only producer is writeTranscript in
// cmd/atlas, and a live turn streams through EventMsg — so trimming the draw
// window here cannot clip a conversation that is still being written.
type AnswerMsg struct {
	Text string
}

// ResetMsg drops the transcript of one conversation.
type ResetMsg struct {
	View int
}

// FlashMsg sets the one line the home screen says about what just happened.
//
// It belongs to the screen rather than to a conversation's transcript: it is
// drawn where the transcript is empty, and the next submitted line clears it.
type FlashMsg struct {
	Text string
}

// ViewMsg moves the prompt to another conversation.
type ViewMsg struct {
	Index int
}

// ComposeMsg puts text in the input line of the conversation on screen.
type ComposeMsg struct {
	Text string
}

// TurnMsg closes a turn.
type TurnMsg struct {
	View int
	Err  error
}

// AskMsg asks the reader whether a call may run. The turn goroutine is blocked
// on Reply while the question is shown, so it must be answered exactly once.
type AskMsg struct {
	Request AskRequest
	Reply   chan AskAnswer
}

// AskRequest is one gate question: what the call would do, and the tool it would
// do it with.
//
// Tool is separate from Subject because it is the scope of the third answer: "no
// more asking about this one, for the rest of this session". An empty Tool means
// the question does not offer that answer, and the box says so by not naming a
// key for it — a scope that cannot be written into the footer is a scope nobody
// agreed to.
type AskRequest struct {
	Subject string
	Tool    string
}

// AskAnswer is the reader's reply.
//
// Always is only meaningful when Allowed is true. It is the reader asking to be
// told no more about this tool until the session ends — not about every tool,
// and not about the next session, which is why the interface passes both facts
// along instead of deciding what the pair means.
type AskAnswer struct {
	Allowed bool
	Always  bool
}

// busyTickMsg is one beat of the busy indicator. It carries the turn it was
// started for, so a beat that outlives its turn cannot count towards the next
// one — and cannot start a second chain of beats either.
type busyTickMsg struct {
	turn int
}

// clearRequest asks for a full repaint. It is a message rather than a command so
// that it arrives through the one channel the model reads: the model that queued
// it must not also have to own a second outstanding read.
type clearRequest struct{}

// host is the part of the model that a background goroutine can reach. Models
// are copied on every update, so the channel the turn writes to has to live
// behind a pointer.
type host struct {
	events chan tea.Msg
	// armed guards the event chain: exactly one command may be waiting on the
	// channel at a time. Without it, every key pressed during a turn would arm
	// another reader, and the extra ones would stay blocked forever.
	armed bool

	// queued records that a repaint request is already in the channel. The
	// renderer paints only what changed, and a line it decides moved can be a
	// line it moved wrong: text that arrives in a burst is where that shows, so
	// one clean frame is asked for after the burst rather than trusting every
	// intermediate one.
	queued bool
}

// Model is the screen: a scrolling transcript, an input line, a status bar, and
// the modal that asks about a tool call.
type Model struct {
	cfg  Config
	host *host

	width, height int
	views         [2]view
	active        int

	busy   bool
	cancel context.CancelFunc
	// turnView is the conversation the running turn belongs to. It is recorded
	// because the prompt can move between views while a turn streams, and the
	// transcript that has to be told the turn was stopped is the one the turn
	// started in.
	turnView     int
	internalTurn bool

	mainSessionID, mainSessionFile string
	subagentWake                   bool

	// picks is the command palette for what is in the composer, and picked is
	// the row Tab and Enter would take. It is not part of view: two views can
	// hold two drafts, but only one of them is on screen.
	picks  []Suggestion
	picked int
	// attach records that picks came from the file provider. Enter treats the
	// two differently, and it is the only place that does: a command is the
	// whole line, so filling one in finishes the thought, while a file
	// reference is one word of a sentence that is still being written.
	attach bool

	// exitArmed records that the last key was a Ctrl+C on an idle prompt, which
	// is what makes the next one leave. Any other key disarms it: the gesture is
	// "press it twice", not "press it twice within a second", and a key in
	// between is the reader saying they are still here.
	exitArmed bool

	approval *AskMsg
	// chooser is the list the command that just ran is waiting on, and chosen
	// is the row it would take. A menu and an approval question are both
	// modals and cannot be up at once: both block the same turn goroutine.
	chooser *PickMsg
	chosen  int
	// query is what the reader has typed into the open list, and matches are
	// the indexes of the rows that answer it, in the host's order. chosen
	// indexes matches rather than the host's slice: everything the box draws
	// and everything a key acts on is a row the query left, and the mapping
	// back to the host's row is the one thing that crosses between them.
	query   string
	matches []int

	// busySeq counts the turns this program has started and busyTicks counts the
	// frames of the current one. The pair is the busy indicator: the tick that
	// redraws the footer carries the turn it belongs to, so the chain of ticks
	// is exactly one per turn and exactly none while nothing runs.
	busySeq   int
	busyTicks int

	quit bool

	// flash is the one line the home screen says about what just happened, and
	// it is empty almost always: the page describes what is about to start, and
	// the only exceptions are the commands that leave a session with nothing in
	// it. The next submitted line clears it — the reader has moved on from the
	// event to what they are saying about it.
	flash string

	// copied is the receipt for a copy: how much went to the clipboard. It takes
	// the status bar's tail, which is the slot the busy marker uses, because the
	// two cannot be true at once — a drag while a turn runs still copies, and
	// the turn's clock is the more important of the two facts. The next key or
	// the next press clears it: a receipt that stayed would be a line about the
	// frame before last.
	copied string
}

// view is one conversation's own state. Scrolling and the half-typed line are
// per view: switching to look at the side conversation must not move the main
// transcript or throw away what was being typed there.
type view struct {
	transcript Transcript
	composer   Composer
	offset     int
	// sel is the stretch of this conversation the mouse has picked out. It is
	// per view for the same reason the offset is: the two conversations scroll
	// differently, so a selection made in one would name unrelated words in the
	// other.
	sel selection
}

// New builds the interface.
func New(cfg Config) Model {
	if cfg.Context == nil {
		cfg.Context = context.Background()
	}
	if cfg.Placeholder == "" {
		cfg.Placeholder = "Ask anything · /help for commands"
	}
	cfg.Layout = resolveLayout(cfg.Layout)
	m := Model{cfg: cfg, host: &host{events: make(chan tea.Msg, 1024)}}
	for i := range m.views {
		m.views[i].transcript = NewTranscript()
		m.views[i].composer = NewComposer()
	}
	m.refreshSubagentSession()
	return m
}

// Init implements tea.Model and checks for results in the starting session.
func (m Model) Init() tea.Cmd {
	if m.cfg.Subagents == nil {
		return nil
	}
	return func() tea.Msg { return checkSubagentResultsMsg{} }
}

// SendSubagentUpdate queues a background update for the screen's update loop.
func (m Model) SendSubagentUpdate(ctx context.Context, update wire.SubagentUpdate) {
	select {
	case m.host.events <- subagentMsg{update: update}:
	case <-ctx.Done():
	}
}

// Update implements tea.Model.
//
// It wraps handle so that a turn in flight always has exactly one listener: any
// message that does not produce a command of its own re-arms the chain, which is
// what keeps events arriving while the reader types.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.handle(msg)
	// The palette follows the composer, and the composer changes on almost every
	// message: a paste, a key, a history walk, and a view switch all move the
	// line the list is about. Refreshing here rather than at each of those call
	// sites is what keeps a new way of typing from silently losing the list.
	next.refreshSuggest()
	if cmd == nil {
		return next, next.listen()
	}
	return next, cmd
}

func (m Model) handle(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		// A reader looking at the top of the window meets more of the
		// conversation when the window grows: the extra rows are history, not
		// blank padding.
		m.revealIfAtTop()
		return m, nil

	case tea.PasteMsg:
		if !m.busy || (m.internalTurn && !m.modal()) {
			m.composer().Insert(msg.Content)
		}
		return m, nil

	case EventMsg:
		m.host.armed = false
		m.view(msg.View).transcript.Apply(msg.Event)
		return m, nil

	case subagentMsg:
		m.host.armed = false
		update := msg.update
		if m.mainSessionID == "" || update.SessionID != m.mainSessionID || update.SessionFile != m.mainSessionFile {
			return m, nil
		}
		m.view(ViewMain).transcript.showSubagent(update, false)
		if subagentFinished(update.State) {
			if m.busy {
				m.subagentWake = true
				return m, nil
			}
			return m.startSubagentResults()
		}
		return m, nil

	case checkSubagentResultsMsg:
		return m.startSubagentResults()

	case NoteMsg:
		m.host.armed = false
		index := m.active
		if msg.view != nil {
			index = *msg.view
		}
		m.view(index).transcript.Append(Block{Role: RoleNotice, Text: msg.Text})
		return m, m.repaint()
	case AnswerMsg:
		m.host.armed = false
		m.view(m.active).transcript.Append(Block{Role: RoleAnswer, Text: msg.Text})
		// A resumed session arrives as a burst of closed answers, and each
		// intermediate frame would redraw everything appended so far. The
		// window keeps only the newest answer drawn; the rest is met by
		// scrolling to the top.
		m.view(m.active).transcript.TrimAnswer()
		return m, m.repaint()
	case clearRequest:
		m.host.armed = false
		m.host.queued = false
		// The renderer repaints the whole screen for this one, which is what
		// undoes a line the incremental path moved wrong. bubbletea calls Update
		// for it too, so the chain keeps its reader.
		return m, func() tea.Msg { return tea.ClearScreen() }

	case ResetMsg:
		m.host.armed = false
		if msg.View < 0 || msg.View >= len(m.views) {
			return m, nil
		}
		m.view(msg.View).transcript.Reset()
		m.view(msg.View).offset = 0
		if msg.View == ViewMain {
			// The host may still be replaying the new session. Bind again when
			// the command releases the turn slot.
			m.mainSessionID, m.mainSessionFile = "", ""
		}
		return m, nil

	case FlashMsg:
		m.host.armed = false
		m.flash = msg.Text
		return m, nil

	case ComposeMsg:
		m.host.armed = false
		m.composer().Set(msg.Text)
		return m, nil

	case ViewMsg:
		m.host.armed = false
		if msg.Index >= 0 && msg.Index < len(m.views) {
			m.active = msg.Index
		}
		return m, nil

	case busyTickMsg:
		// The indicator is the one thing in this program that redraws without
		// the reader doing anything, so it exists only while there is something
		// to report. A beat from a turn that is over, or from a turn that is no
		// longer the current one, ends its chain here.
		if !m.busy || msg.turn != m.busySeq {
			return m, nil
		}
		m.busyTicks++
		return m, busyTick(m.busySeq)

	case TurnMsg:
		m.busy = false
		m.cancel = nil
		m.host.armed = false
		// Whatever the event stream left open is closed here: a turn that was
		// cancelled never sends agent_end, and a block left open would spin
		// forever.
		m.view(msg.View).transcript.Finish()
		if errors.Is(msg.Err, ErrExit) {
			m.quit = true
			return m, tea.Quit
		}
		if msg.Err != nil {
			m.note(msg.View, msg.Err)
		}
		checkResults := !errors.Is(msg.Err, context.Canceled) && (!m.internalTurn || msg.Err == nil || m.subagentWake)
		m.internalTurn = false
		m.refreshSubagentSession()
		m.repaint()
		if checkResults {
			return m.startSubagentResults()
		}
		return m, nil

	case AskMsg:
		m.host.armed = false
		if m.approval != nil || m.chooser != nil || m.busy && m.cancel == nil {
			// A second question cannot be answered: the modal on screen owns
			// the keys. Denying is the only safe default when the reader
			// cannot be asked.
			answer(msg.Reply, AskAnswer{})
			return m, nil
		}
		copied := msg
		m.approval = &copied
		return m, nil

	case PickMsg:
		m.host.armed = false
		if m.chooser != nil || m.approval != nil || m.busy && m.cancel == nil {
			// One list at a time, for the same reason: one modal, one answer.
			sendPick(msg.Reply, PickAnswer{})
			return m, nil
		}
		copied := msg
		// The rows are the host's slice, and the axis positions below are
		// written as the reader presses: storing a copy is what keeps a menu
		// from editing the caller's list.
		copied.Request.Items = slices.Clone(msg.Request.Items)
		m.chooser = &copied
		// A list always opens unfiltered: a query belongs to the menu that is
		// up, and a menu that opened already narrowed by the last one's search
		// would look like it was missing rows.
		m.query = ""
		// Every row matches an empty query, and a fresh slice keeps this list's
		// search from writing into a copy of the model left behind by the last
		// one (the same reason refilter allocates).
		m.matches = make([]int, len(copied.Request.Items))
		for i := range m.matches {
			m.matches[i] = i
		}
		m.chosen = pickStart(&copied)
		return m, nil

	case tea.MouseWheelMsg:
		switch msg.Mouse().Button {
		case tea.MouseWheelUp:
			m.scrollBy(3)
		case tea.MouseWheelDown:
			m.scrollBy(-3)
		}
		return m, nil

	case tea.MouseClickMsg:
		return m.click(msg.Mouse())

	case tea.MouseMotionMsg:
		return m.drag(msg.Mouse())

	case tea.MouseReleaseMsg:
		return m.release(msg.Mouse())

	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

// click starts a selection, or folds a stretch, and which of the two it is
// depends on how the press ends: a press that lets go in the same cell is a
// click, and a press that moves is a drag. The distinction is made on release
// rather than here, because at this moment a click and the first half of a drag
// are the same event.
//
// While a modal is up the mouse is ignored. The question or the list owns the
// screen while it is there, and a selection made in the rows behind it would be
// a selection of text the reader cannot see.
func (m Model) click(mouse tea.Mouse) (Model, tea.Cmd) {
	if mouse.Button != tea.MouseLeft || m.modal() {
		return m, nil
	}
	if m.downArrowAt(mouse.X, mouse.Y) {
		return m.jumpToNewest()
	}
	if m.upArrowAt(mouse.X, mouse.Y) {
		return m.jumpToOldest()
	}
	row, ok := m.transcriptRow(mouse.Y)
	if !ok {
		// A press below the transcript is not a selection: there is nothing
		// there to select.
		return m, nil
	}
	m.copied = ""
	m.activeView().sel = selection{
		anchor: point{row: row, cell: mouse.X},
		cursor: point{row: row, cell: mouse.X},
		held:   true,
	}
	return m, nil
}

// downArrowAt reports that the pointer is on the arrow drawn when the window
// has scrolled back into the conversation: the last cell of the last row of
// the body. The arithmetic mirrors the one arrowLine uses, and the pair
// has to agree — a click on a cell the frame never painted would be a press
// the reader cannot see land.
func (m *Model) downArrowAt(x, y int) bool {
	return m.activeView().offset > 0 && y == m.bodyHeight()-1 && x == m.width-1
}

// upArrowAt reports that the pointer is on the arrow drawn when the window
// still holds older conversation back or has scrolled off the first row: the
// last cell of the first row of the body. The arithmetic mirrors the one
// arrowLine uses, and the pair has to agree — a click on a cell the frame
// never painted would be a press the reader cannot see land.
func (m *Model) upArrowAt(x, y int) bool {
	v := m.activeView()
	return (v.transcript.Hidden() > 0 || v.offset > 0) && y == 0 && x == m.width-1
}

// jumpToNewest scrolls the window back to the newest message. The offset is
// the distance from the newest row, and the selection is cleared because it
// named words in the history the jump left: left in place it would paint
// unrelated words over the rows now on screen.
func (m Model) jumpToNewest() (Model, tea.Cmd) {
	v := m.activeView()
	v.offset = 0
	v.sel.clear()
	return m, nil
}

// jumpToOldest shows the whole conversation and scrolls to its first row.
// The held-back stretches are released in chunks first: the first row has a
// place to scroll to only once nothing is hidden, and the offset is the
// distance from the newest row of that whole. The selection is cleared
// because it named words in the history the jump left.
func (m Model) jumpToOldest() (Model, tea.Cmd) {
	v := m.activeView()
	for v.transcript.Hidden() > 0 {
		v.transcript.Reveal(revealChunkSpans)
	}
	v.offset = m.maxOffset()
	v.sel.clear()
	return m, nil
}

// drag extends the selection the press started. The cell is not clamped here:
// how wide a row is is a fact about the row, and the renderer is where it is
// known.
//
// A drag that leaves the transcript still extends. The window is a hole in a
// longer conversation, and a pointer past the top of it means "up there", so the
// row is pinned to the first or the last one on screen rather than the selection
// stopping at the last row the pointer happened to be over.
func (m Model) drag(mouse tea.Mouse) (Model, tea.Cmd) {
	v := m.activeView()
	if !v.sel.held || m.modal() {
		return m, nil
	}
	row, ok := m.clampedTranscriptRow(mouse.Y)
	if !ok {
		return m, nil
	}
	v.sel.cursor = point{row: row, cell: mouse.X}
	return m, nil
}

// release ends a gesture: a press that never moved folds the stretch it landed
// on, and a press that moved copies what it covered.
//
// Folding is on the release rather than on the press for the reason above — the
// two gestures only differ once they are over — and the row it lands on has to
// be the stretch's own first row, which is the box's title. Clicking the body of
// a box starts a selection instead, because that is the text the reader wanted.
func (m Model) release(mouse tea.Mouse) (Model, tea.Cmd) {
	v := m.activeView()
	if !v.sel.held || m.modal() {
		return m, nil
	}
	v.sel.held = false
	if v.sel.empty() {
		row := v.sel.anchor.row
		v.sel.clear()
		return m.fold(row)
	}
	text := selectionText(m.transcriptLines(m.active, m.width), v.sel)
	if text == "" {
		v.sel.clear()
		return m, nil
	}
	m.copied = copiedField(text)
	// The reader stays armed: Batch drops the nil and the two commands do not
	// compete — one writes the clipboard and the other keeps waiting for the
	// next event, and a frame that returned only the clipboard command would
	// leave a turn's events with no reader.
	return m, tea.Batch(tea.SetClipboard(text), m.listen())
}

// fold toggles the stretch whose title is on row, if that row is a title.
func (m Model) fold(row int) (Model, tea.Cmd) {
	frame := m.views[m.active].transcript.Frame(m.width, m.cfg.Styles, m.cfg.Layout)
	index, ok := frame.SpanAt(row)
	if !ok {
		return m, nil
	}
	if rows, ok := frame.RowsFor(index); !ok || rows.First != row {
		// The body of a box is not a handle: a click there is the start of a
		// selection that happened to end where it began.
		return m, nil
	}
	if !m.activeView().transcript.ToggleSpan(index) {
		return m, nil
	}
	// Folding moves every row under the one that was clicked, so the frame is
	// redrawn whole: the incremental path was told about rows that are no longer
	// there.
	return m, m.repaint()
}

// copiedField is the receipt for a copy. It counts characters rather than rows
// because that is the number a reader can compare against what they meant to
// take, and it says the destination, because a clipboard that quietly does
// nothing — an terminal with no OSC 52 — is otherwise indistinguishable from one
// that worked.
func copiedField(text string) string {
	count := len([]rune(text))
	if count == 1 {
		return "copied 1 char"
	}
	return fmt.Sprintf("copied %d chars", count)
}

// handleKey is the whole keymap. The modal comes first: while a question is on
// screen, every other key is a mistake and must not reach the composer.
func (m Model) handleKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	key := msg.Key()
	composer := m.composer()

	ctrl := key.Mod&tea.ModCtrl != 0
	alt := key.Mod&tea.ModAlt != 0
	shift := key.Mod&tea.ModShift != 0
	ctrlC := ctrl && isRune(key, 'c')

	// Only another Ctrl+C keeps the exit armed; anything else is the reader
	// going back to work.
	if !(ctrl && isRune(key, 'c')) {
		m.exitArmed = false
	}
	// A receipt is about the last gesture, so the next key ends it. It is
	// cleared here rather than where the frame is drawn because "how long a
	// receipt stays" is a question about keys, not about painting.
	m.copied = ""
	// The list comes first: it is a modal too, and while it is up the answer is
	// a whole row rather than a line.
	if m.chooser != nil {
		return m.handlePick(key)
	}

	if m.busy && (ctrlC || key.Code == tea.KeyEscape && m.approval == nil) {
		if m.interrupt() {
			return m, m.repaint()
		}
		return m, nil
	}

	if m.approval != nil {
		switch {
		case isRune(key, 'y'), isRune(key, 'Y'):
			answer(m.approval.Reply, AskAnswer{Allowed: true})
			m.approval = nil
		case isRune(key, 'a'), isRune(key, 'A'):
			// "Yes, and stop asking" is only on offer when the question named
			// the tool it would cover. A request without one gets no answer
			// from this key: a key that quietly means "yes" would teach the
			// reader to press it for a promise nobody scoped.
			if m.approval.Request.Tool == "" {
				return m, nil
			}
			answer(m.approval.Reply, AskAnswer{Allowed: true, Always: true})
			m.approval = nil
		case isRune(key, 'n'), isRune(key, 'N'), key.Code == tea.KeyEnter,
			key.Code == tea.KeyEscape, key.Code == tea.KeyBackspace:
			answer(m.approval.Reply, AskAnswer{})
			m.approval = nil
		default:
			// Anything else is not an answer, so the question stays up: a
			// finger slipping onto the wrong key should not decide a write.
			return m, nil
		}
		// The answer closes the box, and the frame that follows is shorter
		// than the one before it on every row the box covered. Repainting the
		// screen is the one way to be sure the transcript comes back in the
		// right order: the incremental path was told the box had rows the
		// transcript does not.
		return m, m.repaint()
	}

	switch {
	case ctrl && isRune(key, 'c'):
		if ok, cmd := m.stepBack(); ok {
			return m, cmd
		}
		if m.exitArmed {
			m.quit = true
			return m, tea.Quit
		}
		// The first press clears what is there and arms the second one.
		// Clearing and arming are the same gesture on purpose: a reader who
		// wanted the line back has to press again to lose the session, and
		// an empty prompt still costs two presses so that muscle memory
		// from the prompt cannot end a conversation by accident.
		composer.Clear()
		m.exitArmed = true
		return m, nil

	case ctrl && isRune(key, 'd'):
		if composer.Len() == 0 && !m.busy {
			m.quit = true
			return m, tea.Quit
		}
		return m, nil

	case ctrl && isRune(key, 'o'):
		// Switching conversations in the middle of one would send the answer to
		// a view nobody is looking at, so the gesture waits for the turn.
		if !m.busy {
			m.switchView()
		}
		return m, nil

	case ctrl && isRune(key, 'g'):
		// The keyboard half of the fold: a click names one stretch, and this
		// names all of them at once. It is the gesture a reader wants on a turn
		// that has grown past a screenful — "show me the shape of it" — and
		// pressing it again puts the detail back.
		return m.foldAll()

	case key.Code == tea.KeyEscape:
		// Escape only ever means "stop this": there is no mode to leave and
		// nothing else it could be. With nothing running there is nothing to
		// stop, so the key is spent rather than passed on — and a selection left
		// over from a copy is the one thing on screen it can still put away.
		if !m.activeView().sel.empty() || m.activeView().sel.held {
			m.activeView().sel.clear()
		}
		return m, nil
	}

	switch key.Code {
	case tea.KeyEnter:
		if m.busy {
			return m, nil
		}
		// Enter takes the highlighted suggestion when the line is an unfinished
		// name of one: "/mo" is not a command, and submitting it fails on
		// something the reader was in the middle of saying.
		//
		// A file reference is the exception: filling one in completes a word in
		// the middle of a sentence, so the sentence is not finished and Enter
		// spends itself on the word. Pressing it again sends, because by then
		// there is nothing left to fill.
		if m.complete(false) && m.attach {
			return m, nil
		}
		prompt, ok := composer.Take()
		if !ok {
			return m, nil
		}
		return m.start(prompt)

	case tea.KeyTab:
		m.complete(true)
		return m, nil

	case tea.KeyUp:
		switch {
		case shift || alt:
			m.scrollBy(1)
		case !m.pick(-1):
			composer.Previous()
		}
		return m, nil

	case tea.KeyDown:
		switch {
		case shift || alt:
			m.scrollBy(-1)
		case !m.pick(1):
			composer.Next()
		}
		return m, nil

	case tea.KeyPgUp:
		m.scrollBy(m.bodyHeight())
		return m, nil

	case tea.KeyPgDown:
		m.scrollBy(-m.bodyHeight())
		return m, nil

	case tea.KeyHome:
		if ctrl {
			// Home asks for the top of the conversation, and a chunk of it may
			// still be held back: reveal one and land on the new top. Repeated
			// presses walk the whole history.
			if m.activeView().transcript.Hidden() > 0 {
				m.activeView().transcript.Reveal(revealChunkSpans)
				m.activeView().sel.clear()
			}
			m.activeView().offset = m.maxOffset()
		} else {
			composer.Home()
		}
		return m, nil

	case tea.KeyEnd:
		if ctrl {
			m.activeView().offset = 0
		} else {
			composer.End()
		}
		return m, nil

	case tea.KeyLeft:
		composer.Left()
		return m, nil

	case tea.KeyRight:
		composer.Right()
		return m, nil

	case tea.KeyBackspace:
		composer.Backspace()
		return m, nil

	case tea.KeyDelete:
		composer.Delete()
		return m, nil
	}

	switch {
	case ctrl && isRune(key, 'a'):
		composer.Home()
	case ctrl && isRune(key, 'e'):
		composer.End()
	case ctrl && isRune(key, 'u'), ctrl && isRune(key, 'w'):
		composer.DeleteWord()
	default:
		// Typing while a turn streams is allowed: the line waits in the box. The
		// keys that are commands were taken above, so what reaches here is text.
		if key.Text != "" && !alt {
			composer.Insert(key.Text)
		}
	}
	return m, nil
}

// handlePick is the keymap of the list. Arrows move through the rows the query
// left, Enter takes one, typed characters narrow them, and anything that means
// "not this" closes the list. The line the command came in on stays gone: these
// keys never reach the composer, so a query cannot be mistaken for the start of
// the next message.
func (m Model) handlePick(key tea.Key) (Model, tea.Cmd) {
	count := len(m.matches)
	switch key.Code {
	case tea.KeyUp:
		m.chosen = wrapIndex(m.chosen-1, count)
		return m, nil
	case tea.KeyDown:
		m.chosen = wrapIndex(m.chosen+1, count)
		return m, nil
	case tea.KeyPgUp:
		m.chosen = clampIndex(m.chosen-m.cfg.Layout.MenuRows, count)
		return m, nil
	case tea.KeyPgDown:
		m.chosen = clampIndex(m.chosen+m.cfg.Layout.MenuRows, count)
		return m, nil
	case tea.KeyHome:
		m.chosen = 0
		return m, nil
	case tea.KeyEnd:
		m.chosen = clampIndex(count-1, count)
		return m, nil
	case tea.KeyEnter:
		return m.takePick()
	case tea.KeyLeft:
		return m.stepPick(-1)
	case tea.KeyRight:
		return m.stepPick(1)
	case tea.KeyBackspace:
		return m.widenPick()
	case tea.KeyEscape:
		return m.dropPick()
	}
	if key.Mod&tea.ModCtrl != 0 {
		if isRune(key, 'c') {
			// Ctrl+C closes the list rather than the session. The key means
			// "stop what is happening", and what is happening is a question;
			// the exit gesture costs two presses, and a modal is not one of
			// its states.
			return m.dropPick()
		}
		return m, nil
	}
	// Typing is the query. Every printable key that is not part of the keymap
	// above narrows the list, and a key that is neither a character nor part of
	// the keymap is a slip of the finger: the list stays up and nothing
	// changes.
	if text := key.Text; text != "" && !hasControl(text) {
		m.query += text
		m.refilter()
	}
	return m, nil
}

// widenPick gives the query back its last character.
//
// At the start of a query it does nothing rather than closing the list. Backspace
// used to be one of the ways out, and it cannot stay one: a reader who holds it
// down past the beginning of a query would throw away the menu they were still
// reading. Esc is the way out, and it is the one the footer names.
func (m Model) widenPick() (Model, tea.Cmd) {
	typed := []rune(m.query)
	if len(typed) == 0 {
		return m, nil
	}
	m.query = string(typed[:len(typed)-1])
	m.refilter()
	return m, nil
}

// refilter recomputes the rows the query leaves and keeps the keys on the row
// they were on when it survives. That is what makes the window follow the
// selection rather than the query: typing narrows the list under a highlight
// that stays put, and only jumps to the top when the row it was on is gone.
func (m *Model) refilter() {
	items := m.chooser.Request.Items
	held := ""
	if m.chosen >= 0 && m.chosen < len(m.matches) {
		held = items[m.matches[m.chosen]].Value
	}
	terms := queryTerms(m.query)
	// A fresh slice rather than m.matches[:0]: Model is a value and copies of it
	// are common, so reusing the array would let one copy's search write into
	// another copy's rows.
	matches := make([]int, 0, len(items))
	for i, item := range items {
		if choiceMatches(item, terms) {
			matches = append(matches, i)
		}
	}
	m.matches = matches
	m.chosen = 0
	for at, i := range matches {
		if held != "" && items[i].Value == held {
			m.chosen = at
			break
		}
	}
}

// pickRows is the rows the open list is showing, copied out of the host's slice
// so that a turned axis reaches the frame. The copy is shallow because Choice
// holds no slices the box writes to; Steps is read-only here.
func (m *Model) pickRows() []Choice {
	items := m.chooser.Request.Items
	rows := make([]Choice, len(m.matches))
	for i, at := range m.matches {
		rows[i] = items[at]
	}
	return rows
}

// stepPick moves the highlighted row along its axis. A row without one takes
// the keys as a slip of the finger, like every other key it does not use: the
// list stays up and nothing changes.
func (m Model) stepPick(delta int) (Model, tea.Cmd) {
	if m.chosen < 0 || m.chosen >= len(m.matches) {
		return m, nil
	}
	item := &m.chooser.Request.Items[m.matches[m.chosen]]
	if len(item.Steps) == 0 {
		return m, nil
	}
	item.Step = stepIndex(clampIndex(item.Step, len(item.Steps)), len(item.Steps), delta)
	// No repaint: the box is the same height and the rows are the same width,
	// so the incremental diff draws the one changed cell correctly.
	return m, nil
}

// takePick answers with the highlighted row. The box is about to disappear, so
// every row it covered gets shorter — the same repaint the approval box needs,
// for the same reason.
func (m Model) takePick() (Model, tea.Cmd) {
	if len(m.matches) == 0 {
		// Nothing to take. Closing here would answer "no row" to a reader who
		// asked for a row, so the list stays up and the empty line keeps
		// explaining itself.
		return m, nil
	}
	msg := m.chooser
	answer := PickAnswer{}
	item := msg.Request.Items[m.matches[clampIndex(m.chosen, len(m.matches))]]
	// A row without an axis has no position to report, and clampIndex
	// answers -1 for an empty scale: the zero is chosen here rather than
	// inherited.
	step := 0
	if len(item.Steps) > 0 {
		step = clampIndex(item.Step, len(item.Steps))
	}
	answer = PickAnswer{Value: item.Value, Step: step, Taken: true}
	sendPick(msg.Reply, answer)
	m.chooser = nil
	m.chosen = 0
	m.query = ""
	m.matches = nil
	return m, m.repaint()
}

// dropPick closes the list without an answer.
func (m Model) dropPick() (Model, tea.Cmd) {
	sendPick(m.chooser.Reply, PickAnswer{})
	m.chooser = nil
	m.chosen = 0
	m.query = ""
	m.matches = nil
	return m, m.repaint()
}

// pickStart is the row a list opens on: the one already in effect, so the menu
// also says where the reader is now.
func pickStart(msg *PickMsg) int {
	if msg.Request.Current == "" {
		return 0
	}
	for i, item := range msg.Request.Items {
		if item.Value == msg.Request.Current {
			return i
		}
	}
	return 0
}

// start records a submitted line and launches its turn.
func (m Model) start(prompt Prompt) (Model, tea.Cmd) {
	if m.cfg.Run == nil {
		m.note(m.active, errors.New("this view has no conversation"))
		return m, nil
	}
	m.view(m.active).transcript.Append(Block{Role: RoleUser, Text: prompt.Text})
	m.flash = ""
	return m.startTurn(m.active, false, func(ctx context.Context, sink Sink) error {
		return m.cfg.Run(ctx, prompt, sink)
	})
}

// refreshSubagentSession runs while the host is between turns, when no command
// can be replacing the session.
func (m *Model) refreshSubagentSession() {
	if runtime := m.cfg.Subagents; runtime != nil {
		m.mainSessionID, m.mainSessionFile = runtime.Session()
	}
}

func (m Model) startSubagentResults() (Model, tea.Cmd) {
	runtime := m.cfg.Subagents
	if runtime == nil || m.busy || m.modal() || m.quit {
		return m, nil
	}
	m.subagentWake = false
	if !runtime.Ready() {
		return m, nil
	}
	return m.startTurn(ViewMain, true, runtime.Run)
}

// startTurn serializes commands, prompts and background results through the
// same turn slot. Background results can answer into the main transcript while
// the composer stays in the side view.
func (m Model) startTurn(index int, internal bool, run func(context.Context, Sink) error) (Model, tea.Cmd) {
	events := m.host.events
	m.busy = true
	m.busySeq++
	m.busyTicks = 0
	m.turnView = index
	m.internalTurn = internal

	ctx, cancel := context.WithCancel(m.cfg.Context)
	m.cancel = cancel

	sink := Sink{
		Event: func(event wire.AgentEvent) {
			events <- EventMsg{View: index, Event: event}
		},
		Note: func(text string) {
			msg := NoteMsg{Text: text}
			if internal {
				msg.view = &index
			}
			events <- msg
		},
		Answer: func(text string) {
			events <- AnswerMsg{Text: text}
		},
		View: func(index int) {
			events <- ViewMsg{Index: index}
		},
		Reset: func(view int) {
			events <- ResetMsg{View: view}
		},
		Flash: func(text string) {
			events <- FlashMsg{Text: text}
		},
		Compose: func(text string) {
			events <- ComposeMsg{Text: text}
		},
		Ask: func(req AskRequest) AskAnswer {
			reply := make(chan AskAnswer, 1)
			select {
			case events <- AskMsg{Request: req, Reply: reply}:
			case <-ctx.Done():
				return AskAnswer{}
			}
			select {
			case verdict := <-reply:
				return verdict
			case <-ctx.Done():
				return AskAnswer{}
			}
		},
		Pick: func(req PickRequest) PickAnswer {
			reply := make(chan PickAnswer, 1)
			select {
			case events <- PickMsg{Request: req, Reply: reply}:
			case <-ctx.Done():
				return PickAnswer{}
			}
			select {
			case answer := <-reply:
				return answer
			case <-ctx.Done():
				return PickAnswer{}
			}
		},
	}

	go func() {
		err := run(ctx, sink)
		cancel()
		events <- TurnMsg{View: index, Err: err}
	}()

	return m, tea.Batch(m.listen(), busyTick(m.busySeq))
}

// spinnerFrames is the busy mark's cycle: the braille dots spinner, one cell
// wide, which every font that can draw this screen's boxes can already draw.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// spinnerInterval is how long one frame stays.
//
// Ten frames a second is the slowest rate that still reads as motion rather than
// as a redraw, and every frame is a whole-screen repaint — the transcript is
// rewrapped from scratch because the incremental path cannot be trusted with the
// row shifts — so the rate is a CPU bill rather than a preference. pi's loader
// runs at 80ms; the twenty milliseconds this one adds back buy nothing a reader
// can see, and dropping them costs a quarter more repaints.
const spinnerInterval = 100 * time.Millisecond

// spinnerTicksPerSecond is how many frames make the second the footer prints. It
// is derived from the interval rather than written down, so changing the frame
// rate cannot make the clock drift away from the animation.
const spinnerTicksPerSecond = int(time.Second / spinnerInterval)

// busyTick is one frame of the busy indicator, stamped with the turn it belongs
// to.
//
// A tenth of a second apart, while a turn runs: the footer is the only place a
// reader can see that something is happening between the line they submitted and
// the first token, and a turn can spend ten seconds in a tool call with nothing
// on screen changing. The stamp is what keeps that from leaking between turns —
// without it a tick in flight when a turn ended would count towards the next one
// and start a second chain that every later tick would double.
func busyTick(turn int) tea.Cmd {
	return tea.Tick(spinnerInterval, func(time.Time) tea.Msg { return busyTickMsg{turn: turn} })
}

// listen returns the command that reads the next message, or nil when one is
// already outstanding.
func (m Model) listen() tea.Cmd {
	if m.host.armed {
		return nil
	}
	m.host.armed = true
	return wait(m.host.events)
}

// repaint queues a full-redraw request behind whatever the caller just wrote.
// It travels through the channel like any other message so that the reader the
// model already armed delivers it: a Cmd of its own would be a second reader.
func (m Model) repaint() tea.Cmd {
	if m.host.queued {
		return nil
	}
	select {
	case m.host.events <- clearRequest{}:
		m.host.queued = true
	default:
		// The channel is full, which means the reader is behind by a thousand
		// messages: it will get to a frame of its own soon enough.
	}
	return nil
}

// wait blocks until something is posted. The chain never ends while the program
// runs: the channel is the only way a goroutine can reach the model, and it
// carries notes between turns as well as events during them.
func wait(events <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg { return <-events }
}

// interrupt stops the running turn and gives back everything it was holding.
//
// Cancelling the context is the whole of the mechanism below this layer: the
// provider closes the response body of a request that will never finish, a
// command's process group gets its SIGTERM and then its SIGKILL, and a tool
// gate that was waiting for the reader returns at once. What is left is the
// state this model owns, and one hand-back the host cannot do itself.
//
// A question or a menu on screen belongs to the turn that is gone. Leaving it
// up is the one thing that would outlive the turn: it would keep answering into
// a channel nobody reads, and every key would go to a box that no longer
// decides anything.
//
// It reports whether a modal was closed, which is the one case that needs the
// whole screen redrawn — the box covered rows the transcript was never asked to
// repaint, and closing it makes every one of them shorter.
func (m *Model) interrupt() bool {
	cancel := m.cancel
	if cancel != nil {
		cancel()
		m.cancel = nil
	}
	closed := false
	if m.approval != nil {
		answer(m.approval.Reply, AskAnswer{})
		m.approval = nil
		closed = true
	}
	if m.chooser != nil {
		sendPick(m.chooser.Reply, PickAnswer{})
		m.chooser = nil
		m.chosen = 0
		m.query = ""
		m.matches = nil
		closed = true
	}
	if cancel == nil {
		return closed
	}
	m.view(m.turnView).transcript.Stop()
	if !m.internalTurn {
		m.restore(m.cfg.Interrupt)
	}
	return closed
}

// restore puts the lines a stopped turn never delivered back into the input
// line. They were typed while the turn ran and they belong to the turn that was
// abandoned: delivering them to the next one would put words into a
// conversation the reader had just decided to walk away from, and dropping them
// would lose work that only exists here. The draft already in the box comes
// last, because it is the newest thing the reader said.
func (m *Model) restore(queued func() []string) {
	if queued == nil {
		return
	}
	lines := queued()
	if len(lines) == 0 {
		return
	}
	composer := m.composer()
	text := strings.Join(lines, " ")
	if composer.Empty() {
		composer.Set(text)
		return
	}
	composer.trimSpace()
	composer.edit(0, 0, []rune(text+" "))
	composer.End()
}

// note records a failure the model has no block for: a turn that never started,
// or one the host refused.
func (m *Model) note(index int, err error) {
	if err == nil {
		return
	}
	text := err.Error()
	if errors.Is(err, context.Canceled) {
		text = "interrupted"
	}
	m.view(index).transcript.Append(Block{Role: RoleNotice, Text: text})
}

// foldAll folds every stretch on screen, or unfolds every stretch when they are
// already folded.
//
// It is a gesture on the conversation rather than on the row the reader is
// looking at, so it is refused — quietly — where there is nothing to fold: a
// terminal too narrow for the boxes, and a transcript of prompts and answers
// with no stretches at all. A key that folded a transcript into a shape it was
// already in would be a key that did nothing and said nothing.
func (m Model) foldAll() (Model, tea.Cmd) {
	if !m.cfg.Layout.TranscriptFrames {
		return m, nil
	}
	if !m.activeView().transcript.ToggleAll() {
		return m, nil
	}
	// Folding renames every row under the cursor, so the selection it was made
	// over no longer means what it said.
	m.activeView().sel.clear()
	return m, m.repaint()
}

func (m *Model) switchView() {
	if m.cfg.Toggle == nil {
		return
	}
	next, note := m.cfg.Toggle()
	if next < 0 || next >= len(m.views) {
		return
	}
	m.active = next
	if note != "" {
		m.view(next).transcript.Append(Block{Role: RoleNotice, Text: note})
	}
}

// stepBack leaves the conversation the prompt is in, when the host says there is
// one under it. It reports whether the key was spent on that.
//
// One press, against the two the main conversation needs: the side view is one
// level in, so Ctrl+C backs out of it the way it backs out of a menu, and the
// session underneath keeps its own gesture intact. Nothing is thrown away by
// this — the host keeps the side conversation and this view keeps its draft — so
// a press that was meant to clear the line costs one Ctrl+O to undo.
func (m *Model) stepBack() (bool, tea.Cmd) {
	if m.cfg.Back == nil {
		return false, nil
	}
	next, note, ok := m.cfg.Back()
	if !ok || next < 0 || next >= len(m.views) || next == m.active {
		return false, nil
	}
	m.active = next
	// Both views are armed separately in spirit: leaving one is not half of the
	// gesture that ends the session from the other.
	m.exitArmed = false
	if note != "" {
		m.view(next).transcript.Append(Block{Role: RoleNotice, Text: note})
	}
	// The whole body changes — a different conversation, and possibly a home
	// screen where a transcript was — so the frame is rebuilt rather than
	// diffed.
	return true, m.repaint()
}

func (m *Model) scrollBy(delta int) {
	if delta > 0 {
		m.revealIfAtTop()
	}
	v := m.activeView()
	v.offset += delta
	if v.offset < 0 {
		v.offset = 0
	}
	if max := m.maxOffset(); v.offset > max {
		v.offset = max
	}
}

// revealIfAtTop meets the reader's next chunk of held-back history when they
// are already at the top of the rendered window: scrolling up past the newest
// kept line is the signal that says "show me more". The reveal prepends rows
// above the window and the offset is left alone, so the rows on screen keep
// their places and the older conversation arrives as the reader scrolls up
// into it rather than as a jump. A selection does not survive it: every row it
// named now points at different words.
func (m *Model) revealIfAtTop() {
	if m.activeView().transcript.Hidden() == 0 {
		return
	}
	if m.activeView().offset < m.maxOffset() {
		return
	}
	m.activeView().transcript.Reveal(revealChunkSpans)
	m.activeView().sel.clear()
}

// maxOffset is how far the transcript can be scrolled before its first line
// reaches the top of the window.
func (m *Model) maxOffset() int {
	total := len(m.transcriptLines(m.active, m.width))
	body := m.bodyHeight()
	if total <= body {
		return 0
	}
	return total - body
}

func (m *Model) activeView() *view { return &m.views[m.active] }

func (m *Model) composer() *Composer { return &m.views[m.active].composer }

func (m *Model) view(index int) *view { return &m.views[index] }

// refreshSuggest asks the host what the line could become and keeps the answer
// if it changed. Keeping the picked row when the list is the same is what lets
// the arrows walk it: every key would otherwise reset the highlight to the top.
//
// The file provider is asked first: while a reference is being typed at the end
// of the line, that is what the reader is in the middle of, and a command table
// has nothing to say about it. An empty answer is "not a reference", and the
// command table gets its turn.
func (m *Model) refreshSuggest() {
	var picks []Suggestion
	attach := false
	if !m.busy && m.approval == nil && m.chooser == nil {
		composer := m.composer()
		line := composer.String()
		if m.cfg.Attach != nil && composer.cur == composer.Len() {
			picks = m.cfg.Attach(line)
			attach = len(picks) > 0
		}
		if len(picks) == 0 && m.cfg.Suggest != nil {
			picks = m.cfg.Suggest(line)
		}
	}
	if !samePicks(picks, m.picks) {
		m.picks = picks
		m.picked = 0
	}
	m.attach = attach
}

func samePicks(a, b []Suggestion) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// pick moves the highlight and reports whether the list took the key. With one
// row — or none — there is nothing to move to, and the arrow belongs to the
// history instead.
func (m *Model) pick(delta int) bool {
	if len(m.picks) < 2 {
		return false
	}
	m.picked = wrapIndex(m.picked+delta, len(m.picks))
	return true
}

// complete fills the highlighted suggestion into the line and reports whether it
// did.
//
// A command only ever extends what was typed: the filled text has to start with
// the whole line, so a line that already carries an argument — "/rename alpha" —
// is never rewritten under the reader. That argument is the difference between
// "rename this conversation" and "tell me the name it already has", and 0.4.1
// lost it by comparing only the first field.
//
// A file reference is the other shape: it replaces the runes it stood for — the
// token being typed — and leaves the sentence around it untouched. That is what
// Replace is for, and it is why the interface can fill one in without knowing
// that a path is what it filled.
func (m *Model) complete(explicit bool) bool {
	if len(m.picks) == 0 {
		return false
	}
	pick := m.picks[m.picked]
	composer := m.composer()
	line := composer.String()
	if pick.Replace > 0 {
		end := composer.Len()
		start := end - pick.Replace
		if start < 0 {
			return false
		}
		unchanged := string(composer.buf[start:]) == pick.Name
		if unchanged && !explicit {
			return false
		}
		name := []rune(pick.Name)
		if pick.Path != "" && m.cfg.IsImage != nil && m.cfg.IsImage(pick.Path) {
			composer.edit(start, end, append(name, ' '))
			composer.images = append(composer.images, imagePick{start: start, end: start + len(name), path: pick.Path})
			return true
		}
		if unchanged {
			return false
		}
		composer.edit(start, end, name)
		return true
	}
	filled := pick.Name
	if pick.Usage != "" {
		// The command takes something, so the caret belongs where the argument
		// starts rather than at the end of a name that is already complete.
		filled += " "
	}
	if line == filled || !strings.HasPrefix(filled, line) {
		return false
	}
	m.composer().Set(filled)
	return true
}

func (m *Model) transcriptLines(index, width int) []string {
	return m.views[index].transcript.Lines(width, m.cfg.Styles, m.cfg.Layout)
}

// bodyHeight is the number of rows the transcript gets: everything except the
// command palette, the two gaps, the composer and the status line.
func (m *Model) bodyHeight() int {
	body := m.height - len(m.suggestionLines()) - m.cfg.Layout.ComposerGap -
		len(m.composerBlock()) - m.cfg.Layout.StatusGap - 1
	if body < 1 {
		body = 1
	}
	return body
}

// inputTop is the first row under the transcript. The caret and the frame are
// built from the same two helpers, so the gap cannot exist in one and not in
// the other.
func (m *Model) inputTop() int { return m.bodyHeight() + m.cfg.Layout.ComposerGap }

// composerLines is the prompt's own rows: wrapped at the width the frame leaves
// inside it, and at most ComposerRows of them.
//
// The rows are drawn with the panel's styles rather than the screen's, because
// they land on the raised surface: the two differ by a background, and a
// foreground-only style would leave the screen's color showing through every
// letter.
func (m *Model) composerLines() []string {
	panel := m.cfg.Styles.Panel()
	lines := m.composer().Lines(m.prompt(), m.cfg.Layout.composerWidth(m.width), panel.Accent, panel.Text, m.placeholder())
	if max := m.cfg.Layout.ComposerRows; len(lines) > max {
		lines = lines[len(lines)-max:]
	}
	return lines
}

// composerBlock is the input area as it is drawn: the prompt, and the box
// around it when the frame is on.
func (m *Model) composerBlock() []string {
	if !m.cfg.Layout.frames(m.width) {
		return m.composerLines()
	}
	return frameComposer(m.composerLines(), m.width, m.cfg.Styles.Panel())
}

// frameComposer draws the box the prompt sits in: a rounded rule above and
// below, a border column and one blank cell on each side, and the raised
// background behind every cell in between.
//
// The blank cells are painted rather than left to the final fit, because a
// cell's color comes from what was written into it: an unpainted space inside
// the box would show the screen color through the panel, once per row, and the
// box would read as a stack of stripes instead of a surface.
func frameComposer(rows []string, width int, panel Styles) []string {
	text := width - 2*composerBorder
	rule := strings.Repeat("─", width-2)
	out := make([]string, 0, len(rows)+2)
	out = append(out, panel.Border.Render("╭"+rule+"╮"))
	for _, row := range rows {
		if lipgloss.Width(row) > text {
			// Only a placeholder is unwrapped — the composer wraps what is
			// typed — and a row wider than the box would push the right border
			// off the screen. Truncating it is the same cut fit makes.
			row = strings.Split(lipgloss.Wrap(row, text, ""), "\n")[0]
		}
		line := panel.Border.Render("│") + panel.Background.Render(" ") + row
		if gap := text - lipgloss.Width(row); gap > 0 {
			line += panel.Background.Render(strings.Repeat(" ", gap))
		}
		line += panel.Background.Render(" ") + panel.Border.Render("│")
		out = append(out, line)
	}
	out = append(out, panel.Border.Render("╰"+rule+"╯"))
	return out
}

// suggestionLines is the command palette, or nothing when there is nothing to
// suggest.
func (m *Model) suggestionLines() []string {
	return suggestionLines(m.picks, m.picked, m.width, m.cfg.Styles, m.cfg.Layout)
}

// welcome is the home screen's facts for the view on screen, or nil when the
// host has no page for it. The side conversation has one too, for as long as it
// has said nothing: it is a conversation in its own right from the moment it
// opens, and the page is where the host says what it inherited.
func (m *Model) welcome() *Welcome {
	// A reader who turned the page off gets an empty screen above the prompt,
	// which is the whole of what the setting means: the host still has the
	// facts, and there is nowhere on screen they are asked for.
	if !m.cfg.Layout.HomePage {
		return nil
	}
	if m.cfg.Welcome == nil {
		return nil
	}
	info := m.cfg.Welcome(m.active)
	// The flash describes the session the prompt returns to, and the side view
	// is a page of its own: repeating the main conversation's last event there
	// would report something that did not happen in the conversation on screen.
	if info == nil || m.flash == "" || m.active != ViewMain {
		return info
	}
	copied := *info
	copied.Flash = m.flash
	return &copied
}

func (m *Model) prompt() string {
	if m.busy {
		return "⋯ "
	}
	return "> "
}

// modal reports whether something is on screen that the turn is blocked on and
// that owns every key until it is answered.
func (m *Model) modal() bool { return m.approval != nil || m.chooser != nil }

func (m *Model) placeholder() string {
	switch {
	case m.modal():
		// The box carries every gesture it takes, and the composer takes none
		// while it is up: the usual stop hint beside it would give Esc two
		// meanings on one screen, the way the approval box refuses to.
		return ""
	case m.busy && m.cancel == nil:
		return "stopping…"
	case m.busy:
		return "esc to interrupt"
	case m.exitArmed:
		// The one place the second Ctrl+C is announced. It takes the
		// placeholder's slot because the composer is empty by then — arming
		// clears it — and a hint nobody can see is not a hint.
		return "ctrl+c again to exit"
	}
	return m.cfg.Placeholder
}

// View implements tea.Model. Everything the terminal has to know besides the
// text — alt screen, mouse, window title, caret — is a field on the returned
// value: bubbletea v2 moved the imperative half of a frame out of the model.
func (m Model) View() tea.View {
	if m.width < 1 || m.height < 1 {
		return tea.NewView("")
	}
	lines := m.bodyLines()
	// The up arrow goes first so that a body one row tall, where both arrows
	// claim the same cell, keeps the down arrow: the way to the newest message
	// is the one worth the last word.
	if m.activeView().transcript.Hidden() > 0 || m.activeView().offset > 0 {
		lines[0] = m.arrowLine(lines[0], "↑")
	}
	if m.activeView().offset > 0 {
		lines[len(lines)-1] = m.arrowLine(lines[len(lines)-1], "↓")
	}
	for i := 0; i < m.cfg.Layout.ComposerGap; i++ {
		lines = append(lines, "")
	}
	lines = append(lines, m.suggestionLines()...)
	lines = append(lines, m.composerBlock()...)
	// The gap under the prompt is below the composer on purpose: the status bar
	// belongs to the session, not to the line being typed, and a bar glued to
	// the prompt reads as its footline.
	for i := 0; i < m.cfg.Layout.StatusGap; i++ {
		lines = append(lines, "")
	}
	lines = append(lines, m.statusLine())

	if m.approval != nil {
		lines = m.drawApproval(lines)
	}
	if m.chooser != nil {
		lines = m.drawPicker(lines)
	}
	// Every line is filled to the full width before the frame is handed over.
	// A frame is a screenful, not a list of strings: a row that got shorter
	// than the one before it leaves the difference on screen, and the modal
	// closing is exactly that — a box disappearing over a transcript that was
	// never asked to repaint those rows.
	for i, line := range lines {
		lines[i] = fit(line, m.width, m.cfg.Styles.Background)
	}

	view := tea.NewView(strings.Join(lines, "\n"))
	view.AltScreen = true
	view.MouseMode = tea.MouseModeCellMotion
	view.WindowTitle = m.cfg.Title

	if row, col, ok := m.caret(); ok {
		view.Cursor = tea.NewCursor(col, row)
	}
	return view
}

// bodyLines fills the transcript window to exactly the height it is given.
// Content is anchored to the bottom: a conversation that has just started should
// sit above the composer, not at the top of an empty screen.
func (m *Model) bodyLines() []string {
	lines := m.transcriptLines(m.active, m.width)

	if len(lines) == 0 && !m.busy {
		if info := m.welcome(); info != nil {
			return welcomeLines(*info, welcomeHints(m.active), m.bodyHeight(), m.width, m.cfg.Styles)
		}
	}

	start, end, pad := m.bodyWindow(lines)
	window := make([]string, 0, m.bodyHeight())
	for ; pad > 0; pad-- {
		window = append(window, "")
	}
	for row := start; row < end; row++ {
		window = append(window, m.markRow(lines[row], row))
	}
	return window
}

// arrowLine puts an arrow in the last cell of a row of the body. The row is
// walked as graphemes so that a wide grapheme covering the arrow's cell is
// dropped whole: cutting it would leave half a glyph on screen, and the half
// that stayed would say a word the reader cannot read.
func (m *Model) arrowLine(line, arrow string) string {
	var out strings.Builder
	out.Grow(m.width + 16)
	state := byte(0)
	cell := 0
	for rest := line; rest != ""; {
		// nil for the parser: the parameters of each sequence are of no
		// interest, only how wide the grapheme it carries is.
		seq, width, n, newState := xansi.DecodeSequence(rest, state, nil)
		if n <= 0 {
			break
		}
		state = newState
		rest = rest[n:]
		if width == 0 {
			// An escape or a control byte: it paints no cell, so it is
			// kept as part of whatever follows.
			out.WriteString(seq)
			continue
		}
		if cell+width > m.width-1 {
			// The grapheme covers the arrow's cell: it is dropped whole,
			// and so is everything after it, which would only be drawn
			// past the arrow. The cells it would have taken are filled by
			// the gap below.
			break
		}
		out.WriteString(seq)
		cell += width
	}
	if gap := m.width - 1 - cell; gap > 0 {
		out.WriteString(m.cfg.Styles.Background.Render(strings.Repeat(" ", gap)))
	}
	out.WriteString(m.cfg.Styles.Panel().Accent.Render(arrow))
	return out.String()
}

// markRow paints the cells of one transcript row that the selection covers, and
// is the identity on every row it does not.
func (m *Model) markRow(line string, row int) string {
	from, to, ok := m.activeView().sel.cells(row, xansi.StringWidth(line))
	if !ok {
		return line
	}
	return markSelection(line, from, to)
}

// bodyWindow reports which rows of the transcript the window shows: the first
// and one past the last, and how many blank rows are put above them to keep the
// conversation against the composer.
//
// It is one function rather than three lines inside bodyLines because a click
// arrives as a row on the screen and has to be turned back into a row of the
// transcript, and the arithmetic that does it has to be the same arithmetic the
// frame used. A second copy of it would be a second answer to "which row is
// this", and the frame would be free to disagree with the mouse.
//
// The lines are passed in rather than rendered here: the caller has them, and a
// frame that rendered the transcript twice would pay for the whole of it twice.
func (m *Model) bodyWindow(lines []string) (start, end, pad int) {
	height := m.bodyHeight()
	v := m.activeView()
	if v.offset > len(lines) {
		// The offset belongs to the view, and the clamp has to stick: a copy of
		// it would clamp the same value on every frame that followed.
		v.offset = len(lines)
	}
	end = len(lines) - v.offset
	if end < 0 {
		end = 0
	}
	start = end - height
	if start < 0 {
		start = 0
	}
	return start, end, height - (end - start)
}

// transcriptRow turns a row on the screen into a row of the transcript, and
// reports false for a row that shows padding, the home page, or anything else
// that is not history.
func (m *Model) transcriptRow(y int) (row int, ok bool) {
	lines := m.transcriptLines(m.active, m.width)
	if len(lines) == 0 && !m.busy && m.welcome() != nil {
		// The screen is the home page: there are no rows of conversation on it,
		// so a press there selects nothing rather than the first row of a
		// transcript that has none.
		return 0, false
	}
	start, end, pad := m.bodyWindow(lines)
	if y < pad {
		return 0, false
	}
	row = start + y - pad
	if row < start || row >= end {
		return 0, false
	}
	return row, true
}

// clampedTranscriptRow is transcriptRow for a drag: a pointer above or below the
// window names the nearest row on it rather than nothing. On the home page there
// is still nothing to name, which is the one case that stays empty.
func (m *Model) clampedTranscriptRow(y int) (row int, ok bool) {
	if row, ok := m.transcriptRow(y); ok {
		return row, true
	}
	lines := m.transcriptLines(m.active, m.width)
	start, end, _ := m.bodyWindow(lines)
	if end <= start {
		return 0, false
	}
	if y < m.bodyHeight() {
		return start, true
	}
	return end - 1, true
}

// statusSep separates the footer's fields. The host builds them and this package
// adds exactly one of its own, so the separator is written here too: a row that
// changed its grammar halfway would read as two lines.
const statusSep = " | "

// statusLine is the footer: the host's facts about the session, then the one
// field this package writes.
//
// The tail field comes last and is paid for out of the same width, because the
// host shortens and then drops its fields to fit whatever it is given. Appending
// it afterwards would leave the host filling the row and the marker — the only
// sign that a turn is running — being the thing that gets cut.
func (m *Model) statusLine() string {
	tail := m.tailField(m.width)
	width := m.width
	if tail != "" {
		width -= lipgloss.Width(statusSep) + lipgloss.Width(tail)
	}
	status := ""
	if m.cfg.Status != nil && width > 0 {
		status = m.cfg.Status(width)
	}
	switch {
	case tail == "":
		return fit(status, m.width, m.cfg.Styles.Background)
	case status == "":
		return fit(tail, m.width, m.cfg.Styles.Background)
	default:
		return fit(status+statusSep+tail, m.width, m.cfg.Styles.Background)
	}
}

// tailField is the field this package owns on the footer, and it is one slot for
// two facts that cannot be true at once: the turn's clock while a turn runs, and
// the receipt for the last copy while nothing does. The clock wins, because a
// turn in flight is happening now and the receipt describes something that is
// already done — and the receipt is cleared by the next key, so nothing is lost
// by waiting.
func (m *Model) tailField(width int) string {
	if busy := m.busyField(width); busy != "" {
		return busy
	}
	return m.copied
}

// busyField is the mark and how long the turn on screen has been running.
//
// The frame is the point: it moves, and a still screen under a stalled model
// looks exactly like a screen with nothing happening. The clock is the second
// fact — "is this slow or stuck" is a question a reader asks by the second — and
// until the first second is up the label carries the message alone, because "0s"
// is a number about nothing.
//
// While an approval question waits for its answer the label says so: a turn
// stopped on a gate looks like a turn that is merely slow, and the footer is
// the one place that can say the difference without touching the box.
//
// It is here rather than in the host because the clock is here: the host is
// handed a frame, not a timeline, and a second clock counting the same turn
// would eventually disagree with this one about a number printed to the second.
//
// The three shapes are the footer's width ladder. The mark is paid for out of the
// width the host's own fields get, so a long label on a narrow terminal would
// push the model name off the row: the label goes first, then the clock, and the
// frame itself never does — a footer with no mark on it while a turn runs is the
// one outcome worth spending a cell on at any width.
func (m *Model) busyField(width int) string {
	if !m.busy {
		return ""
	}
	label := "working"
	if m.approval != nil {
		label = "waiting for approval"
	}
	frame := spinnerFrames[m.busyTicks%len(spinnerFrames)]
	clock := ""
	if seconds := m.busyTicks / spinnerTicksPerSecond; seconds > 0 {
		clock = fmt.Sprintf(" %ds", seconds)
	}
	for _, tail := range []string{frame + " " + label + clock, frame + clock} {
		if width-lipgloss.Width(statusSep)-lipgloss.Width(tail) >= minStatusWidth {
			return tail
		}
	}
	return frame
}

// minStatusWidth is what the host's own fields keep before this package spends
// width on its label. The footer describes the session — which model, which
// permission, how much context is left — and the mark is the one field on it
// that is also said by the clock, so it is the field that gives way.
const minStatusWidth = 24

// fit pads or truncates one line to the width of the screen, so a caller that
// returns a shorter string does not leave the previous frame's tail on screen.
//
// The padding is drawn with pad, which is the screen's background: a cell's
// color comes from what was written into it, so a plain space at the end of a
// row would leave the terminal's own background showing as a notch on the
// right of every line. With the terminal's default background pad paints
// nothing and this is the plain space it always was.
func fit(line string, width int, pad lipgloss.Style) string {
	if width < 1 {
		return ""
	}
	if lipgloss.Width(line) > width {
		// Wrapping and keeping the first row is truncation without a second
		// terminal-width implementation: the caller already drops fields on a
		// narrow screen, so this only ever cuts a field that had to go anyway.
		line = strings.Split(lipgloss.Wrap(line, width, ""), "\n")[0]
	}
	if gap := width - lipgloss.Width(line); gap > 0 {
		line += pad.Render(strings.Repeat(" ", gap))
	}
	return line
}

// caret reports where the hardware cursor goes, and whether it should be shown
// at all. It is hidden under the modal: a caret blinking behind a question
// invites typing that would be ignored.
func (m *Model) caret() (row, col int, ok bool) {
	if m.approval != nil || m.chooser != nil {
		return 0, 0, false
	}
	panel := m.cfg.Styles.Panel()
	line, column := m.composer().Cursor(m.prompt(), m.cfg.Layout.composerWidth(m.width))
	if clipped := len(m.composer().Lines(m.prompt(), m.cfg.Layout.composerWidth(m.width), panel.Accent, panel.Text, "")) - m.cfg.Layout.ComposerRows; clipped > 0 {
		line -= clipped
	}
	row = m.inputTop() + len(m.suggestionLines()) + m.cfg.Layout.composerTop(m.width) + line
	if row < 0 || row >= m.height {
		return 0, 0, false
	}
	// The composer measures its column inside its own rows; on screen the text
	// starts after the border and its air, and a caret that stayed in the
	// composer's coordinates would blink on the border.
	return row, m.cfg.Layout.composerLeft(m.width) + column, true
}

// drawApproval puts the question directly above the input area, where the
// reader is looking. Everything behind it stays where it was: the answer a
// reader is deciding on is often the thing on screen.
func (m *Model) drawApproval(lines []string) []string {
	width := m.width - 4
	if width > m.cfg.Layout.BoxWidth {
		width = m.cfg.Layout.BoxWidth
	}
	if width < 20 {
		width = m.width
	}
	return overlay(lines, approvalBox(m.approval.Request, width, m.cfg.Styles), m.inputTop())
}

// drawPicker places the list the way the question is placed: directly above
// the input area, so the keys that answer it are next to the prompt.
func (m *Model) drawPicker(lines []string) []string {
	width := m.width - 4
	if width > m.cfg.Layout.MenuWidth {
		width = m.cfg.Layout.MenuWidth
	}
	if width < 20 {
		width = m.width
	}
	return overlay(lines, pickBox(m.chooser, m.pickRows(), m.chosen, m.query, m.inputTop(), width, m.cfg.Styles, m.cfg.Layout), m.inputTop())
}

// overlay replaces whole rows with a box whose bottom row is the one directly
// above `above`, the first row of the input area. A box in the middle of the
// frame is easy to miss; one glued to the prompt is where the reader's eyes are.
//
// The rows it lands on are dropped rather than composited: drawing the
// transcript underneath would mean re-wrapping it at the box's width, so the
// text would move while the reader is deciding about it.
func overlay(lines, box []string, above int) []string {
	if above > len(lines) {
		above = len(lines)
	}
	if above < 0 {
		above = 0
	}
	// A box taller than the room above the input area keeps its top rows: the
	// border and the title say what the box is, and a screen with no room for
	// those has no room for any placement of it.
	if len(box) > above {
		box = box[:above]
	}
	out := make([]string, len(lines))
	copy(out, lines)
	top := above - len(box)
	for i, line := range box {
		out[top+i] = line
	}
	return out
}

// answer hands the reader's reply back to the goroutine blocked on the gate.
// The channel is buffered and the send is non-blocking: the question has already
// been closed on screen, and a receiver that walked away (a cancelled turn) must
// not be able to block the frame that closed it.
func answer(reply chan AskAnswer, verdict AskAnswer) {
	if reply == nil {
		return
	}
	select {
	case reply <- verdict:
	default:
	}
}

func isRune(key tea.Key, want rune) bool {
	return key.Code == want
}

// hasControl reports whether any of the text a key carries is a control
// character. A key that comes with text is usually one character of a sentence,
// but a paste can arrive as a whole string, and the control characters in it
// are keys the list already answered for.
func hasControl(text string) bool {
	for _, r := range text {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// Compile-time proof that the model satisfies what bubbletea asks for.
var _ tea.Model = Model{}
