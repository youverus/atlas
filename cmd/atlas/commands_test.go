package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pi-go/internal/app"
	"pi-go/internal/assembly"
	"pi-go/internal/provider/faux"
	"pi-go/internal/session"
	"pi-go/internal/tool/fs"
	"pi-go/internal/wire"
)

// commandApp builds an app whose conversation runs in cwd, with the file tools
// mounted as they are in a real session. Commands that touch the working tree
// need the same tools the user's turn would have.
func commandApp(t *testing.T, manager *session.Manager, cwd string, responses ...faux.Response) *app.App {
	t.Helper()
	return commandAppWith(t, manager, cwd, assembly.NewPrompts("", false, true).Source(), responses...)
}

// commandAppWith is commandApp for a session whose prompt comes from a source.
// The app and the commands have to share that source: the app asks it again
// for every turn, and a command that reports what the prompt holds has to
// report the same one.
func commandAppWith(t *testing.T, manager *session.Manager, cwd string, prompts *assembly.PromptSource, responses ...faux.Response) *app.App {
	t.Helper()
	application, err := app.New(app.Config{
		Model:        wire.Model{ID: "faux-1", Name: "Faux", API: "faux", Provider: "faux"},
		Provider:     faux.New(responses...),
		SystemPrompt: prompts.Prompt,
		Tools:        app.StaticTools(fs.Tools(fs.Options{Cwd: cwd})),
	}, manager)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	return application
}

// memoryAt is an in-memory session whose cwd is dir, which is what the export
// and init paths resolve their relative paths against.
func memoryAt(t *testing.T, dir string) *session.Manager {
	t.Helper()
	manager, err := session.InMemory(dir, session.Options{})
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	return manager
}

// runCommand dispatches one slash command and returns its output.
func runCommand(t *testing.T, application *app.App, line string) (string, error) {
	t.Helper()
	return runCommandWith(t, application, assembly.NewPrompts("", false, true).Source(), line)
}

// runCommandWith is runCommand for the commands that ask about the prompt:
// /init and /reload take the same source the session was started with, and the
// answer depends on it.
func runCommandWith(t *testing.T, application *app.App, prompts *assembly.PromptSource, line string) (string, error) {
	t.Helper()
	out := &bytes.Buffer{}
	render := newRenderer(out, out, false)
	done, err := replCommand(context.Background(), application, nil, line, models{}, newSessionPicker(application, ""), nil, prompts, out, render, mcpState{}, nil, nil)
	if done {
		t.Fatalf("%s ended the session", line)
	}
	return out.String(), err
}

// TestExportMarkdownPinsTheDocument is the format: anyone who keeps an exported
// transcript has to be able to read it, so the shape is a contract, not an
// implementation detail.
func TestExportMarkdownPinsTheDocument(t *testing.T) {
	messages := []wire.Message{
		wire.SystemMessage("you are an agent"),
		wire.UserMessage("hi"),
		{
			Role: wire.RoleAssistant,
			Content: wire.NewBlocks(
				wire.ThinkingContent("think twice", ""),
				wire.TextContent("answer"),
				wire.ToolCallContent("call_1", "bash", json.RawMessage(`{"command":"ls"}`)),
			),
			StopReason: wire.StopToolUse,
		},
		{
			Role:       wire.RoleToolResult,
			Content:    wire.NewBlocks(wire.TextContent("a.txt")),
			ToolName:   "bash",
			ToolCallID: "call_1",
			IsError:    wire.Ptr(false),
		},
		{
			Role:         wire.RoleAssistant,
			Content:      wire.NewBlocks(wire.TextContent("done")),
			StopReason:   wire.StopError,
			ErrorMessage: "rate limited",
		},
	}
	header := exportHeader{
		name:     "review",
		session:  "01a16ec2-2818-7101",
		cwd:      "/tmp/project",
		model:    "test/m1",
		exported: time.Date(2026, 10, 25, 1, 2, 3, 0, time.UTC),
	}

	want := `# review

- session: 01a16ec2-2818-7101
- cwd: /tmp/project
- model: test/m1
- messages: 4
- exported: 2026-10-25T01:02:03Z

---

## User

hi

## Assistant

<details>
<summary>Thinking</summary>

think twice

</details>

answer

**Tool call: bash**

` + "```json" + `
{
  "command": "ls"
}
` + "```" + `

## Tool result: bash

` + "```text" + `
a.txt
` + "```" + `

## Assistant

done

*error: rate limited*
`

	got := &bytes.Buffer{}
	if err := exportMarkdown(got, header, messages); err != nil {
		t.Fatalf("export: %v", err)
	}
	if got.String() != want {
		t.Fatalf("document mismatch\n--- got ---\n%s\n--- want ---\n%s", got.String(), want)
	}
}

