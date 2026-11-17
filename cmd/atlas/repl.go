package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"

	"pi-go/internal/app"
	"pi-go/internal/assembly"
	"pi-go/internal/tui"
	"pi-go/internal/wire"
)

// models is the /model surface: the switch a typed command runs and the list a
// bare command prints. They travel together because they are two faces of one
// table, and a command that switched a model the list did not show would be a
// session lying about what it can run.
type models struct {
	// switchModel applies a spec and reports what took effect: the descriptor,
	// the level, and the note explaining a level the model did not offer.
	switchModel func(spec string) (model wire.Model, level, adjustment string, err error)
	list        func(io.Writer) error
	// choices is the same table as rows for the interface to draw a menu from.
	// Nil when the caller has no config to enumerate — a test that only needs
	// the switch, for instance — and then the bare command prints the table.
	choices func() []tui.Choice
}

// chooser asks the reader to take one row from a list. It is nil on the paths
// with no interface to draw a menu in, and every command that uses it falls back
// to printing the same rows.
type chooser func(req tui.PickRequest) tui.PickAnswer

// turnRunner runs one turn and reports what the events cannot say. It is nil on
// the paths with nowhere to stream to.
//
// A command needs it because /continue is not a report: the turn it resumes was
// composed by an earlier prompt, so the only thing left to choose is where the
// answer is drawn. Both front ends already own that choice — the inline one
// streams to the renderer under a signal handler, the screen streams into the
// answer block — and this is that choice, handed in. /goal uses it too: the
// loop runs one turn per call and reads the result back for its accounting.
type turnRunner func(ctx context.Context, run func(context.Context, func(wire.AgentEvent) error) (app.Result, error)) (app.Result, error)

