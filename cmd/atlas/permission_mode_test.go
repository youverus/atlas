package main

import (
	"context"
	"strings"
	"testing"

	"pi-go/internal/assembly"
	"pi-go/internal/config"
	"pi-go/internal/permission"
	"pi-go/internal/tui"
)

// permissionState is a side state whose engine is in the given mode, which is
// what /permission reads and writes.
func permissionState(mode string) *sideState {
	return newSideStateWith(assembly.NewGate(permission.New(permission.Options{Mode: mode})))
}

// TestPermissionSwitchesTheMode is the runtime switch the four modes were added
// for: one command, and the next tool call is decided differently.
func TestPermissionSwitchesTheMode(t *testing.T) {
	state := permissionState(permission.ModeDefault)
	application := sideApp(t, state, t.TempDir())

	out, err := runSideCommand(t, application, state, "/permission bypassPermissions")
	if err != nil {
		t.Fatalf("/permission: %v", err)
	}
	if !strings.Contains(out, "Full access") {
		t.Fatalf("output = %q, want the mode that took effect", out)
	}
	if state.permissionMode() != permission.ModeBypass {
		t.Fatalf("mode = %q, want %q", state.permissionMode(), permission.ModeBypass)
	}

	// And the ladder agrees, which is the only part that matters: the engine the
	// gate consults is the one that changed.
	if decision := state.gate.Engine().Decide(toolCall("write")); decision.Behavior != permission.Allow {
		t.Fatalf("after the switch the write is %q, want allow", decision.Behavior)
	}
}

// TestPermissionRefusesAnotherName: a mode this build does not have is an error
// with the four that it does, not a silent fallback to asking.
func TestPermissionRefusesAnotherName(t *testing.T) {
	state := permissionState(permission.ModeDefault)
	application := sideApp(t, state, t.TempDir())

	if _, err := runSideCommand(t, application, state, "/permission acceptEdits"); err == nil {
		t.Fatal("an unknown mode was accepted")
	} else {
		for _, mode := range config.PermissionModes {
			if !strings.Contains(err.Error(), mode) {
				t.Fatalf("error %q does not name %q", err, mode)
			}
		}
	}
	if state.permissionMode() != permission.ModeDefault {
		t.Fatalf("a refused switch changed the mode to %q", state.permissionMode())
	}
}

// TestPermissionStatusNamesTheModeInForce: "status" is the read half, and it is
// also what a bare /permission prints where there is no menu to draw.
func TestPermissionStatusNamesTheModeInForce(t *testing.T) {
	state := permissionState(permission.ModeAuto)
	application := sideApp(t, state, t.TempDir())

	out, err := runSideCommand(t, application, state, "/permission status")
	if err != nil {
		t.Fatalf("/permission status: %v", err)
	}
	if !strings.Contains(out, "permission: auto") {
		t.Fatalf("output = %q, want the mode in force", out)
	}
	// Every row is listed with the current one marked, which is what makes the
	// output answer "what else is there" without a second command.
	if strings.Count(out, "\n") < len(config.PermissionModes) {
		t.Fatalf("output = %q, want a row per mode", out)
	}
	if !strings.Contains(out, "* Auto") {
		t.Fatalf("output = %q, want the row in force marked", out)
	}
}

// TestPermissionSaysWhatTheSideConversationStillDoes: Full access does not reach
// into a side conversation, and the command that turned it on is the place to say
// so rather than letting the reader find out from a prompt.
func TestPermissionSaysWhatTheSideConversationStillDoes(t *testing.T) {
	state := permissionState(permission.ModeDefault)
	application := sideApp(t, state, t.TempDir())

	out, err := runSideCommand(t, application, state, "/permission off")
	if err != nil {
		t.Fatalf("/permission off: %v", err)
	}
	if !strings.Contains(out, "side conversation still asks") {
		t.Fatalf("output = %q, want the side-conversation note", out)
	}
}

// TestPermissionTakesOneMode: two words is a sentence, and applying the first one
// would be the command guessing.
func TestPermissionTakesOneMode(t *testing.T) {
	state := permissionState(permission.ModeDefault)
	application := sideApp(t, state, t.TempDir())

	if _, err := runSideCommand(t, application, state, "/permission auto please"); err == nil {
		t.Fatal("two arguments were accepted")
	}
	if state.permissionMode() != permission.ModeDefault {
		t.Fatalf("mode = %q, want it unchanged", state.permissionMode())
	}
}

