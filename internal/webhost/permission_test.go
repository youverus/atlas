package webhost

import (
	"testing"

	"pi-go/internal/assembly"
	"pi-go/internal/config"
	"pi-go/internal/permission"
)

// permissionHost builds the smallest kernel this method reads: the posture lives
// on the gate, so nothing else has to exist for the adapter to answer.
func permissionHost(t *testing.T, mode string) *Host {
	t.Helper()
	engine := permission.New(permission.Options{Mode: mode})
	return New(&assembly.Kernel{Gate: assembly.NewGate(engine)})
}

// TestPermissionReportsTheEngineAndTheMenu is the join the window depends on: the
// mode in force comes from the live engine, and the four rows come from the same
// table the terminal's /permission menu reads.
func TestPermissionReportsTheEngineAndTheMenu(t *testing.T) {
	host := permissionHost(t, permission.ModeBypass)

	state, err := host.Permission()
	if err != nil {
		t.Fatalf("permission: %v", err)
	}
	if state.Mode != permission.ModeBypass {
		t.Fatalf("mode = %q, want %q", state.Mode, permission.ModeBypass)
	}
	if len(state.Modes) != len(config.PermissionModes) {
		t.Fatalf("modes = %d rows, want %d", len(state.Modes), len(config.PermissionModes))
	}
	for i, value := range config.PermissionModes {
		if state.Modes[i].Value != value {
			t.Fatalf("row %d = %q, want %q (the config's own order)", i, state.Modes[i].Value, value)
		}
		if state.Modes[i].Label != permission.Label(value) {
			t.Fatalf("row %d label = %q, want %q", i, state.Modes[i].Label, permission.Label(value))
		}
	}
}

// TestPermissionDefaultsAnEmptyModeToAsk covers an engine built without a mode.
// Sending "" would draw four rows and highlight none of them, so the adapter
// reports the posture the ladder actually applies.
func TestPermissionDefaultsAnEmptyModeToAsk(t *testing.T) {
	state, err := permissionHost(t, "").Permission()
	if err != nil {
		t.Fatalf("permission: %v", err)
	}
	if state.Mode != permission.ModeDefault {
		t.Fatalf("mode = %q, want %q", state.Mode, permission.ModeDefault)
	}
}

// TestSetPermissionSwitchesTheLiveEngine says the window's switch is the engine's
// switch: there is no window-local copy of the posture that could disagree with
// the ladder deciding the next tool call.
func TestSetPermissionSwitchesTheLiveEngine(t *testing.T) {
	engine := permission.New(permission.Options{Mode: permission.ModeDefault})
	host := New(&assembly.Kernel{Gate: assembly.NewGate(engine)})

	if err := host.SetPermission(permission.ModeOff); err != nil {
		t.Fatalf("set: %v", err)
	}
	if got := engine.Mode(); got != permission.ModeOff {
		t.Fatalf("engine mode = %q, want %q", got, permission.ModeOff)
	}
	if err := host.SetPermission("paranoid"); err == nil {
		t.Fatal("an unknown mode was accepted")
	}
}

// TestPermissionWithoutAnEngine says a kernel with no gate is told so instead of
// being handed an empty posture. An empty one reads as a mode named "".
func TestPermissionWithoutAnEngine(t *testing.T) {
	host := New(&assembly.Kernel{})
	if _, err := host.Permission(); err == nil {
		t.Fatal("a kernel with no gate reported a posture")
	}
	if err := host.SetPermission(permission.ModeOff); err == nil {
		t.Fatal("a kernel with no gate accepted a switch")
	}
}
