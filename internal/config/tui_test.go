package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pointed writes a config that names a tui file and the tui file itself, so a
// test can say what it is testing in one line.
func pointed(t *testing.T, tuiBody string) string {
	t.Helper()
	dir := t.TempDir()
	write(t, dir, TUIFileName, tuiBody)
	return write(t, dir, FileName, `{
  "tui": "`+TUIFileName+`",
  "providers": {"local": {"api": "openai-completions", "models": [{"id": "m"}]}}
}`)
}

// TestLoadDecodesTheTUIFile pins the two halves of the file: colors stay
// strings for internal/theme to parse, and a layout field that is absent stays
// absent while a field written as zero keeps its zero.
func TestLoadDecodesTheTUIFile(t *testing.T) {
	path := pointed(t, `{
  "theme": "atlas-dark",
  "colors": {"accent": "#ff0000", "background": "default"},
  "blocks": {"transcript": {"gap": 1, "frames": false},
             "composer": {"gapAbove": 0, "rows": 3}, "status": {"gapAbove": 2}, "welcome": {"on": false}}
}`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.UI.Theme != "atlas-dark" {
		t.Fatalf("theme = %q", cfg.UI.Theme)
	}
	if got := cfg.UI.Colors["accent"]; got != "#ff0000" {
		t.Fatalf("colors.accent = %q", got)
	}
	if got := cfg.UI.Colors["background"]; got != "default" {
		t.Fatalf("colors.background = %q", got)
	}
	composer := cfg.UI.Blocks.Composer
	if composer.GapAbove == nil || *composer.GapAbove != 0 {
		t.Fatalf("blocks.composer.gapAbove = %v, want a written zero", composer.GapAbove)
	}
	if composer.Rows == nil || *composer.Rows != 3 {
		t.Fatalf("blocks.composer.rows = %v, want 3", composer.Rows)
	}
	// The gap under the prompt is its own key rather than a second meaning for
	// the gap above it: the two sides of the input area are separate decisions.
	if gap := cfg.UI.Blocks.Status.GapAbove; gap == nil || *gap != 2 {
		t.Fatalf("blocks.status.gapAbove = %v, want 2", gap)
	}
	if on := cfg.UI.Blocks.Welcome.On; on == nil || *on {
		t.Fatalf("blocks.welcome.on = %v, want false", on)
	}
	// A switch beside a number in the same block: one key set does not reach
	// the other, and false is a real setting rather than a missing one.
	if frames := cfg.UI.Blocks.Transcript.Frames; frames == nil || *frames {
		t.Fatalf("blocks.transcript.frames = %v, want false", frames)
	}
	if gap := cfg.UI.Blocks.Transcript.Gap; gap == nil || *gap != 1 {
		t.Fatalf("blocks.transcript.gap = %v, want 1", gap)
	}
	// Not written is not the same as written as zero: the interface has to be
	// able to tell "leave this alone" from "make it nothing".
	if cfg.UI.Blocks.Composer.Frame != nil || cfg.UI.Roles.Thinking.Rows != nil || cfg.UI.Blocks.Menu.Width != nil {
		t.Fatalf("an absent field came back set: %+v", cfg.UI.Layout)
	}
}

// TestLoadWithoutTUIKeepsTheDefaults: the key is optional, and leaving it out
// must not send this package looking for a file it was never told about.
func TestLoadWithoutTUIKeepsTheDefaults(t *testing.T) {
	path := write(t, t.TempDir(), FileName, `{
  "providers": {"local": {"api": "openai-completions", "models": [{"id": "m"}]}}
}`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.TUI != nil {
		t.Fatalf("tui = %q, want the key left unset", *cfg.TUI)
	}
	if cfg.UI.Theme != "" || cfg.UI.Colors != nil || cfg.UI.Blocks.Composer.GapAbove != nil {
		t.Fatalf("ui came back set: %+v", cfg.UI)
	}
}

// TestTUIPathIsRelativeToTheConfig is the whole reason the path is resolved
// here: the same file has to be found whatever directory atlas was started
// from, and a test run from the package directory is not that directory.
func TestTUIPathIsRelativeToTheConfig(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "settings")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	write(t, nested, TUIFileName, `{"theme": "atlas-light"}`)
	path := write(t, nested, FileName, `{
  "tui": "`+TUIFileName+`",
  "providers": {"local": {"api": "openai-completions", "models": [{"id": "m"}]}}
}`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.UI.Theme != "atlas-light" {
		t.Fatalf("theme = %q, want the file next to the config", cfg.UI.Theme)
	}
}

// TestTUIPathAcceptsAnAbsoluteName: "tui" is a path, so a reader who keeps the
// appearance in a dotfiles checkout may say so outright.
func TestTUIPathAcceptsAnAbsoluteName(t *testing.T) {
	dir := t.TempDir()
	absolute := write(t, dir, "appearance.json", `{"theme": "atlas-light"}`)
	path := write(t, t.TempDir(), FileName, `{
  "tui": "`+absolute+`",
  "providers": {"local": {"api": "openai-completions", "models": [{"id": "m"}]}}
}`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.UI.Theme != "atlas-light" {
		t.Fatalf("theme = %q", cfg.UI.Theme)
	}
}

// TestTUIPathExpandsHome: the docs write "~/..." everywhere else, so this key
// has to mean the same thing.
func TestTUIPathExpandsHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	write(t, home, TUIFileName, `{"theme": "atlas-light"}`)
	path := write(t, t.TempDir(), FileName, `{
  "tui": "~/`+TUIFileName+`",
  "providers": {"local": {"api": "openai-completions", "models": [{"id": "m"}]}}
}`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.UI.Theme != "atlas-light" {
		t.Fatalf("theme = %q", cfg.UI.Theme)
	}
}

// TestLoadNamesAMissingTUIFile: the failure is the reader's to fix, and the
// message has to say which file they asked for.
func TestLoadNamesAMissingTUIFile(t *testing.T) {
	path := write(t, t.TempDir(), FileName, `{
  "tui": "missing.json",
  "providers": {"local": {"api": "openai-completions", "models": [{"id": "m"}]}}
}`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load accepted a missing tui file")
	}
	text := err.Error()
	if !strings.Contains(text, "config: tui:") || !strings.Contains(text, "missing.json") {
		t.Fatalf("error = %v, want it to name the tui file", err)
	}
}

// TestLoadRejectsAnEmptyTUIValue: writing the key is a statement that there is
// a file, so a blank value is a path someone deleted rather than a request for
// the built-in look. Accepting it would make `"tui": ""` a silent synonym for
// omitting the key, and the reader would go on editing a name nothing reads.
func TestLoadRejectsAnEmptyTUIValue(t *testing.T) {
	for _, value := range []string{"", "   "} {
		path := write(t, t.TempDir(), FileName, `{
  "tui": "`+value+`",
  "providers": {"local": {"api": "openai-completions", "models": [{"id": "m"}]}}
}`)
		_, err := Load(path)
		if err == nil || !strings.Contains(err.Error(), "is empty") {
			t.Fatalf("tui = %q: error = %v, want an empty-path error", value, err)
		}
		if !strings.Contains(err.Error(), `leave the key out`) {
			t.Fatalf("tui = %q: error = %v, want it to name the way out", value, err)
		}
	}
}

// TestLoadRejectsAnUnknownTUIField: the appearance file is the most
// hand-edited one atlas has, so a misspelled key in it has to fail loudly
// rather than leave the interface at whatever the default was.
func TestLoadRejectsAnUnknownTUIField(t *testing.T) {
	path := pointed(t, `{"colour": {"accent": "#ff0000"}}`)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "colour") {
		t.Fatalf("error = %v, want it to name the unknown field", err)
	}
}

// TestTUIFileRejectsTrailingData: the same paste twice mistake is available in
// either file, so the check is not the config's alone.
func TestTUIFileRejectsTrailingData(t *testing.T) {
	path := pointed(t, `{"theme": "auto"}`+"\n{}\n")
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "trailing data") {
		t.Fatalf("error = %v, want a trailing-data error", err)
	}
}

// TestTUIFileValidatesTheLayout: geometry is the part of the appearance this
// package can judge on its own, and the error has to point at the file whose
// number is wrong.
func TestTUIFileValidatesTheLayout(t *testing.T) {
	path := pointed(t, `{"blocks": {"composer": {"rows": 0}}}`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load accepted a composer that cannot show a line")
	}
	text := err.Error()
	if !strings.Contains(text, "blocks.composer.rows") {
		t.Fatalf("error = %v, want it to name the block and the field", err)
	}
	if !strings.Contains(text, TUIFileName) {
		t.Fatalf("error = %v, want it to name the file", err)
	}
}

// TestMovedHintTellsTheReaderWhereTheKeysWent: 0.4.10 kept the appearance in
// config.json, and a reader upgrading by hand will copy those keys back in.
// "unknown field" alone would send them looking for a typo instead of a move.
func TestMovedHintTellsTheReaderWhereTheKeysWent(t *testing.T) {
	for _, key := range []string{"theme", "ui"} {
		path := write(t, t.TempDir(), FileName, `{
  "`+key+`": "auto",
  "providers": {"local": {"api": "openai-completions", "models": [{"id": "m"}]}}
}`)
		_, err := Load(path)
		if err == nil {
			t.Fatalf("Load accepted the old %q key", key)
		}
		text := err.Error()
		if !strings.Contains(text, key) {
			t.Fatalf("error = %v, want it to name %q", err, key)
		}
		if !strings.Contains(text, `"tui"`) {
			t.Fatalf("error = %v, want it to point at the \"tui\" key", err)
		}
	}
}

// TestMovedLayoutTellsTheReaderWhereTheNumbersWent: 0.4.11 wrote the geometry
// as one flat "layout" object, and that file has to be refused with directions
// rather than with a bare "unknown field".
func TestMovedLayoutTellsTheReaderWhereTheNumbersWent(t *testing.T) {
	// The whole object, as 0.4.11 wrote it.
	path := pointed(t, `{"layout": {"composerGap": 2}}`)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), `unknown field "layout"`) {
		t.Fatalf("error = %v, want it to name the old key", err)
	}
	if !strings.Contains(err.Error(), `"blocks"`) {
		t.Fatalf("error = %v, want it to point at the new shape", err)
	}
	// And the half-migration: the wrapper deleted, the numbers left flat. The
	// ten pairs are spelled out rather than read from layoutMoves, because a
	// test that walks the table it is testing goes green when a row is deleted.
	for _, move := range []struct{ was, now string }{
		{"blockGap", "blocks.transcript.gap"},
		{"composerGap", "blocks.composer.gapAbove"},
		{"composerRows", "blocks.composer.rows"},
		{"paletteRows", "blocks.palette.rows"},
		{"statusGap", "blocks.status.gapAbove"},
		{"menuRows", "blocks.menu.rows"},
		{"menuWidth", "blocks.menu.width"},
		{"boxWidth", "blocks.approval.width"},
		{"thinkingRows", "roles.thinking.rows"},
		{"homePage", "blocks.welcome.on"},
	} {
		path := pointed(t, `{"`+move.was+`": 1}`)
		_, err := Load(path)
		if err == nil {
			t.Fatalf("%s: Load accepted a flat geometry key", move.was)
		}
		if !strings.Contains(err.Error(), move.was) {
			t.Fatalf("%s: error = %v, want it to name the key", move.was, err)
		}
		if !strings.Contains(err.Error(), move.now) {
			t.Fatalf("%s: error = %v, want it to point at %s", move.was, err, move.now)
		}
	}
}