// TestPermissionNeedsaMenuOrPrints: a bare command takes a row when the interface
// can draw one, and prints the same rows when it cannot. Dismissing changes
// nothing, because looking is not switching.
func TestPermissionNeedsAMenuOrPrints(t *testing.T) {
	for _, taken := range []bool{true, false} {
		state := permissionState(permission.ModeDefault)
		application := sideApp(t, state, t.TempDir())
		out := &strings.Builder{}
		render := newRenderer(out, out, false)
		// The picker answers by taking the Auto row, or by dismissing.
		pick := func(req tui.PickRequest) tui.PickAnswer {
			if len(req.Items) != len(config.PermissionModes) {
				t.Fatalf("menu offered %d rows, want %d", len(req.Items), len(config.PermissionModes))
			}
			if !taken {
				return tui.PickAnswer{}
			}
			return tui.PickAnswer{Value: permission.ModeAuto, Taken: true}
		}
		done, err := replCommand(context.Background(), application, state, "/permission", models{}, newSessionPicker(application, ""), pick, emptyPrompts(), out, render, mcpState{}, nil)
		if done {
			t.Fatal("/permission ended the session")
		}
		if err != nil {
			t.Fatalf("/permission: %v", err)
		}
		want := permission.ModeDefault
		if taken {
			want = permission.ModeAuto
		}
		if state.permissionMode() != want {
			t.Fatalf("taken=%v: mode = %q, want %q", taken, state.permissionMode(), want)
		}
		if !taken && out.String() != "" {
			t.Fatalf("a dismissed menu printed %q, want nothing", out.String())
		}
	}
}

// TestPermissionWithoutAnEngineIsAnError: the demo and a test build a state with
// no engine, and a command that silently did nothing would be worse than one that
// says there is nothing to switch.
func TestPermissionWithoutAnEngineIsAnError(t *testing.T) {
	if err := permissionCommand(&sideState{}, nil, nil, &strings.Builder{}); err == nil {
		t.Fatal("a state with no engine accepted the command")
	}
	if err := permissionCommand(nil, nil, nil, &strings.Builder{}); err == nil {
		t.Fatal("a missing state accepted the command")
	}
}

// TestTheFooterCarriesTheMode: the footer's second field answers "what will happen
// when a tool is called", and the mode is half of that answer.
func TestTheFooterCarriesTheMode(t *testing.T) {
	state := permissionState(permission.ModeAuto)
	application := sideApp(t, state, t.TempDir())
	bar := newStatusBar(application, state, t.TempDir(), "fs+bash")

	if got := bar.grantsField(); got != "fs+bash · Auto" {
		t.Fatalf("grants = %q, want the tools and the mode", got)
	}
	if got := bar.modeField(); got != "Auto" {
		t.Fatalf("mode = %q, want the mode alone", got)
	}
	if err := state.setPermissionMode(permission.ModeOff); err != nil {
		t.Fatalf("setPermissionMode: %v", err)
	}
	if got := bar.grantsField(); got != "fs+bash · Off" {
		t.Fatalf("grants after the switch = %q, want the new mode", got)
	}
	if got := bar.modeField(); got != "Off" {
		t.Fatalf("mode after the switch = %q, want the new mode alone", got)
	}

	// A session with no tool summary still reports the mode, and a state with no
	// engine reports the tools alone.
	empty := newStatusBar(application, state, t.TempDir(), "")
	if got := empty.grantsField(); got != "Off" {
		t.Fatalf("grants with no tools = %q", got)
	}
	if got := empty.modeField(); got != "Off" {
		t.Fatalf("mode with no tools = %q", got)
	}
	engineLess := newStatusBar(application, &sideState{}, t.TempDir(), "fs")
	if got := engineLess.grantsField(); got != "fs" {
		t.Fatalf("grants with no engine = %q", got)
	}
	if got := engineLess.modeField(); got != "" {
		t.Fatalf("mode with no engine = %q, want an empty field", got)
	}
	if got := newStatusBar(application, nil, t.TempDir(), "fs").modeField(); got != "" {
		t.Fatalf("mode with no side state = %q, want an empty field", got)
	}
}

// TestThePermissionFlagOverridesTheConfig pins the headless switch: -permission
// wins over permission.mode, because a script has no /permission and no prompt.
func TestThePermissionFlagOverridesTheConfig(t *testing.T) {
	cfg := &config.Config{Permission: &config.Permission{Mode: config.PermissionModes[0]}}
	engine, _ := assembly.PermissionFor(cfg, false, config.Selection{}, permission.ModeOff)
	if engine.Mode() != permission.ModeOff {
		t.Fatalf("mode = %q, want the flag", engine.Mode())
	}
	// Without the flag the config decides.
	engine, _ = assembly.PermissionFor(cfg, false, config.Selection{}, "")
	if engine.Mode() != config.PermissionModes[0] {
		t.Fatalf("mode = %q, want the configured one", engine.Mode())
	}
}

// TestTheFlagRefusesAnUnknownMode: the same list the command prints, on the path
// a script takes, where nobody reads a prompt.
func TestTheFlagRefusesAnUnknownMode(t *testing.T) {
	if _, err := parseFlags([]string{"-permission", "acceptEdits"}); err == nil {
		t.Fatal("an unknown -permission value was accepted")
	}
	if _, err := parseFlags([]string{"-permission", permission.ModeAuto}); err != nil {
		t.Fatalf("-permission auto: %v", err)
	}
}