// replCommand handles one slash command and reports whether the session should
// end. An unknown command is an error, not a prompt: sending "/modle" to the
// model would waste a turn and confuse the transcript.
func replCommand(ctx context.Context, application *app.App, side *sideState, line string, model models, resume *sessionPicker, pick chooser, prompts *assembly.PromptSource, out io.Writer, render *renderer, servers mcpState, draft func(string), turn turnRunner) (bool, error) {
	fields := strings.Fields(line)
	// /skill:foo is the spelling people bring from other agents, and "unknown
	// command" would read as "atlas has no skills". The hint comes before the
	// two state checks because a command that does not exist is not refused by
	// a state; it is answered the same way wherever it is typed.
	if name, ok := strings.CutPrefix(fields[0], "/skill:"); ok && name != "" {
		return false, fmt.Errorf("/skill takes a space, not a colon: did you mean \"/skill %s\"?", name)
	}
	// A second spelling is answered by the command it names, and it is resolved
	// here so that both gates below and the switch itself see one name per
	// command.
	if canonical, ok := commandAliases[fields[0]]; ok {
		fields[0] = canonical
	}
	// An archived conversation is read-only from the moment it opens: the file
	// is a record, and everything below either writes to it or swaps it out.
	// The two commands that end the state and the two that never touch it stay.
	if application != nil && application.Archived() && !archivedCommands[fields[0]] {
		return false, app.ErrArchived
	}
	// A side conversation keeps the commands that describe itself and drops the
	// rest: /new or /resume would move the session the snapshot came from out
	// from under it, and /model would change a model the side agent copied when
	// it was opened.
	if side != nil && side.typingInSide() && !sideCommands[fields[0]] {
		return false, fmt.Errorf("%s does not run in a side conversation; /parent returns to the main one", fields[0])
	}
	switch fields[0] {
	case "/exit":
		return true, nil
	case "/help":
		fmt.Fprint(out, helpText())
		if side != nil && side.typingInSide() {
			fmt.Fprintf(out, "\n%s\n", sideHelp)
		}
		if application != nil && application.Archived() {
			fmt.Fprintf(out, "\n%s\n", archivedHelp)
		}
		return false, nil
	case "/side", "/btw":
		if side == nil {
			return false, fmt.Errorf("%s: no side conversation in this mode", fields[0])
		}
		// Exactly two fields: "/side off" discards, but "off by one?" is a
		// question, and the command should not eat the first word of a prompt.
		if len(fields) == 2 && fields[1] == "off" {
			return false, discardSide(application, side, out, render)
		}
		return false, openSide(ctx, application, side, strings.Join(fields[1:], " "), out, render)
	case "/parent":
		if side == nil {
			return false, errors.New("/parent: no side conversation in this mode")
		}
		return false, leaveSide(side, out, render)
	case "/model":
		if len(fields) < 2 {
			// No argument is not an error: it is the question "which models
			// are there", and the answer is a menu when there is one to draw.
			return false, model.pick(application, pick, out)
		}
		return false, model.apply(application, strings.Join(fields[1:], " "), out)
	case permissionTrigger:
		return false, permissionCommand(side, fields[1:], pick, out)
	case "/mcp":
		// An argument names one server: "which tools did this connect" is the
		// question, and /mcp <server> is the follow-up. login is the one verb
		// that writes, so the two states that must not write refuse it here —
		// they are facts about this prompt, which the report cannot see.
		if isMCPLogin(fields[1:]) {
			if side != nil && side.typingInSide() {
				return false, fmt.Errorf("/mcp login does not run in a side conversation; a login changes what is stored for every session, and /parent returns to the main one")
			}
			if application != nil && application.Archived() {
				return false, app.ErrArchived
			}
		}
		return false, servers.report(ctx, fields[1:], out)
	case "/skill":
		return false, skillCommand(ctx, application, side, fields[1:], out, render)
	case "/plugin":
		if len(fields) > 1 {
			return false, fmt.Errorf("/plugin takes no argument; it lists the installed plugins")
		}
		return false, pluginCommand(application, out)
	case "/compact":
		result, err := application.Compact(ctx, strings.Join(fields[1:], " "), render.event)
		if err != nil {
			return false, err
		}
		fmt.Fprintf(out, "compacted %d tokens into %d\n", result.TokensBefore, estimateAfter(result))
		return false, nil
	case "/continue":
		// No argument: what to continue is the shape of the last turn, and a
		// session has exactly one of those. The refusal before the turn is the
		// point of the command's report shape — a reader who typed it after a
		// turn that finished should be told so, not answered with silence.
		if len(fields) > 1 {
			return false, errors.New("/continue takes no argument; it resumes the turn that was interrupted")
		}
		if turn == nil {
			return false, errors.New("/continue: no turn runner in this mode")
		}
		continuation := application.Continuation()
		if !continuation.Resumable() {
			return false, fmt.Errorf("/continue: %s", continuation.Reason)
		}
		fmt.Fprintln(out, continuationNote(continuation))
		_, err := turn(ctx, func(turnCtx context.Context, emit func(wire.AgentEvent) error) (app.Result, error) {
			return application.Continue(turnCtx, emit)
		})
		return false, err
	case "/goal":
		return false, goalCommand(ctx, application, fields[1:], pick, out, turn)
	case "/update":
		if len(fields) > 1 {
			return false, errors.New("/update takes no argument; it downloads the newest release")
		}
		exePath, err := updateExecutable()
		if err != nil {
			return false, fmt.Errorf("/update: %w", err)
		}
		return false, updateCommand(ctx, pick, out, updateRepo, exePath, updateClient())
	case "/new":
		if err := application.NewSession(); err != nil {
			return false, err
		}
		fmt.Fprintf(out, "new session: %s\n", application.Session().ID())
		return false, nil
	case "/resume":
		return false, resume.open(resumeArg(line), pick, out)
	case recoverTrigger:
		recover := newRecoverPicker(resume)
		if recover == nil {
			return false, errors.New("/recover: no session store in this mode")
		}
		return false, recover.open(commandArg(line, recoverTrigger), pick, out)
	case "/archive":
		return false, archiveSession(application, out)
	case "/restore":
		return false, restoreSession(application, out)
	case "/delete":
		return false, deleteSession(application, out)
	case "/fork":
		return false, forkCommand(application, strings.TrimSpace(strings.TrimPrefix(line, "/fork")), pick, draft, out)
	case "/rename":
		return false, renameSession(application, strings.Join(fields[1:], " "), out)
	case "/export":
		return false, writeExport(out, application, strings.Join(fields[1:], " "))
	case "/init":
		return false, initProject(ctx, application, out, render, prompts)
	case "/reload":
		return false, reloadPrompt(application, prompts, out)
	}
	return false, fmt.Errorf("unknown command %q; /help lists the commands", fields[0])
}