// An export is a file to read, not a file to break: output that contains a
// fence gets a longer fence instead of ending the block early.
func TestExportFenceOutlivesItsContent(t *testing.T) {
	messages := []wire.Message{
		{
			Role:     wire.RoleToolResult,
			Content:  wire.NewBlocks(wire.TextContent("```go\nfmt.Println()\n```")),
			ToolName: "read",
		},
	}
	got := &bytes.Buffer{}
	if err := exportMarkdown(got, exportHeader{session: "abc"}, messages); err != nil {
		t.Fatalf("export: %v", err)
	}
	if !strings.Contains(got.String(), "````text\n```go\nfmt.Println()\n```\n````") {
		t.Fatalf("document = %q, want a four-backtick fence", got.String())
	}
}

// The header falls back to the session id when the session has no name, and
// the system message never reaches the file.
func TestExportTitlesAnUnnamedSession(t *testing.T) {
	got := &bytes.Buffer{}
	if err := exportMarkdown(got, exportHeader{session: "01a16ec2-2818"}, []wire.Message{
		wire.SystemMessage("do not export me"),
		wire.UserMessage("hi"),
	}); err != nil {
		t.Fatalf("export: %v", err)
	}
	text := got.String()
	if !strings.HasPrefix(text, "# atlas session 01a16ec2\n") {
		t.Fatalf("document starts with %q", firstLine(text))
	}
	if strings.Contains(text, "do not export me") {
		t.Fatal("the system message was exported")
	}
	if !strings.Contains(text, "- messages: 1") {
		t.Fatalf("document = %q, want one exported message", text)
	}
}

func firstLine(text string) string {
	if at := strings.IndexByte(text, '\n'); at >= 0 {
		return text[:at]
	}
	return text
}

// /export with no argument writes into the session cwd under a name derived
// from the session id, so two exports of one session update one file.
func TestExportWritesTheSessionToCwd(t *testing.T) {
	cwd := t.TempDir()
	manager := memoryAt(t, cwd)
	application := commandApp(t, manager, cwd, faux.Response{Text: "answer"})
	if _, err := application.Ask(context.Background(), wire.UserMessage("hi"), nil); err != nil {
		t.Fatalf("ask: %v", err)
	}

	printed, err := runCommand(t, application, "/export")
	if err != nil {
		t.Fatalf("/export: %v", err)
	}

	want := filepath.Join(cwd, "atlas-session-"+shortSessionID(manager.ID())+".md")
	if !strings.Contains(printed, "exported: "+want+" (") {
		t.Fatalf("output = %q, want the path %q", printed, want)
	}
	content, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("read the export: %v", err)
	}
	for _, want := range []string{"## User\n\nhi", "## Assistant\n\nanswer", "- model: faux/faux-1"} {
		if !strings.Contains(string(content), want) {
			t.Fatalf("export = %q, want %q", content, want)
		}
	}

	// A second export replaces the first instead of adding a copy.
	if _, err := runCommand(t, application, "/export"); err != nil {
		t.Fatalf("second /export: %v", err)
	}
	entries, err := os.ReadDir(cwd)
	if err != nil {
		t.Fatalf("read cwd: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("cwd holds %d files, want one export", len(entries))
	}
}