// TestAnUnknownKeyInsideABlockNamesTheBlock: "rows" is a key of four blocks and
// "gap"-like names are close to each other, so the bare field name the decoder
// reports by default would not say where the typo is.
func TestAnUnknownKeyInsideABlockNamesTheBlock(t *testing.T) {
	for _, row := range []struct{ body, path string }{
		{`{"blocks": {"composer": {"row": 3}}}`, "blocks.composer"},
		{`{"roles": {"thinking": {"row": 3}}}`, "roles.thinking"},
	} {
		path := pointed(t, row.body)
		_, err := Load(path)
		if err == nil {
			t.Fatalf("%s: Load accepted an unknown key", row.body)
		}
		if !strings.Contains(err.Error(), row.path) {
			t.Fatalf("%s: error = %v, want it to name %s", row.body, err, row.path)
		}
	}
}

// TestResolveTUIPathTrimsASpacedName: a hand-written path with a trailing
// space is the same path, and failing on it would be a puzzle.
func TestResolveTUIPathTrimsASpacedName(t *testing.T) {
	got, err := resolveTUIPath("  tui.json  ", filepath.Join("/settings", FileName))
	if err != nil {
		t.Fatalf("resolveTUIPath: %v", err)
	}
	if want := filepath.Join("/settings", TUIFileName); got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}
}
