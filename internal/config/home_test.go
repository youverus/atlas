package config

import (
	"os"
	"path/filepath"

	"testing"
)

func TestDefaultHomeUsesTheEnvironment(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(homeEnv, "relative-home")
	home, err := DefaultHome()
	if err != nil {
		t.Fatalf("DefaultHome: %v", err)
	}
	// A relative $ATLAS_HOME must be resolved once: the home is compared
	// against absolute paths elsewhere.
	if !filepath.IsAbs(home) {
		t.Fatalf("home = %q, want an absolute path", home)
	}
	if filepath.Base(home) != "relative-home" {
		t.Fatalf("home = %q", home)
	}

	t.Setenv(homeEnv, dir)
	home, err = DefaultHome()
	if err != nil {
		t.Fatalf("DefaultHome: %v", err)
	}
	if home != dir {
		t.Fatalf("home = %q, want %q", home, dir)
	}
	path, err := DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath: %v", err)
	}
	if path != filepath.Join(dir, FileName) {
		t.Fatalf("path = %q", path)
	}
}

func TestDefaultHomeFallsBackToDotAtlas(t *testing.T) {
	t.Setenv(homeEnv, "")
	home, err := DefaultHome()
	if err != nil {
		t.Fatalf("DefaultHome: %v", err)
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	if home != filepath.Join(userHome, homeDirName) {
		t.Fatalf("home = %q, want ~/.atlas", home)
	}
}

func TestExpandHome(t *testing.T) {
	userHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	cases := map[string]string{
		"":                "",
		"/abs/path":       "/abs/path",
		"relative/path":   "relative/path",
		"~x":              "~x",
		"~":               userHome,
		"~/sessions":      filepath.Join(userHome, "sessions"),
		"/tmp/~/not-here": "/tmp/~/not-here",
	}
	for input, want := range cases {
		got, err := ExpandHome(input)
		if err != nil {
			t.Fatalf("ExpandHome(%q): %v", input, err)
		}
		if got != want {
			t.Fatalf("ExpandHome(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestWriteTemplateCreatesALoadableConfig(t *testing.T) {
	home := filepath.Join(t.TempDir(), "atlas")
	path, created, err := WriteTemplate(home)
	if err != nil {
		t.Fatalf("WriteTemplate: %v", err)
	}
	if !created || path != filepath.Join(home, FileName) {
		t.Fatalf("path = %q, created = %v", path, created)
	}

	// The template has to be valid as written, otherwise the first run hands
	// the user a file that fails before they can edit it.
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load(template): %v", err)
	}
	selection, err := cfg.Resolve("")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if selection.ProviderName != "deepseek" {
		t.Fatalf("selection = %q", selection.Name())
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("mode = %o, want 600", perm)
	}
	homeInfo, err := os.Stat(home)
	if err != nil {
		t.Fatalf("Stat(home): %v", err)
	}
	if perm := homeInfo.Mode().Perm(); perm != 0o700 {
		t.Fatalf("home mode = %o, want 700", perm)
	}

	example, err := os.ReadFile(filepath.Join(home, ExampleName))
	if err != nil {
		t.Fatalf("read example: %v", err)
	}
	if len(example) == 0 {
		t.Fatal("the example file is empty")
	}

	// The first run writes the appearance file too: config.json names it, so a
	// config that loads only because that file is missing would break the
	// first time the user edited anything.
	tuiPath := filepath.Join(home, TUIFileName)
	tuiInfo, err := os.Stat(tuiPath)
	if err != nil {
		t.Fatalf("Stat(tui): %v", err)
	}
	if perm := tuiInfo.Mode().Perm(); perm != 0o600 {
		t.Fatalf("tui mode = %o, want 600", perm)
	}
	if cfg.UI.Theme != "auto" {
		t.Fatalf("theme = %q, want the written template's", cfg.UI.Theme)
	}
	if on := cfg.UI.Blocks.Welcome.On; on == nil || !*on {
		t.Fatalf("blocks.welcome.on = %v, want the template's true", on)
	}
	// Every key is written out, including the one whose default is "no gap":
	// the file is the list of settings, and a setting nobody has seen is one
	// nobody will find.
	if gap := cfg.UI.Blocks.Status.GapAbove; gap == nil || *gap != 0 {
		t.Fatalf("blocks.status.gapAbove = %v, want the template's written zero", gap)
	}
	if frames := cfg.UI.Blocks.Transcript.Frames; frames == nil || !*frames {
		t.Fatalf("blocks.transcript.frames = %v, want the template's true", frames)
	}
}

func TestWriteTemplateNeverOverwrites(t *testing.T) {
	home := t.TempDir()
	path, _, err := WriteTemplate(home)
	if err != nil {
		t.Fatalf("WriteTemplate: %v", err)
	}
	edited := `{"providers": {"kept": {"api": "openai-completions", "models": [{"id": "m"}]}}}`
	if err := os.WriteFile(path, []byte(edited), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	again, created, err := WriteTemplate(home)
	if err != nil {
		t.Fatalf("WriteTemplate: %v", err)
	}
	if created {
		t.Fatal("WriteTemplate overwrote an existing config")
	}
	content, err := os.ReadFile(again)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(content) != edited {
		t.Fatalf("config = %q, want the edited file untouched", content)
	}
}

func TestWriteExclusiveRemovesAPartialWrite(t *testing.T) {
	// A directory where the file should go makes the open succeed and the
	// write fail on some systems, so the guard is checked with a path whose
	// parent is missing instead: it must not leave a zero-length file behind.
	dir := t.TempDir()
	path := filepath.Join(dir, "missing", FileName)
	if _, err := writeExclusive(path, "x"); err == nil {
		t.Fatal("writeExclusive created a file under a missing directory")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("Stat = %v, want not exist", err)
	}
}

func TestExampleTemplateIsValid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ExampleName)
	if err := os.WriteFile(path, []byte(exampleTemplate), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	// The example names the appearance file, so the sample pair has to be
	// written together for it to be loadable at all.
	if err := os.WriteFile(filepath.Join(dir, TUIFileName), []byte(tuiTemplate), 0o600); err != nil {
		t.Fatalf("WriteFile(tui): %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load(example): %v", err)
	}
	if len(cfg.Selections()) != 4 {
		t.Fatalf("example declares %d models, want 4", len(cfg.Selections()))
	}
	if selection, err := cfg.Resolve(""); err != nil || selection.Name() != "deepseek/deepseek-chat" {
		t.Fatalf("Resolve = %q, %v", selection.Name(), err)
	}
}
