package main

// What a typed "@path" means: the parser, the blocks it becomes, and the list the
// palette offers while it is being typed.

import (
	"context"
	"encoding/base64"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pi-go/internal/app"
	"pi-go/internal/provider/faux"
	"pi-go/internal/session"
	"pi-go/internal/tool/fs"
	"pi-go/internal/tui"
	"pi-go/internal/wire"
)

// tinyPNG is a header the image sniff accepts: a PNG signature and an IHDR chunk
// of the declared length. Nothing decodes it, which is the point — the sniff is
// what decides inline image versus text, and it has to be the same sniff the read
// tool uses.
var tinyPNG = append(append([]byte{}, 0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a),
	0x00, 0x00, 0x00, 0x0d, 'I', 'H', 'D', 'R', 0x00, 0x00, 0x01, 0x00)

func TestParseAttachRefs(t *testing.T) {
	for _, tc := range []struct {
		name string
		line string
		want []string // the tokens, in order
	}{
		{"relative", "explain @main.go", []string{"main.go"}},
		{"absolute", "read @/etc/hosts", []string{"/etc/hosts"}},
		{"tilde", "read @~/notes.md", []string{"~/notes.md"}},
		{"leading", "@main.go is the entry", []string{"main.go"}},
		{"quoted space", `look at @"my shot.png"`, []string{"my shot.png"}},
		{"quoted then prose", `@"a b.png" and @"c d.png"`, []string{"a b.png", "c d.png"}},
		{"after a bracket", "see (@main.go)", []string{"main.go)"}},
		{"mid word is prose", "mail bob@example.com", nil},
		{"bare at is the palette", "look at @", nil},
		{"trailing space ends it", "look at @main.go ", []string{"main.go"}},
		{"two in one line", "@a.go and @b.go", []string{"a.go", "b.go"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			refs, err := parseAttachRefs(tc.line)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if strings.Join(refs, "|") != strings.Join(tc.want, "|") {
				t.Fatalf("refs = %v, want %v", refs, tc.want)
			}
		})
	}
}

// A quoted path that lost its closing quote is refused rather than treated as
// prose: the reader meant a file with a space in its name, and sending the
// sentence without it answers a question they did not ask.
func TestParseAttachRefsRefusesAnUnclosedQuote(t *testing.T) {
	_, err := parseAttachRefs(`look at @"my shot.png`)
	if err == nil || !strings.Contains(err.Error(), "closing quote") {
		t.Fatalf("error = %v, want a missing-quote error", err)
	}
}

func TestAttachMessageInlinesText(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	message, err := attachMessage("explain @main.go", cwd)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	blocks := message.Content.Items()
	if len(blocks) != 2 {
		t.Fatalf("blocks = %d, want the line and the file", len(blocks))
	}
	if blocks[0].Text != "explain @main.go" {
		t.Fatalf("first block = %q, want the line as typed", blocks[0].Text)
	}
	want := `<file name="` + filepath.Join(cwd, "main.go") + "\">\npackage main\n\n</file>"
	if blocks[1].Text != want {
		t.Fatalf("tag = %q, want %q", blocks[1].Text, want)
	}
}

// A reference ends at whitespace, so the punctuation that follows it in prose
// arrives as part of the path. The path is tried as typed first, then without the
// punctuation — never the other way round, so a file named "notes." still wins
// over "notes".
func TestAttachTrimsPunctuationThatProseAdded(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	for _, line := range []string{"read @main.go.", "see (@main.go)", "read @main.go,"} {
		message, err := attachMessage(line, cwd)
		if err != nil {
			t.Fatalf("%q: %v", line, err)
		}
		blocks := message.Content.Items()
		if len(blocks) != 2 || !strings.Contains(blocks[1].Text, "package main") {
			t.Fatalf("%q attached %+v, want main.go", line, blocks)
		}
	}
}

func TestAttachMessageInlinesAnImage(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "shot.png"), tinyPNG, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	message, err := attachMessage("what is @"+"shot.png", cwd)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	blocks := message.Content.Items()
	if len(blocks) != 3 {
		t.Fatalf("blocks = %d, want the line, the tag and the image", len(blocks))
	}
	if want := `<file name="` + filepath.Join(cwd, "shot.png") + `"></file>`; blocks[1].Text != want {
		t.Fatalf("tag = %q, want %q", blocks[1].Text, want)
	}
	if blocks[2].Type != wire.ContentImage || blocks[2].MIMEType != "image/png" {
		t.Fatalf("block 2 = %+v, want an inline png", blocks[2])
	}
	if blocks[2].Data != base64.StdEncoding.EncodeToString(tinyPNG) {
		t.Fatalf("image data is not the file's bytes")
	}
}

