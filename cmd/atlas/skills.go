package main

// /skill is the command half of the skills feature: bare, it lists what is
// installed; with a name it reads that file out of turn.
//
// The discovery and the prompt block live in the assembly, because a window
// lists the same skills from the same four directories. What stays here is the
// command, which is a terminal gesture with a transcript to write the answer to.

import (
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"pi-go/internal/app"
	"pi-go/internal/assembly"
	"pi-go/internal/skill"
	"pi-go/internal/wire"
)

// skillCommand is /skill: bare, it lists what is installed; with a name it loads
// that file as the next request and runs the turn, because a name the user typed
// is the whole instruction.
//
// It writes through out and asks through render like every other command, so the
// answer lands in whichever conversation the prompt belongs to and the side view
// gets the markers it draws with.
func skillCommand(ctx context.Context, application *app.App, side *sideState, args []string, out io.Writer, render *renderer) error {
	cwd := application.Session().Cwd()
	skills, notes := assembly.AvailableSkills(cwd)
	if len(args) == 0 {
		return writeSkills(out, skills, cwd)
	}

	name := args[0]
	found, ok := skill.Skill{}, false
	for _, candidate := range skills {
		if candidate.Name == name {
			found, ok = candidate, true
			break
		}
	}
	if !ok {
		// The skipped files are printed here and nowhere else: a name that is
		// not in the list is the one moment when "this file was ignored
		// because" answers the question the reader just asked.
		for _, note := range notes {
			fmt.Fprintln(out, "warning:", note)
		}
		return fmt.Errorf("/skill: no skill named %q; /skill lists the %d installed", name, len(skills))
	}
	if application.Archived() {
		// The list half of this command is a read, and it ran above. Loading is
		// an append and a turn, and a record the user put away takes neither —
		// refused here rather than after a note that would report something
		// that did not happen.
		return app.ErrArchived
	}
	body, err := found.Text(assembly.SkillLimits())
	if err != nil {
		return fmt.Errorf("/skill %s: %w", found.Name, err)
	}
	fmt.Fprintf(out, "loaded skill %s (%s)\n", found.Name, found.Path)
	message := assembly.SkillMessage(found, body, strings.Join(args[1:], " "))
	if side != nil {
		if conversation := side.active(); conversation != nil {
			render.setSide(true)
			return askSide(ctx, conversation, wire.UserMessage(message), render)
		}
	}
	return ask(ctx, application, message, render)
}

// writeSkills is the table /skill prints. It is separate from the command so the
// rendering can be tested against skills built by hand, the way /mcp's table is.
func writeSkills(out io.Writer, skills []skill.Skill, cwd string) error {
	if len(skills) == 0 {
		// The roots, not just "none": the question behind a bare /skill on a
		// machine with none is "where would I put one", and four paths answer it
		// in less space than a sentence about discovery rules.
		fmt.Fprintln(out, "no skills are installed; a skill is a directory holding a SKILL.md, in:")
		for _, root := range assembly.SkillRoots(cwd) {
			fmt.Fprintf(out, "  %s\n", root.Dir)
		}
		return nil
	}
	writer := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "NAME\tSCOPE\tDESCRIPTION\tLOCATION")
	for _, s := range skills {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\n", s.Name, s.Scope, oneLine(s.Description, 80), s.Path)
	}
	if err := writer.Flush(); err != nil {
		return err
	}
	fmt.Fprintf(out, "\n/skill <name> loads one; the model reads them itself when a task matches\n")
	return nil
}