func TestExportTakesARelativePath(t *testing.T) {
	cwd := t.TempDir()
	application := commandApp(t, memoryAt(t, cwd), cwd, faux.Response{Text: "answer"})
	if _, err := application.Ask(context.Background(), wire.UserMessage("hi"), nil); err != nil {
		t.Fatalf("ask: %v", err)
	}
	if err := os.Mkdir(filepath.Join(cwd, "notes"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	printed, err := runCommand(t, application, "/export notes/session.md")
	if err != nil {
		t.Fatalf("/export: %v", err)
	}
	want := filepath.Join(cwd, "notes", "session.md")
	if !strings.Contains(printed, want) {
		t.Fatalf("output = %q, want %q", printed, want)
	}
}

// A target whose directory does not exist is an error, not a silently created
// tree: a mistyped path should look like a mistyped path. The rule does not
// depend on the format — session.SaveFile would create the tree for the JSONL
// one, which is exactly why the check is in the command.
func TestExportRejectsAMissingDirectory(t *testing.T) {
	cwd := t.TempDir()
	application := commandApp(t, memoryAt(t, cwd), cwd)
	for _, target := range []string{"missing/session.md", "missing/session.html", "missing/session.jsonl"} {
		if _, err := runCommand(t, application, "/export "+target); err == nil {
			t.Fatalf("/export %s wrote into a directory that does not exist", target)
		}
		if _, err := os.Stat(filepath.Join(cwd, "missing")); err == nil {
			t.Fatalf("/export %s created the directory", target)
		}
	}
}

// The extension picks the document: one rule, so the name of an exported file is
// never a lie about what is inside it.
func TestExportTakesItsFormatFromTheExtension(t *testing.T) {
	cwd := t.TempDir()
	application := commandApp(t, memoryAt(t, cwd), cwd, faux.Response{Text: "answer"})
	if _, err := application.Ask(context.Background(), wire.UserMessage("hi"), nil); err != nil {
		t.Fatalf("ask: %v", err)
	}

	cases := []struct {
		name string
		want string
	}{
		// The session named itself after its first message, and the heading
		// follows the name; the id fallback is covered separately.
		{"session.md", "# hi"},
		{"session.html", "<!DOCTYPE html>"},
		{"session.jsonl", `"type":"session"`},
	}
	for _, c := range cases {
		printed, err := runCommand(t, application, "/export "+c.name)
		if err != nil {
			t.Fatalf("/export %s: %v", c.name, err)
		}
		if !strings.Contains(printed, filepath.Join(cwd, c.name)) {
			t.Fatalf("output = %q, want %q", printed, c.name)
		}
		content, err := os.ReadFile(filepath.Join(cwd, c.name))
		if err != nil {
			t.Fatalf("read %s: %v", c.name, err)
		}
		if !strings.Contains(firstLine(string(content)), c.want) {
			t.Fatalf("%s starts with %q, want %q", c.name, firstLine(string(content)), c.want)
		}
	}
}

// A bare --md / --html / --jsonl asks for the default name in that format, which
// is the one request a path cannot express without repeating the session id.
func TestExportNamesTheDefaultFileAfterTheFormat(t *testing.T) {
	cwd := t.TempDir()
	manager := memoryAt(t, cwd)
	application := commandApp(t, manager, cwd, faux.Response{Text: "answer"})
	if _, err := application.Ask(context.Background(), wire.UserMessage("hi"), nil); err != nil {
		t.Fatalf("ask: %v", err)
	}

	prefix := "atlas-session-" + shortSessionID(manager.ID())
	cases := []struct {
		argument string
		want     string
	}{
		{"--md", prefix + ".md"},
		{"--html", prefix + ".html"},
		{"--jsonl", prefix + ".jsonl"},
	}
	for _, c := range cases {
		printed, err := runCommand(t, application, "/export "+c.argument)
		if err != nil {
			t.Fatalf("/export %s: %v", c.argument, err)
		}
		if !strings.Contains(printed, filepath.Join(cwd, c.want)) {
			t.Fatalf("/export %s wrote %q, want %q", c.argument, printed, c.want)
		}
	}
}

// A name with no format, or a format atlas cannot write, is refused before a
// file is created: an export that guesses would leave the user with a document
// they cannot open.
func TestExportRefusesAFormatItCannotWrite(t *testing.T) {
	cwd := t.TempDir()
	application := commandApp(t, memoryAt(t, cwd), cwd)
	for _, argument := range []string{"notes.txt", "notes", "--pdf"} {
		_, err := runCommand(t, application, "/export "+argument)
		if err == nil {
			t.Fatalf("/export %s was accepted", argument)
		}
		if !strings.Contains(err.Error(), ".md") || !strings.Contains(err.Error(), ".jsonl") {
			t.Fatalf("/export %s: error = %q, want it to name the formats", argument, err)
		}
	}
	if entries, err := os.ReadDir(cwd); err != nil || len(entries) != 0 {
		t.Fatalf("cwd holds %d entries after refused exports, want none", len(entries))
	}
}

// TestExportJSONLIsASessionFile is the promise of that format: it is not a
// printout of the conversation, it is a session — so it opens again, with the
// same entries, the same ids and the same conversation.
func TestExportJSONLIsASessionFile(t *testing.T) {
	cwd := t.TempDir()
	manager := memoryAt(t, cwd)
	application := commandApp(t, manager, cwd, faux.Response{Text: "answer"})
	if _, err := application.Ask(context.Background(), wire.UserMessage("hi"), nil); err != nil {
		t.Fatalf("ask: %v", err)
	}

	if _, err := runCommand(t, application, "/export --jsonl"); err != nil {
		t.Fatalf("/export --jsonl: %v", err)
	}
	path := filepath.Join(cwd, "atlas-session-"+shortSessionID(manager.ID())+".jsonl")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stat the export: %v", err)
	}

	reopened, err := session.Open(path, "", "")
	if err != nil {
		t.Fatalf("open the export: %v", err)
	}
	if reopened.ID() != manager.ID() {
		t.Fatalf("id = %q, want %q", reopened.ID(), manager.ID())
	}
	want, err := manager.BuildProjection()
	if err != nil {
		t.Fatalf("projection: %v", err)
	}
	got, err := reopened.BuildProjection()
	if err != nil {
		t.Fatalf("reopened projection: %v", err)
	}
	if len(got.Messages) != len(want.Messages) {
		t.Fatalf("reopened holds %d messages, want %d", len(got.Messages), len(want.Messages))
	}
	for i := range want.Messages {
		if got.Messages[i].Text() != want.Messages[i].Text() {
			t.Fatalf("message %d = %q, want %q", i, got.Messages[i].Text(), want.Messages[i].Text())
		}
	}
}

// The exported branch is one chain: every entry names the one before it, and the
// first names nothing. A file whose first line inherited a parent would describe
// a branch that is not in the file.
func TestExportJSONLWritesOneChain(t *testing.T) {
	cwd := t.TempDir()
	manager := memoryAt(t, cwd)
	application := commandApp(t, manager, cwd, faux.Response{Text: "answer"})
	if _, err := application.Ask(context.Background(), wire.UserMessage("hi"), nil); err != nil {
		t.Fatalf("ask: %v", err)
	}
	if _, err := runCommand(t, application, "/export --jsonl"); err != nil {
		t.Fatalf("/export --jsonl: %v", err)
	}

	path := filepath.Join(cwd, "atlas-session-"+shortSessionID(manager.ID())+".jsonl")
	file, err := session.LoadFile(path)
	if err != nil {
		t.Fatalf("load the export: %v", err)
	}
	if len(file.Entries) == 0 {
		t.Fatal("the export holds no entries")
	}
	if file.Entries[0].ParentID != nil {
		t.Fatalf("first entry has parent %q, want none", *file.Entries[0].ParentID)
	}
	for i := 1; i < len(file.Entries); i++ {
		parent := file.Entries[i].ParentID
		if parent == nil || *parent != file.Entries[i-1].ID {
			t.Fatalf("entry %d does not follow entry %d", i, i-1)
		}
	}
}

// /rename records the name the footer and the session file report, and prints
// the sanitized form so the user sees what was stored.
func TestRenameSetsTheSessionName(t *testing.T) {
	cwd := t.TempDir()
	application := commandApp(t, memoryAt(t, cwd), cwd)

	printed, err := runCommand(t, application, "/rename  plan\n\nreview  ")
	if err != nil {
		t.Fatalf("/rename: %v", err)
	}
	if !strings.Contains(printed, "name: plan review") {
		t.Fatalf("output = %q, want the stored name", printed)
	}
	if got := application.Name(); got != "plan review" {
		t.Fatalf("name = %q, want plan review", got)
	}

	// With no argument it reports the name instead of clearing it.
	printed, err = runCommand(t, application, "/rename")
	if err != nil {
		t.Fatalf("/rename with no argument: %v", err)
	}
	if !strings.Contains(printed, "name: plan review") {
		t.Fatalf("output = %q, want the current name", printed)
	}
}

func TestRenameReportsAnUnnamedSession(t *testing.T) {
	cwd := t.TempDir()
	application := commandApp(t, memoryAt(t, cwd), cwd)
	printed, err := runCommand(t, application, "/rename")
	if err != nil {
		t.Fatalf("/rename: %v", err)
	}
	if !strings.Contains(printed, "name: (unnamed)") {
		t.Fatalf("output = %q, want the unnamed marker", printed)
	}
}

// /fork continues in a new session and tells the user both files, because the
// point of a fork is that the old conversation is still there.
//
// "0" is the first point the command offers — the whole conversation — and the
// one this used to be when /fork took no argument at all.
func TestForkPrintsTheNewSession(t *testing.T) {
	cwd := t.TempDir()
	manager, err := session.Create(cwd, t.TempDir(), session.Options{})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	application := commandApp(t, manager, cwd, faux.Response{Text: "answer"})
	if _, err := application.Ask(context.Background(), wire.UserMessage("hi"), nil); err != nil {
		t.Fatalf("ask: %v", err)
	}
	oldID := manager.ID()

	printed, err := runCommand(t, application, "/fork 0")
	if err != nil {
		t.Fatalf("/fork 0: %v", err)
	}
	if manager.ID() == oldID {
		t.Fatal("/fork 0 kept the session id")
	}
	if !strings.Contains(printed, "fork: "+manager.ID()) {
		t.Fatalf("output = %q, want the new session id", printed)
	}
	if !strings.Contains(printed, "file: "+manager.File()) {
		t.Fatalf("output = %q, want the new session file", printed)
	}
	if got := application.MessageCount(); got != 2 {
		t.Fatalf("messages = %d, want the conversation to survive the fork", got)
	}
}

func TestForkInMemorySaysSo(t *testing.T) {
	cwd := t.TempDir()
	application := commandApp(t, memoryAt(t, cwd), cwd, faux.Response{Text: "answer"})
	if _, err := application.Ask(context.Background(), wire.UserMessage("hi"), nil); err != nil {
		t.Fatalf("ask: %v", err)
	}
	printed, err := runCommand(t, application, "/fork 0")
	if err != nil {
		t.Fatalf("/fork 0: %v", err)
	}
	if !strings.Contains(printed, "(in memory)") {
		t.Fatalf("output = %q, want the in-memory note", printed)
	}
}

// /init is a turn: it runs the model with the session tools, and then the
// command checks that the file it asked for is really there.
func TestInitWritesAgentsFile(t *testing.T) {
	cwd := t.TempDir()
	args, err := json.Marshal(map[string]string{
		"path":    "AGENTS.md",
		"content": "# Project\n\nRun `go test ./...`.\n",
	})
	if err != nil {
		t.Fatalf("encode tool arguments: %v", err)
	}
	prompts := assembly.NewPrompts("", false, true).Source()
	application := commandAppWith(t, memoryAt(t, cwd), cwd, prompts,
		faux.Response{ToolCalls: []wire.Content{wire.ToolCallContent("call_1", "write", args)}},
		faux.Response{Text: "wrote AGENTS.md"},
	)

	printed, err := runCommandWith(t, application, prompts, "/init")
	if err != nil {
		t.Fatalf("/init: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(cwd, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	if !strings.Contains(string(content), "Run `go test ./...`.") {
		t.Fatalf("AGENTS.md = %q, want what the tool wrote", content)
	}
	if !strings.Contains(printed, "agents: "+filepath.Join(cwd, "AGENTS.md")) {
		t.Fatalf("output = %q, want the file it verified", printed)
	}
	if !strings.Contains(printed, "read into the prompt") {
		t.Fatalf("output = %q, want the note that the file is read", printed)
	}
	// The file is the prompt's own material: writing it has to change the
	// instructions this session runs on, or the user has to restart to see the
	// effect of the command they just ran.
	if prompt := application.SystemPrompt(); !strings.Contains(prompt, "Run `go test ./...`.") {
		t.Fatalf("system prompt = %q, want the file /init just wrote", prompt)
	}
}

// The flag is the one case where /init writes a file the session will not read,
// and saying so is the difference between a setting and a mystery.
func TestInitWithNoContextFilesSaysTheFileIsNotRead(t *testing.T) {
	cwd := t.TempDir()
	args, err := json.Marshal(map[string]string{
		"path":    "AGENTS.md",
		"content": "# Project\n",
	})
	if err != nil {
		t.Fatalf("encode tool arguments: %v", err)
	}
	prompts := assembly.NewPrompts("", true, true).Source()
	application := commandAppWith(t, memoryAt(t, cwd), cwd, prompts,
		faux.Response{ToolCalls: []wire.Content{wire.ToolCallContent("call_1", "write", args)}},
		faux.Response{Text: "wrote AGENTS.md"},
	)

	printed, err := runCommandWith(t, application, prompts, "/init")
	if err != nil {
		t.Fatalf("/init: %v", err)
	}
	if !strings.Contains(printed, "not read: -no-context-files is set") {
		t.Fatalf("output = %q, want the note that the file is ignored", printed)
	}
	if prompt := application.SystemPrompt(); strings.Contains(prompt, "# Project") {
		t.Fatalf("system prompt = %q, want it untouched", prompt)
	}
}

// A model that says "done" without writing is the failure this command
// exists to catch.
func TestInitReportsAFileThatWasNotWritten(t *testing.T) {
	cwd := t.TempDir()
	application := commandApp(t, memoryAt(t, cwd), cwd, faux.Response{Text: "all set"})
	_, err := runCommand(t, application, "/init")
	if err == nil || !strings.Contains(err.Error(), "AGENTS.md was not written") {
		t.Fatalf("err = %v, want a missing-file error", err)
	}
}

// The command table feeds /help, so a command missing from it cannot be found
// at all.
func TestCommandTableListsTheSessionCommands(t *testing.T) {
	text := helpText()
	for _, name := range []string{"/init", "/fork", "/rename", "/export", "/resume"} {
		if !strings.Contains(text, name+" ") && !strings.Contains(text, name+"\n") {
			t.Fatalf("help text is missing %s:\n%s", name, text)
		}
	}
}

func TestEveryCommandIsInThePalette(t *testing.T) {
	if picks := suggestionsFor("/", nil, false); len(picks) != len(commands) {
		t.Fatalf("/ matched %d of %d commands", len(picks), len(commands))
	}
	for _, c := range commands {
		picks := suggestionsFor(c.name, nil, false)
		if len(picks) != 1 || picks[0].Name != c.name {
			t.Fatalf("%s matched %+v", c.name, picks)
		}
		if picks[0].Usage != c.usage || picks[0].About != c.summary {
			t.Fatalf("%s: the palette and the table disagree: %+v", c.name, picks[0])
		}
	}
}

func TestSuggestionsMatchOnlyTheFirstField(t *testing.T) {
	picks := suggestionsFor("/mo", nil, false)
	if len(picks) != 1 || picks[0].Name != "/model" {
		t.Fatalf("/mo matched %+v", picks)
	}
	if picks[0].About != "switch model, or pick one from the list" {
		t.Fatalf("summary = %q", picks[0].About)
	}

	// The description stays up while the argument is being typed: that is the
	// moment the reader is deciding what to put in it.
	if picks := suggestionsFor("/model deep", nil, false); len(picks) != 1 {
		t.Fatalf("an argument closed the list: %+v", picks)
	}
	// Leading indentation is not a command, and neither is a prompt that
	// happens to mention one.
	if picks := suggestionsFor("  hello", nil, false); len(picks) != 0 {
		t.Fatalf("a prompt opened the list: %+v", picks)
	}
	if picks := suggestionsFor("/nope", nil, false); len(picks) != 0 {
		t.Fatalf("an unknown command matched %+v", picks)
	}
}

func TestThePaletteShrinksInASideConversation(t *testing.T) {
	state := newSideState()
	state.enter(nil)

	picks := suggestionsFor("/", state, false)
	if len(picks) != len(sideCommands) {
		t.Fatalf("/ matched %d commands in a side conversation, want %d", len(picks), len(sideCommands))
	}
	for _, pick := range picks {
		if !sideCommands[pick.Name] {
			t.Fatalf("the palette offers %s in a side conversation", pick.Name)
		}
	}
}

// TestEveryAliasNamesAListedCommand: an alias is a second spelling, not a second
// command. It has to point at a name the table and the switch agree on — a typo
// there would be a command /help does not list — and it must not be listed
// itself, or /help would show two rows that do the same thing.
func TestEveryAliasNamesAListedCommand(t *testing.T) {
	listed := make(map[string]bool, len(commands))
	for _, c := range commands {
		listed[c.name] = true
	}
	for alias, canonical := range commandAliases {
		if !listed[canonical] {
			t.Fatalf("alias %s names %q, which the command table does not list", alias, canonical)
		}
		if listed[alias] {
			t.Fatalf("%s is both an alias and a listed command", alias)
		}
		if picks := suggestionsFor(alias, nil, false); len(picks) != 0 {
			t.Fatalf("the palette offers %s as a command of its own: %+v", alias, picks)
		}
	}
}