// permissionAlias is the plural spelling, which is the one the reference
// implementation's footer names. A reader who learned the command there types it
// here, and "unknown command" would say atlas has no way to change the mode —
// the one answer that is wrong.
const permissionAlias = "/permissions"

// commandAliases maps a second spelling onto the command that answers it. It is
// read before the two state gates rather than inside the switch, because an
// alias has to be allowed exactly where its own command is: the archived table,
// the side table, the palette and /help then register one name each, and an
// alias can never become a command that runs where the command it names is
// refused.
var commandAliases = map[string]string{
	permissionAlias: permissionTrigger,
}

// command is one slash command. The table is the one place that says what
// exists: /help renders it, and the parser is checked against it by a test.
type command struct {
	name    string // "/model"
	usage   string // "<name>", empty when the command takes no argument
	summary string
}

var commands = []command{
	{"/help", "", "list these commands"},
	{"/side", "[question|off]", "open a temporary conversation beside this one"},
	{"/btw", "[question|off]", "the same as /side"},
	{"/parent", "", "return to the main conversation, keeping the side one"},
	{"/model", "[<name>[:level]]", "switch model, or pick one from the list"},
	{permissionTrigger, "[status|<mode>]", "show or switch how tool calls are approved"},
	{"/mcp", "[<server>|login <server>]", "list the connected MCP servers and their tools, or authorize one"},
	{"/skill", "[<name> [text]]", "list the installed skills, or load one and start a turn"},
	{"/plugin", "", "list the installed plugins and what each one adds"},
	{"/compact", "[note]", "summarize and drop older history"},
	{"/continue", "", "resume the turn that was interrupted"},
	{"/goal", "[<objective>|pause|resume|clear]", "set, show, pause, resume or clear the session's goal"},
	{"/update", "", "download and install the newest atlas release"},
	{"/new", "", "start a new session in this directory"},
	{"/resume", "[<path|id>]", "continue a recorded session"},
	{recoverTrigger, "[<id>]", "open a session you archived"},
	{"/archive", "", "hide this session from /resume and start a new one"},
	{"/restore", "", "put an archived session back in /resume"},
	{"/delete", "", "delete the archived session you are in"},
	{"/fork", "[<number>]", "branch off one of your messages, or the whole conversation"},
	{"/rename", "<name>", "set the session name"},
	{"/export", "[<path>]", "write this session to a .md, .html or .jsonl file"},
	{"/init", "", "write an AGENTS.md for this repository and read it now"},
	{"/reload", "", "read the project files and skills again"},
	{"/exit", "", "leave"},
}

// sideHelp is the line under the command table while the prompt belongs to a
// side conversation. The table above it lists every command atlas has, and
// leaving the reader to find out by typing which of them still run here would
// make the one place they look for answers wrong.
const sideHelp = "in a side conversation only /help, /side, /btw, /parent, /permission, /mcp, /skill, /plugin and /exit run; Ctrl+O switches to main, /side off discards it"

// suggestionsFor turns what has been typed into the commands it could become.
//
// Only the first field is matched. Everything after the space is an argument,
// and the description stays up while it is being typed because that is exactly
// when it is worth reading: "/model deep" is still a question about /model.
//
// The side conversation gets the shorter list, for the same reason /help names
// it: suggesting a command that will be refused would make the palette the one
// place in the interface that is wrong about what can run.
func suggestionsFor(line string, side *sideState, archived bool) []tui.Suggestion {
	field := strings.TrimLeft(line, " ")
	if i := strings.IndexByte(field, ' '); i >= 0 {
		field = field[:i]
	}
	if !strings.HasPrefix(field, "/") {
		return nil
	}
	inSide := side != nil && side.typingInSide()
	out := make([]tui.Suggestion, 0, len(commands))
	for _, c := range commands {
		if archived && !archivedCommands[c.name] {
			continue
		}
		if inSide && !sideCommands[c.name] {
			continue
		}
		if !strings.HasPrefix(c.name, field) {
			continue
		}
		out = append(out, tui.Suggestion{Name: c.name, Usage: c.usage, About: c.summary})
	}
	return out
}

