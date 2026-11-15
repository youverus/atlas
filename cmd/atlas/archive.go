package main

import (
	"fmt"
	"io"

	"pi-go/internal/app"
)

// archivedCommands are the commands that still run while an archived session is
// on screen. /restore and /delete are the two ways out of the state; /help and
// /exit never touch the file. Everything else either appends to the record the
// user put away or swaps it out from under them — and a state you can neither
// ask about nor leave is a trap.
var archivedCommands = map[string]bool{
	"/help":    true,
	"/restore": true,
	"/delete":  true,
	"/mcp":     true,
	// /skill's list half is a read, and the record stays untouched by it. Its
	// load half is refused by the command itself, which says why: the turn
	// would append to the file, and that is what this state takes away.
	"/skill": true,
	// /reload reads the files behind the prompt and rebuilds it in memory.
	// The record stays untouched: what it holds is a conversation, and the
	// instructions it runs on are the process's, not the file's.
	"/reload": true,
	// /plugin describes the machine, not this session: it reads the plugin
	// directories and the skills they hold, and neither is part of the record.
	"/plugin": true,
	// /permission reads and writes the one engine this process gates on, so
	// neither half touches the file. No turn can run here, which makes a switch
	// a statement about the conversations that follow rather than about this
	// one — and the status half is how a reader asks what the mode is without
	// leaving the archive to find out.
	permissionTrigger: true,
	"/exit":           true,
}

// archivedHelp is the line under the command table while an archived session is
// the one on screen. Same reason as sideHelp: the table above it lists every
// command atlas has, and leaving the reader to find out by typing which of them
// still run here would make the one place they look for answers wrong.
const archivedHelp = "in an archived session only /help, /restore, /delete, /permission, /mcp, /skill, /plugin, /reload and /exit run; /restore puts it back in /resume, /delete removes the file"

// archivedLocked is what opening an archived session says before the first
// refusal — the switch into it, or a startup in one. The error would say the
// same thing one keystroke later and cost a prompt.
const archivedLocked = "archived: read-only here; /restore puts it back in /resume, /delete removes the file"

// archiveSession puts the conversation away and reports where the prompt landed.
// Two things have to be said: which session was archived (the user may have
// several) and that the prompt is now in a new one, because the next message
// goes to that one rather than to the conversation on screen a moment ago.
func archiveSession(application *app.App, out io.Writer) error {
	manager := application.Session()
	id, name := manager.ID(), application.Name()
	if err := application.Archive(); err != nil {
		return err
	}
	fmt.Fprintf(out, "archived: %s\n", archivedLabel(name, id))
	fmt.Fprintf(out, "new session: %s\n", application.Session().ID())
	fmt.Fprintln(out, "hidden from /resume; /recover opens it again")
	return nil
}

// restoreSession lifts the mark. It is the only command that turns the state
// off without destroying anything, so it says what came back.
func restoreSession(application *app.App, out io.Writer) error {
	name := application.Name()
	id := application.Session().ID()
	if err := application.Restore(); err != nil {
		return err
	}
	fmt.Fprintf(out, "restored: %s is back in /resume and takes prompts again\n", archivedLabel(name, id))
	return nil
}

// deleteSession removes an archived conversation and starts a fresh one. The
// path is part of the report: this is the one command in atlas that destroys a
// transcript, and "which file" is the receipt.
func deleteSession(application *app.App, out io.Writer) error {
	manager := application.Session()
	id, name := manager.ID(), application.Name()
	file := manager.File()
	if err := application.Delete(); err != nil {
		return err
	}
	fmt.Fprintf(out, "deleted: %s\n", archivedLabel(name, id))
	fmt.Fprintf(out, "file: %s (removed)\n", file)
	fmt.Fprintf(out, "new session: %s\n", application.Session().ID())
	return nil
}

// archivedLabel names the session a report is about, the way a menu row or the
// footer would: its name when it has one, otherwise the id prefix.
func archivedLabel(name, id string) string {
	if name != "" {
		return name
	}
	if len(id) > 8 {
		id = id[:8]
	}
	return "session " + id
}
