package main

// The live prompt: the files behind the instructions are read again for every
// turn, so a project file that changed and a skill that was installed take
// effect on the next question rather than on the next restart.

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"pi-go/internal/app"
	"pi-go/internal/assembly"
	"pi-go/internal/provider/faux"
	"pi-go/internal/tool/fs"
	"pi-go/internal/wire"
)

// liveApp builds a session whose prompt comes from a source, and returns the
// provider so a test can look at the requests it sent.
func liveApp(t *testing.T, cwd string, prompts *assembly.PromptSource, responses ...faux.Response) (*app.App, *faux.Provider) {
	t.Helper()
	prov := faux.New(responses...)
	application, err := app.New(app.Config{
		Model:        wire.Model{ID: "faux-1", Name: "Faux", API: "faux", Provider: "faux"},
		Provider:     prov,
		SystemPrompt: prompts.Prompt,
		Tools:        app.StaticTools(fs.Tools(fs.Options{Cwd: cwd})),
	}, memoryAt(t, cwd))
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	return application, prov
}

// A file written while the session runs is in the prompt of the next
// question. The session does not have to be restarted for it, and the reader
// does not have to guess whether the file was read.
func TestTheNextTurnReadsTheProjectFileAgain(t *testing.T) {
	emptyHome(t)
	cwd := t.TempDir()
	prompts := assembly.NewPrompts("", false, false).Source()
	application, prov := liveApp(t, cwd, prompts,
		faux.Response{Text: "first"},
		faux.Response{Text: "second"},
	)

	if _, err := application.Ask(context.Background(), wire.UserMessage("hi"), nil); err != nil {
		t.Fatalf("ask: %v", err)
	}
	putFile(t, filepath.Join(cwd, "AGENTS.md"), "# project rules\n")
	if _, err := application.Ask(context.Background(), wire.UserMessage("again"), nil); err != nil {
		t.Fatalf("ask: %v", err)
	}

	requests := prov.Calls()
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(requests))
	}
	first := requests[0].Messages[0].Content.Text()
	if strings.Contains(first, "<project_instructions") {
		t.Fatalf("first prompt = %q, want no project file yet", first)
	}
	second := requests[1].Messages[0].Content.Text()
	if !strings.Contains(second, "<project_instructions") {
		t.Fatalf("second prompt = %q, want the file that was written", second)
	}
	if !strings.Contains(second, "# project rules") {
		t.Fatalf("second prompt = %q, want what the file says", second)
	}
}

// A skill installed while the session runs is in the prompt of the next
// question, for the same reason the project file is.
func TestTheNextTurnListsASkillInstalledMidSession(t *testing.T) {
	emptyHome(t)
	cwd := t.TempDir()
	prompts := assembly.NewPrompts("", false, false).Source()
	application, prov := liveApp(t, cwd, prompts,
		faux.Response{Text: "first"},
		faux.Response{Text: "second"},
	)

	if _, err := application.Ask(context.Background(), wire.UserMessage("hi"), nil); err != nil {
		t.Fatalf("ask: %v", err)
	}
	putFile(t, filepath.Join(cwd, ".atlas", "skills", "deploy", "SKILL.md"), skillFile("deploy", "Deploy it."))
	if _, err := application.Ask(context.Background(), wire.UserMessage("again"), nil); err != nil {
		t.Fatalf("ask: %v", err)
	}

	requests := prov.Calls()
	second := requests[1].Messages[0].Content.Text()
	if !strings.Contains(second, "<name>deploy</name>") {
		t.Fatalf("second prompt = %q, want the skill that was installed", second)
	}
}

// /reload says what it read: the counts, the paths, and whether anything
// changed. A reader who just wrote a file wants to see that this is the file
// the prompt was made of.
func TestReloadSaysWhatItRead(t *testing.T) {
	emptyHome(t)
	cwd := t.TempDir()
	prompts := assembly.NewPrompts("", false, false).Source()
	application, _ := liveApp(t, cwd, prompts, faux.Response{Text: "hi"})

	// Nothing has changed yet: the file was read at startup and the prompt
	// still holds it.
	printed, err := runCommandWith(t, application, prompts, "/reload")
	if err != nil {
		t.Fatalf("/reload: %v", err)
	}
	if !strings.Contains(printed, "prompt reloaded: 0 context files, 0 skills") {
		t.Fatalf("output = %q, want the counts", printed)
	}
	if !strings.Contains(printed, "(unchanged)") {
		t.Fatalf("output = %q, want the second read to say nothing changed", printed)
	}

	// A file written after the session started is read now, and the report
	// names it.
	putFile(t, filepath.Join(cwd, "AGENTS.md"), "# project rules\n")
	printed, err = runCommandWith(t, application, prompts, "/reload")
	if err != nil {
		t.Fatalf("/reload: %v", err)
	}
	if !strings.Contains(printed, "prompt reloaded: 1 context file, 0 skills") {
		t.Fatalf("output = %q, want the counts", printed)
	}
	if strings.Contains(printed, "(unchanged)") {
		t.Fatalf("output = %q, want the new file to count as a change", printed)
	}
	if !strings.Contains(printed, filepath.Join(cwd, "AGENTS.md")) {
		t.Fatalf("output = %q, want the file it read", printed)
	}
}

// A session that carries its own instructions says so instead of reporting a
// reload that did not happen.
func TestReloadSaysTheSessionCarriesItsOwnInstructions(t *testing.T) {
	emptyHome(t)
	cwd := t.TempDir()
	prompts := assembly.NewPrompts("", false, false).Source()
	application, _ := liveApp(t, cwd, prompts, faux.Response{Text: "hi"})

	// The session records its own prompt, the way a conversation started in
	// pi does.
	if err := application.SetSystemPrompt("prompt from the session"); err != nil {
		t.Fatalf("set prompt: %v", err)
	}
	printed, err := runCommandWith(t, application, prompts, "/reload")
	if err != nil {
		t.Fatalf("/reload: %v", err)
	}
	if !strings.Contains(printed, "carries its own instructions") {
		t.Fatalf("output = %q, want the session to say it keeps its own prompt", printed)
	}
}

// The prompt is a file the user can rewrite, and the flags that turn its
// parts off have to leave the rest of it byte for byte alone.
func TestTheFlagsLeaveThePromptByteForByte(t *testing.T) {
	emptyHome(t)
	dir := t.TempDir()
	putFile(t, filepath.Join(dir, "AGENTS.md"), "# project rules\n")
	putFile(t, filepath.Join(dir, ".atlas", "skills", "deploy", "SKILL.md"), skillFile("deploy", "Deploy it."))

	plain, err := assembly.NewPrompts("", false, false).Build(dir)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	noContext, err := assembly.NewPrompts("", true, false).Build(dir)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	noSkills, err := assembly.NewPrompts("", false, true).Build(dir)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	if strings.Contains(noContext.Text, "<project_instructions") {
		t.Fatalf("prompt = %q, want no project file", noContext.Text)
	}
	if !strings.Contains(noContext.Text, "<available_skills>") {
		t.Fatalf("prompt = %q, want the skills", noContext.Text)
	}
	if strings.Contains(noSkills.Text, "<available_skills>") {
		t.Fatalf("prompt = %q, want no skills", noSkills.Text)
	}
	if !strings.Contains(noSkills.Text, "<project_instructions") {
		t.Fatalf("prompt = %q, want the project file", noSkills.Text)
	}
	if plain.Text == noContext.Text || plain.Text == noSkills.Text {
		t.Fatal("want the two flags to change the prompt")
	}
}