// A path with a space in it is reachable, and it resolves to the same file the
// tools would open: "@" plus quotes is the whole syntax.
func TestAttachTakesAPathWithSpaces(t *testing.T) {
	cwd := t.TempDir()
	name := filepath.Join(cwd, "my shot.png")
	if err := os.WriteFile(name, tinyPNG, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	message, err := attachMessage(`what is @"my shot.png"`, cwd)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if blocks := message.Content.Items(); len(blocks) != 3 {
		t.Fatalf("blocks = %d, want the line, the tag and the image", len(blocks))
	}

	// The absolute form works the same way, which is the other half of the rule;
	// the quotes are still the thing that carries the space.
	absolute, err := attachMessage(`what is @"`+name+`"`, cwd)
	if err != nil {
		t.Fatalf("absolute: %v", err)
	}
	if blocks := absolute.Content.Items(); len(blocks) != 3 {
		t.Fatalf("absolute blocks = %d, want 3", len(blocks))
	}
}

// Every one of these is a reference that cannot become content. Each is an error
// naming the reference: a turn sent without it would be answered as if the file
// had been read.
func TestAttachRefusesWhatItCannotAttach(t *testing.T) {
	cwd := t.TempDir()
	if err := os.Mkdir(filepath.Join(cwd, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	big := strings.Repeat("x", attachMaxTextBytes+1)
	if err := os.WriteFile(filepath.Join(cwd, "big.txt"), []byte(big), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	for _, tc := range []struct {
		line string
		want string
	}{
		{"read @missing.go", "no such file"},
		{"read @sub", "is a directory"},
		{"read @big.txt", "ask for it to be read instead"},
	} {
		t.Run(tc.line, func(t *testing.T) {
			_, err := attachMessage(tc.line, cwd)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestAttachSuggestionsListOneDirectory(t *testing.T) {
	cwd := t.TempDir()
	for _, name := range []string{"main.go", "mod.go", "README.md", ".hidden"} {
		if err := os.WriteFile(filepath.Join(cwd, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := os.Mkdir(filepath.Join(cwd, "internal"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	picks := attachSuggestions("explain @m", cwd)
	names := pickNames(picks)
	if strings.Join(names, "|") != "@main.go|@mod.go" {
		t.Fatalf("picks = %v, want the two files that start with m", names)
	}
	if picks[0].Replace != len([]rune("@m")) {
		t.Fatalf("replace = %d, want the length of the token", picks[0].Replace)
	}
	if picks[0].About != "1B" {
		t.Fatalf("about = %q, want a size", picks[0].About)
	}

	// A dot file is a decision: it appears once the dot is typed.
	if got := pickNames(attachSuggestions("explain @.", cwd)); strings.Join(got, "|") != "@.hidden" {
		t.Fatalf("dot picks = %v, want the dot file only", got)
	}

	// A directory is offered with its separator, so the next keystroke keeps
	// drilling instead of sending.
	if got := pickNames(attachSuggestions("explain @i", cwd)); strings.Join(got, "|") != "@internal/" {
		t.Fatalf("dir picks = %v, want @internal/", got)
	}
	if about := attachSuggestions("explain @i", cwd)[0].About; about != "directory" {
		t.Fatalf("about = %q, want directory", about)
	}
}

func TestAttachSuggestionsKeepTheTypedPrefix(t *testing.T) {
	cwd := t.TempDir()
	dir := filepath.Join(cwd, "my dir")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "file.png"), tinyPNG, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Inside a quote the prefix keeps its quotes, and a file name with a space
	// comes back quoted: the sender reads back exactly what the palette filled.
	picks := attachSuggestions(`look at @"my d`, cwd)
	if len(picks) != 1 {
		t.Fatalf("picks = %v, want the one directory", pickNames(picks))
	}
	if picks[0].Name != `@"my dir/` {
		t.Fatalf("name = %q, want the quote left open for the next segment", picks[0].Name)
	}

	closed := `@"my dir/file.png"`
	if picks := attachSuggestions("look at "+closed, cwd); len(picks) != 1 || picks[0].Name != closed || !isAttachImage(picks[0].Path) {
		t.Fatalf("quoted image picks = %+v, want the exact image", picks)
	}

	// Absolute prefixes stay absolute.
	if got := pickNames(attachSuggestions("look at @"+filepath.Dir(cwd)+"/", cwd)); len(got) == 0 {
		t.Fatalf("an absolute prefix offered nothing")
	}
	if picks := attachSuggestions(`look at @"`+dir+`/`, cwd); len(picks) != 1 || picks[0].Name != `@"`+dir+`/file.png"` {
		t.Fatalf("picks = %v, want the quoted file inside the quoted directory", pickNames(picks))
	}
}

func TestAttachSuggestionsOnlyAtTheEndOfTheLine(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "main.go"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	for _, line := range []string{
		"explain @main.go and then", // prose follows the reference
		"mail bob@example.com",      // an address, not a reference
		"look at @main.go ",         // the reader moved past it
		"no reference here",
	} {
		if picks := attachSuggestions(line, cwd); len(picks) != 0 {
			t.Fatalf("%q offered %v, want nothing", line, pickNames(picks))
		}
	}
}

// TestTurnSendsTheAttachedImage checks the prompt the provider receives after an image pick.
func TestTurnSendsTheAttachedImage(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "shot.png"), tinyPNG, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	application, provider := attachApp(t, cwd, faux.Response{Text: "a cat"})

	picks := attachSuggestions("what is @shot", cwd)
	if len(picks) != 1 || !isAttachImage(picks[0].Path) {
		t.Fatalf("image pick = %+v", picks)
	}
	if err := tuiMainTurn(context.Background(), application, "what is @shot.png", (&homeSink{}).sink(), picks[0].Path); err != nil {
		t.Fatalf("turn: %v", err)
	}

	calls := provider.Calls()
	if len(calls) != 1 {
		t.Fatalf("made %d requests, want one", len(calls))
	}
	last := calls[0].Messages[len(calls[0].Messages)-1]
	blocks := last.Content.Items()
	if len(blocks) != 3 {
		t.Fatalf("blocks = %d, want the line, the tag and the image", len(blocks))
	}
	if blocks[0].Text != "what is @shot.png" {
		t.Fatalf("the line was rewritten: %q", blocks[0].Text)
	}
	if blocks[2].Type != wire.ContentImage || blocks[2].MIMEType != "image/png" {
		t.Fatalf("no image reached the provider: %+v", blocks[2])
	}
}

func TestInteractivePathsRemainPromptText(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "shot.png"), tinyPNG, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "main.go"), []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}
	line := `看看 @shot.png @main.go @./ @missing @"带空格的目录/"`
	t.Run("main", func(t *testing.T) {
		application, provider := attachApp(t, cwd, faux.Response{Text: "ok"})
		if err := tuiMainTurn(context.Background(), application, line, (&homeSink{}).sink()); err != nil {
			t.Fatal(err)
		}
		assertPlainAttachmentPrompt(t, provider, line)
	})
	t.Run("side command", func(t *testing.T) {
		application, provider := attachApp(t, cwd, faux.Response{Text: "ok"})
		state := &sideState{}
		render := newRenderer(io.Discard, io.Discard, false)
		if _, err := replCommand(context.Background(), application, state, "/btw "+line, models{}, nil, nil, emptyPrompts(), io.Discard, render, mcpState{}, nil); err != nil {
			t.Fatal(err)
		}
		assertPlainAttachmentPrompt(t, provider, line)
	})
}

func assertPlainAttachmentPrompt(t *testing.T, provider *faux.Provider, want string) {
	t.Helper()
	calls := provider.Calls()
	if len(calls) != 1 {
		t.Fatalf("made %d requests, want one", len(calls))
	}
	messages := calls[0].Messages
	content := messages[len(messages)-1].Content
	if !content.IsPlain() || content.PlainText() != want {
		t.Fatalf("content = %+v, want plain %q", content, want)
	}
}

func TestSelectedImageChangedBeforeSend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shot.png")
	if err := os.WriteFile(path, []byte("now text"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := interactiveMessage("@shot.png", []string{path}); err == nil || !strings.Contains(err.Error(), "no longer a supported image") {
		t.Fatalf("error = %v, want unsupported image error", err)
	}
}

// TestHeadlessRefusesAMissingAttachment: the failure has to be the turn's, not a
// sentence the model answers as if the file had been read. Without a terminal
// there is no palette to explain it, so the error is the whole report.
func TestHeadlessRefusesAMissingAttachment(t *testing.T) {
	cwd := t.TempDir()
	application, provider := attachApp(t, cwd, faux.Response{Text: "unused"})

	err := askLine(context.Background(), application, "read @missing.go", newRenderer(os.Stderr, os.Stderr, false))
	if err == nil || !strings.Contains(err.Error(), "no such file") {
		t.Fatalf("error = %v, want a missing-file error", err)
	}
	if calls := provider.Calls(); len(calls) != 0 {
		t.Fatalf("sent %d requests, want none", len(calls))
	}
}

// attachApp builds an app whose session lives in cwd, with the provider kept so a
// test can read what was sent.
func attachApp(t *testing.T, cwd string, responses ...faux.Response) (*app.App, *faux.Provider) {
	t.Helper()
	provider := faux.New(responses...)
	manager, err := session.InMemory(cwd, session.Options{})
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	application, err := app.New(app.Config{
		Model:        wire.Model{ID: "faux-1", Name: "Faux", API: "faux", Provider: "faux"},
		Provider:     provider,
		SystemPrompt: app.StaticPrompt("test"),
		Tools:        app.StaticTools(fs.Tools(fs.Options{Cwd: cwd})),
	}, manager)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	return application, provider
}

func pickNames(picks []tui.Suggestion) []string {
	names := make([]string, 0, len(picks))
	for _, pick := range picks {
		names = append(names, pick.Name)
	}
	return names
}