// helpText renders the command table plus the catch-all line. The command
// column is measured from the table rather than fixed: a usage string that
// outgrows the column would push its own summary out of line, and the one place
// a user goes to see what exists is a bad place to look ragged.
func helpText() string {
	width := 0
	for _, c := range commands {
		if n := lipgloss.Width(commandLabel(c)); n > width {
			width = n
		}
	}
	var b strings.Builder
	for _, c := range commands {
		fmt.Fprintf(&b, "%-*s  %s\n", width, commandLabel(c), c.summary)
	}
	fmt.Fprintf(&b, "%-*s  %s\n", width, "anything else", "sent to the model")
	// The gestures are not commands, so they are not rows: keeping them off
	// the table leaves "anything else is a prompt" the last word about
	// commands, and this block still puts the keys where a user looks for them.
	const keys = "keys: "
	fmt.Fprintf(&b, "\n%s%s\n", keys, "Ctrl+O switches between the main and side conversations")
	// Twice for the turn and twice for the session, and the line says which
	// twice is which: the same key means two different things depending on
	// whether one is running, and this block is where a reader looks to find
	// that out rather than guess.
	fmt.Fprintf(&b, "%s%s\n", strings.Repeat(" ", len(keys)), "Esc or Ctrl+C twice stops the turn")
	fmt.Fprintf(&b, "%s%s\n", strings.Repeat(" ", len(keys)), "Ctrl+C on an idle prompt clears it; twice exits")
	// The two gestures that are not keys at all, or not only keys: the mouse
	// cannot be advertised in a key table, and a reader who does not know a
	// drag copies will keep reaching for Shift when the answer scrolls away.
	fmt.Fprintf(&b, "%s%s\n", strings.Repeat(" ", len(keys)), "drag over the transcript copies what it covers")
	fmt.Fprintf(&b, "%s%s\n", strings.Repeat(" ", len(keys)), "Ctrl+G folds every stretch of reasoning and calls; again to unfold")
	return b.String()
}

// commandLabel is the command and its argument hint, the left column of /help.
func commandLabel(c command) string {
	if c.usage == "" {
		return c.name
	}
	return c.name + " " + c.usage
}

// continuationNote says what /continue picked up.
//
// It is not decoration. A continued answer reopens the block that was cut off,
// so the reader can see it happen; a continued tool batch or a re-sent prompt
// sends no visible prompt at all, and the model would answer a question the
// transcript does not show being asked. One line naming the break is the whole
// difference between a session that resumes and one that appears to talk to
// itself.
func continuationNote(continuation app.Continuation) string {
	switch continuation.Kind {
	case app.ContinuationAnswer:
		return fmt.Sprintf("continuing after %d characters", continuation.Chars)
	case app.ContinuationTools:
		call := "tool call"
		if continuation.Calls != 1 {
			call = "tool calls"
		}
		return fmt.Sprintf("continuing: %d %s were cut off", continuation.Calls, call)
	case app.ContinuationPrompt:
		return "continuing: the last prompt never got an answer"
	}
	return "continuing"
}

// currentModel is the machine spelling "provider/id" of the model in effect.
// The /model menu needs exactly this: its Current is matched against row
// Values, so a display name here would open the list on the wrong row. Every
// surface that prints the model for a person uses modelField instead.
func currentModel(application *app.App) string {
	model := application.Model()
	return model.Provider + "/" + model.ID
}

// forkSession continues the conversation in a new session and reports where it
// went. Printing the file matters: the point of a fork is that both sessions
// survive, and the user has to be able to find the one that stayed behind.
func forkSession(application *app.App, out io.Writer) error {
	if _, err := application.ForkSession(); err != nil {
		return err
	}
	reportFork(application, out)
	return nil
}

// reportFork says which session is open now. Both halves of /fork end here,
// because both of them swap the session and the reader needs the same two facts
// either way.
func reportFork(application *app.App, out io.Writer) {
	manager := application.Session()
	if manager.Persisted() {
		fmt.Fprintf(out, "fork: %s\nfile: %s\n", manager.ID(), manager.File())
		return
	}
	fmt.Fprintf(out, "fork: %s (in memory)\n", manager.ID())
}

