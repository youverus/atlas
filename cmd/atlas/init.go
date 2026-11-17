package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"pi-go/internal/app"
	"pi-go/internal/assembly"
)

// initPrompt asks the model to write AGENTS.md.
//
// It is a prompt and not a template: the file describes this repository, and
// the only way to know what is in it is to read it, which is what the tools are
// for. Shipping a skeleton would produce a file that is true of every project
// and therefore worth nothing.
const initPrompt = `Write AGENTS.md at the root of this repository. Coding agents read it at the start of every session, so it has to be short, concrete, and true.

Explore the repository first: build files, the test and lint commands, the documentation, and a few representative source files. Use the file tools and bash.

Cover what a new contributor would get wrong on the first try:
- what this project is and how the tree is organised;
- the exact commands to build, run, and test it;
- the conventions a change must follow: formatting, dependencies, error handling, test style;
- anything a contributor must not do.

Write it with the write tool. If AGENTS.md already exists, read it first and improve it instead of replacing it blindly. Do not state a command or a convention you have not seen in the repository, and do not pad it: a page read at the start of every session has to earn every line.`

// initProject runs one turn that writes AGENTS.md, then checks the file is
// there.
//
// The check is the command's promise. A model that answers "done" without
// writing is the failure worth catching, and one stat catches it; reporting the
// size also tells the user whether they got three lines or three pages.
//
// Then the prompt is read again. The instructions are read for every turn, so
// the next question already uses the file; the reload makes that true now
// rather than at the next turn, and it reports a file that cannot be read
// instead of leaving the failure to arrive with somebody's question.
func initProject(ctx context.Context, application *app.App, out io.Writer, render *renderer, prompts *assembly.PromptSource) error {
	if err := ask(ctx, application, initPrompt, render); err != nil {
		return err
	}
	path := filepath.Join(application.Session().Cwd(), "AGENTS.md")
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("AGENTS.md was not written: %w", err)
	}
	fmt.Fprintf(out, "agents: %s (%d bytes)%s\n", path, info.Size(), prompts.Note())
	if _, err := application.ReloadSystemPrompt(); err != nil {
		return err
	}
	return nil
}
