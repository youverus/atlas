package assembly

// The tests for the two decisions a config makes about confinement and
// permission: which directories may not be read, and whether an "auto" mode has
// anything to judge with. They live here rather than in the command because the
// answers are built here now, and a window gets them from the same code.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pi-go/internal/config"
	"pi-go/internal/permission"
)

// TestSandboxForIncludesDefaultAndConfiguredDenyRead verifies that the sandbox
// wires both the default credential directories and the user's extra denyRead
// entries into the policy.
func TestSandboxForIncludesDefaultAndConfiguredDenyRead(t *testing.T) {
	extra := t.TempDir()
	list := denyReadFor(&config.Sandbox{DenyRead: []string{extra}})
	if !containsString(list, extra) {
		t.Fatalf("denyReadFor = %v, missing configured path %q", list, extra)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		wantSSH := filepath.Join(home, ".ssh")
		if !containsString(list, wantSSH) {
			t.Fatalf("denyReadFor = %v, missing default %q", list, wantSSH)
		}
	}
}

func containsString(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

// TestSandboxIsOffUnlessConfigured pins the three shapes of "no confinement
// asked for": no block, an empty block on a machine that cannot confine, and the
// nil sandbox the shell tool tests for.
func TestSandboxIsOffUnlessConfigured(t *testing.T) {
	applied, note := SandboxNote(nil)
	if applied || note != "" {
		t.Fatalf("SandboxNote(nil) = %v, %q; want no sandbox and no note", applied, note)
	}
	if box := confine(nil, t.TempDir()); box != nil {
		t.Fatalf("confine(nil) = %v, want no confinement", box)
	}
	if tool := narrow(nil); tool != nil {
		t.Fatalf("narrow(nil) = %v, want the untyped nil the shell tool tests for", tool)
	}

	cwd := t.TempDir()
	box := confine(&config.Sandbox{}, cwd)
	if box == nil {
		t.Fatal("an empty sandbox block produced no sandbox")
	}
	// The writable root is the workspace the tools run in, not the one the
	// process started in: that is the whole reason the confinement is built per
	// directory.
	if err := box.Available(); err != nil {
		// The executor is a fact about this machine. When it is missing the
		// warning has to say so, and no sandbox may be reported as applied.
		applied, note = SandboxNote(&config.Sandbox{})
		if applied || note == "" {
			t.Fatalf("SandboxNote on a machine without an executor = %v, %q; want a warning", applied, note)
		}
		return
	}
	if applied, note := SandboxNote(&config.Sandbox{}); !applied || note != "" {
		t.Fatalf("SandboxNote = %v, %q; want the sandbox reported as in effect", applied, note)
	}
}

// TestAutoWithoutAPermissionBlockStillJudges: -permission auto on a config with no
// permission block at all. The documented rule is that an empty classifier falls
// back to the model the run started on, and "no block" is the emptiest value there
// is; returning nil here would make the flag a switch that quietly does nothing.
func TestAutoWithoutAPermissionBlockStillJudges(t *testing.T) {
	cfg := &config.Config{}
	selection := config.Selection{
		ProviderName: "local",
		Provider:     config.Provider{API: "openai-completions"},
		Model:        config.Model{ID: "m", MaxTokens: 4096},
	}
	classifier, note := classifierFor(cfg, permission.ModeAuto, selection)
	if note != "" {
		t.Fatalf("note = %q, want no warning about a model that builds", note)
	}
	if classifier == nil {
		t.Fatal("auto built no classifier from the starting model")
	}
	// Any other mode never asks, so no classifier is the right answer.
	if built, note := classifierFor(cfg, permission.ModeDefault, selection); built != nil || note != "" {
		t.Fatalf("classifierFor(default) = %v %q, want nothing", built, note)
	}
}

// TestAutoWithNoClassifierWarnsAndAsks is the other half of the fallback: a
// classifier named in the config that cannot be built is a warning, and the mode
// stays auto with no judge rather than silently becoming "always ask".
func TestAutoWithNoClassifierWarnsAndAsks(t *testing.T) {
	cfg := &config.Config{Permission: &config.Permission{
		Mode:       permission.ModeAuto,
		Classifier: "nope/model",
	}}
	engine, note := PermissionFor(cfg, false, config.Selection{}, "")
	if note == "" || !strings.Contains(note, "nope/model") {
		t.Fatalf("note = %q, want the unusable classifier named", note)
	}
	if engine.Mode() != permission.ModeAuto {
		t.Fatalf("mode = %q, want auto to stay selected", engine.Mode())
	}
}
