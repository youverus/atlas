package main

import (
	"fmt"
	"io"
	"strings"

	"pi-go/internal/config"
	"pi-go/internal/permission"
	"pi-go/internal/tui"
)

// permissionTrigger is the /permission spelling. It is a constant because it is
// read by the command table, by the side-command table, and by the switch that
// dispatches on it, and a typo in one of the three would be a command that /help
// lists and the prompt does not run.
const permissionTrigger = "/permission"

// permissionChoices is the shared vocabulary as menu rows, in the order the
// config validates them so the menu and the error message agree.
//
// The labels come from internal/permission because the window's settings sheet
// shows the same four rows; one table means the two surfaces cannot name the
// same posture differently.
func permissionChoices() []tui.Choice {
	out := make([]tui.Choice, 0, len(config.PermissionModes))
	for _, mode := range config.PermissionModes {
		out = append(out, tui.Choice{Value: mode, Label: permission.Label(mode), Note: permission.Note(mode)})
	}
	return out
}

// permissionModeLabel is the short name for one mode, for the footer and for the
// one-line report. The words themselves live in internal/permission.
func permissionModeLabel(mode string) string {
	return permission.Label(mode)
}

// permissionCommand is /permission: bare asks, "status" reports, a mode name
// switches. It runs in both conversations because the four modes are read from
// one engine that both of them gate on — switching here changes what the next
// call in either view does, which is the whole point of a runtime switch.
func permissionCommand(state *sideState, args []string, choose chooser, out io.Writer) error {
	if state == nil || !state.hasEngine() {
		return fmt.Errorf("%s: this mode has no permission engine", permissionTrigger)
	}
	if len(args) == 0 {
		return permissionPick(state, choose, out)
	}
	// One argument is a mode; anything else is a reader who wrote a sentence and
	// would rather be told than have the first word quietly applied.
	if len(args) > 1 {
		return fmt.Errorf("%s takes one mode: %s", permissionTrigger, strings.Join(config.PermissionModes, ", "))
	}
	mode := args[0]
	if strings.EqualFold(mode, "status") {
		return writePermissionStatus(state, out)
	}
	if err := state.setPermissionMode(mode); err != nil {
		return err
	}
	writePermissionMode(state, out)
	return nil
}

// permissionPick answers a bare /permission: a menu when the interface can draw
// one, the printed list when it cannot. Dismissing the menu changes nothing, for
// the same reason /model's does: the reader asked to look.
func permissionPick(state *sideState, choose chooser, out io.Writer) error {
	current := state.permissionMode()
	if choose != nil {
		if items := permissionChoices(); len(items) > 0 {
			answer := choose(tui.PickRequest{Title: "permission", Items: items, Current: current})
			if !answer.Taken {
				return nil
			}
			if err := state.setPermissionMode(answer.Value); err != nil {
				return err
			}
			writePermissionMode(state, out)
			return nil
		}
	}
	return writePermissionStatus(state, out)
}

// writePermissionMode reports the mode that just took effect, and — in the two
// modes that stop the main conversation from asking — says that the side
// conversation is still going to. That is not a limitation to hide: a side
// conversation exists to ask a question beside the one being worked on, and its
// read-only rule is what keeps the snapshot it inherited honest. The mode governs
// how an ask is answered; the side conversation's rule is that a change has to be
// asked about at all, which is a different sentence.
func writePermissionMode(state *sideState, out io.Writer) {
	mode := state.permissionMode()
	fmt.Fprintf(out, "permission: %s (%s)\n", mode, permissionModeLabel(mode))
	if mode == permission.ModeBypass || mode == permission.ModeOff {
		fmt.Fprintln(out, "note: a side conversation still asks before it changes anything; reads run there without asking")
	}
}

// writePermissionStatus prints the mode in force and what the other three would
// do. The list is the answer to "what else is there" without a second command,
// and the current row is marked because that is the fact being asked for.
func writePermissionStatus(state *sideState, out io.Writer) error {
	current := state.permissionMode()
	fmt.Fprintf(out, "permission: %s (%s)\n", current, permissionModeLabel(current))
	for _, mode := range config.PermissionModes {
		mark := " "
		if mode == current {
			mark = "*"
		}
		fmt.Fprintf(out, "  %s %-11s %s\n", mark, permission.Label(mode), permission.Note(mode))
	}
	return nil
}

// hasEngine reports whether this state gates anything. Every /permission entry
// point asks first: with no engine there is no mode to read, switch or report, and
// printing an empty one would read as a mode named "".
func (s *sideState) hasEngine() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gate.Engine() != nil
}

// permissionMode and setPermissionMode are the engine's mode behind the state's
// mutex: the terminal reads it while a turn may be switching it, and the engine's
// own lock guards the value but not this struct.
func (s *sideState) permissionMode() string {
	s.mu.Lock()
	engine := s.gate.Engine()
	s.mu.Unlock()
	if engine == nil {
		return ""
	}
	return engine.Mode()
}

func (s *sideState) setPermissionMode(mode string) error {
	s.mu.Lock()
	engine := s.gate.Engine()
	s.mu.Unlock()
	if engine == nil {
		return fmt.Errorf("%s: this mode has no permission engine", permissionTrigger)
	}
	return engine.SetMode(mode)
}