// TestPermissionsReachesTheSameCommand: the plural is the spelling the reference
// implementation's footer names, and a reader who learned it there brings it
// here. Both halves of the command have to answer to it — the report and the
// switch — because "unknown command" would say atlas has no way to change the
// mode.
func TestPermissionsReachesTheSameCommand(t *testing.T) {
	state := permissionState(permission.ModeDefault)
	application := sideApp(t, state, t.TempDir())

	out, err := runSideCommand(t, application, state, permissionAlias+" status")
	if err != nil {
		t.Fatalf("%s status: %v", permissionAlias, err)
	}
	if !strings.Contains(out, "permission: default") {
		t.Fatalf("output = %q, want the mode reported", out)
	}
	out, err = runSideCommand(t, application, state, permissionAlias+" auto")
	if err != nil {
		t.Fatalf("%s auto: %v", permissionAlias, err)
	}
	if got := state.permissionMode(); got != permission.ModeAuto {
		t.Fatalf("mode = %q, want the switch to have happened", got)
	}
	if !strings.Contains(out, "permission: auto") {
		t.Fatalf("output = %q, want the new mode reported", out)
	}
	// The argument rules are the command's, not the alias's: two words are still
	// refused, and the message names the spelling that was typed.
	_, err = runSideCommand(t, application, state, permissionAlias+" auto please")
	if err == nil {
		t.Fatalf("%s accepted a sentence where it takes one mode", permissionAlias)
	}
	if got := state.permissionMode(); got != permission.ModeAuto {
		t.Fatalf("a refused switch left the mode at %q", got)
	}
}

// TestTheModeIsReachableFromEveryState: the mode is process state, not record
// content, so the two states that refuse anything writing still answer /permission
// — and their help lines say so, because the table above them lists every command
// atlas has.
func TestTheModeIsReachableFromEveryState(t *testing.T) {
	// A conversation with a side one open, a plain main conversation and an
	// archived session are the three states a prompt can be in.
	if !sideCommands[permissionTrigger] {
		t.Fatalf("%s does not run in a side conversation", permissionTrigger)
	}
	if !archivedCommands[permissionTrigger] {
		t.Fatalf("%s does not run in an archived session", permissionTrigger)
	}
	for _, line := range []string{sideHelp, archivedHelp} {
		if !strings.Contains(line, permissionTrigger) {
			t.Fatalf("help line = %q, want it to name %s", line, permissionTrigger)
		}
		// The alias is a spelling, not a second command: listing it beside the
		// command would read as two commands that do the same thing.
		if strings.Contains(line, permissionAlias) {
			t.Fatalf("help line = %q, want the command listed once", line)
		}
	}
}

// TestTheModeCanBeSwitchedFromASideConversation is the reachability test that
// runs the command rather than reading the table: the switch is real, and it is
// the main conversation's next call that changes.
func TestTheModeCanBeSwitchedFromASideConversation(t *testing.T) {
	state := permissionState(permission.ModeDefault)
	application := sideApp(t, state, t.TempDir())
	if _, err := runSideCommand(t, application, state, "/side"); err != nil {
		t.Fatalf("/side: %v", err)
	}
	if !state.typingInSide() {
		t.Fatal("the prompt is not in the side conversation")
	}
	out, err := runSideCommand(t, application, state, permissionAlias+" off")
	if err != nil {
		t.Fatalf("%s off in a side conversation: %v", permissionAlias, err)
	}
	if got := state.permissionMode(); got != permission.ModeOff {
		t.Fatalf("mode = %q, want the switch", got)
	}
	// The report has to say that the side conversation still asks, because the
	// mode governs how an ask is answered, not whether one happens.
	if !strings.Contains(out, "a side conversation still asks") {
		t.Fatalf("output = %q, want the note about the side conversation", out)
	}
}

// TestTheModeCanBeSwitchedFromAnArchivedSession: no turn can run there, which is
// exactly why the switch is harmless — it says what the conversations that
// follow will do, and the status half is how a reader asks without restoring.
func TestTheModeCanBeSwitchedFromAnArchivedSession(t *testing.T) {
	application, _, id, _ := archiveSetup(t)
	if _, err := runCommand(t, application, recoverTrigger+" "+id); err != nil {
		t.Fatalf("%s: %v", recoverTrigger, err)
	}
	if !application.Archived() {
		t.Fatal("the recovered session is not archived")
	}
	state := permissionState(permission.ModeDefault)
	out, err := runSideCommand(t, application, state, permissionAlias+" status")
	if err != nil {
		t.Fatalf("%s status in an archived session: %v", permissionAlias, err)
	}
	if !strings.Contains(out, "permission: default") {
		t.Fatalf("output = %q, want the mode reported", out)
	}
	if _, err := runSideCommand(t, application, state, permissionAlias+" bypassPermissions"); err != nil {
		t.Fatalf("%s bypassPermissions in an archived session: %v", permissionAlias, err)
	}
	if got := state.permissionMode(); got != permission.ModeBypass {
		t.Fatalf("mode = %q, want the switch", got)
	}
}