// forkCommand is /fork: branch the conversation at one of its prompts.
//
// With no argument it asks where, because that is the question the command is
// for; a number answers it without the menu, which is what the printed table
// tells a reader to type. The bare command used to mean "fork the whole branch",
// and that is now the first row rather than a separate spelling: a fork point is
// somewhere in the conversation, and the end of it is one of the points.
func forkCommand(application *app.App, arg string, pick chooser, draft func(string), out io.Writer) error {
	places := forkPlaces(application.ForkMessages())
	if len(places) == 1 {
		// Nothing to choose between: the session holds no message to branch
		// before, so the only point is the end of it. A menu of one row would
		// ask a question with one answer.
		return forkSession(application, out)
	}
	if arg == "" {
		if pick == nil {
			forkTable(places, out)
			return nil
		}
		answer := pick(tui.PickRequest{Title: "fork before", Items: places})
		if !answer.Taken {
			return nil
		}
		return forkAt(application, answer.Value, draft, out)
	}
	index, err := strconv.Atoi(arg)
	if err != nil || index < 0 || index >= len(places) {
		return fmt.Errorf("/fork: no point %q; /fork lists them", arg)
	}
	return forkAt(application, places[index].Value, draft, out)
}

// forkAt swaps the session for one that ends before entryID. An empty entryID is
// the end of the conversation, which is what the first row names.
//
// The message that was branched off comes back as a draft: the reason to fork
// in front of something is to say it differently, and retyping the sentence to
// change one word in it is the work the command exists to save.
func forkAt(application *app.App, entryID string, draft func(string), out io.Writer) error {
	if entryID == "" {
		return forkSession(application, out)
	}
	text, err := application.ForkAt(entryID)
	if err != nil {
		return err
	}
	reportFork(application, out)
	if draft != nil && text != "" {
		draft(text)
	}
	return nil
}

// forkPlaces is what /fork offers: the whole conversation, then every message
// the reader wrote. Only the prompts are points, which is not a limitation but
// the shape of a branch — what a fork redecides is what was asked, and the
// answers follow from that.
//
// The first row carries an empty value, so "the end of the conversation" needs
// no entry id of its own.
func forkPlaces(messages []app.ForkMessage) []tui.Choice {
	places := make([]tui.Choice, 0, len(messages)+1)
	places = append(places, tui.Choice{
		Label: "the whole conversation",
		Note:  "fork after the last message",
	})
	for _, message := range messages {
		places = append(places, tui.Choice{Value: message.EntryID, Label: preview(message.Text, 60)})
	}
	return places
}

// forkTable lists the points for a reader with no menu to take one from. The
// numbers lead the row because they are what the command takes, and the column
// is measured rather than fixed so a list of ten does not look ragged.
func forkTable(places []tui.Choice, out io.Writer) {
	width := 0
	for index := range places {
		if n := len(strconv.Itoa(index)); n > width {
			width = n
		}
	}
	for index, place := range places {
		fmt.Fprintf(out, "%*d  %s\n", width, index, place.Label)
	}
	fmt.Fprintf(out, "\nfork at one of them with /fork <number>\n")
}

// renameSession sets the name the footer and the session file report. With no
// argument it reports the current name, which is how a user checks the name
// before replacing it.
func renameSession(application *app.App, name string, out io.Writer) error {
	if name == "" {
		if current := application.Name(); current != "" {
			fmt.Fprintf(out, "name: %s\n", current)
		} else {
			fmt.Fprintln(out, "name: (unnamed)")
		}
		return nil
	}
	if err := application.SetName(name); err != nil {
		return err
	}
	fmt.Fprintf(out, "name: %s\n", application.Name())
	return nil
}

// thinkingNote reports the level a switch applied. A model that cannot think
// drops the level, so naming it would describe a setting the request never
// carries; off is the default and stays quiet, the way the banner does it.
func thinkingNote(model wire.Model, level string) string {
	if !sendsThinking(model.Reasoning, level) {
		return ""
	}
	return " (thinking: " + level + ")"
}

// estimateAfter reports the post-compaction estimate, falling back to the
// pre-compaction count when the kernel did not report one.
func estimateAfter(result wire.CompactionResult) int {
	if result.EstimatedTokensAfter != nil {
		return *result.EstimatedTokensAfter
	}
	return result.TokensBefore
}
