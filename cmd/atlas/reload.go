package main

// The /reload command: read the instructions again and say what they hold.
//
// The prompt is read for every turn, so the files behind it are picked up
// without this command. What the command adds is the answer to the question
// a reader actually has after editing a file: was it read, and what is in the
// prompt now.

import (
	"fmt"
	"io"

	"pi-go/internal/app"
	"pi-go/internal/assembly"
)

// reloadPrompt reads the instructions again and reports what it found.
//
// The counts and the paths are the proof: a reader who just wrote AGENTS.md
// wants to see that this is the file the prompt was made of, and a count
// alone would not say which one it was.
//
// A session that carries its own instructions says so instead of reporting a
// reload that did not happen. That session's prompt is the one recorded in
// its file — the same rule the tools follow — and there is nothing here to
// read again.
func reloadPrompt(application *app.App, prompts *assembly.PromptSource, out io.Writer) error {
	result, err := application.ReloadSystemPrompt()
	if err != nil {
		return err
	}
	if !result.Live {
		fmt.Fprintln(out, "prompt: this session carries its own instructions; there is nothing to reload")
		return nil
	}

	// The app asked this source a moment ago — it is the source the session
	// was started with — so last() is what the prompt in effect was made of.
	build := prompts.Last()
	unchanged := ""
	if !result.Changed {
		unchanged = " (unchanged)"
	}
	fmt.Fprintf(out, "prompt reloaded: %s, %s, %d bytes%s\n",
		countNoun(len(build.ContextFiles), "context file", "context files"),
		countNoun(build.Skills, "skill", "skills"),
		len(build.Text), unchanged)
	for _, path := range build.ContextFiles {
		fmt.Fprintf(out, "  %s\n", path)
	}
	for _, note := range build.Notes {
		fmt.Fprintln(out, "warning:", note)
	}
	return nil
}

// countNoun names how many of something there are, in the singular when
// there is one. A report that says "1 context files" is the kind of detail that
// makes a reader distrust the rest of the line.
func countNoun(n int, singular, plural string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, singular)
	}
	return fmt.Sprintf("%d %s", n, plural)
}
