package main

// The skills a session can see: the four directories they are read from, the
// block of prompt that lists them, and the /skill command that reads one out of
// turn.
//
// The block is metadata only. A skill's body is a file the model reads with the
// tool that is already there, which is the whole reason this feature has no
// executor: a skill cannot do anything a user could not type. /skill is that
// same read, done early because the model picked something else or nothing.

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"pi-go/internal/app"
	"pi-go/internal/config"
	"pi-go/internal/skill"
	"pi-go/internal/tool"
	"pi-go/internal/wire"
)

// skillRoots are the directories v1 reads, in the order that decides a name
// collision: the first root to claim a name keeps it, so the user's own skills
// come before anything a checkout brought.
//
// A plugin sits between the shared directory and the project: it is installed
// the way a user skill is, so it outranks a checkout, but a skill the user wrote
// themselves still wins the name. One root per plugin rather than one root over
// all of them, because the scope is what a collision note names.
//
// There is no walk up to the repository root, which is where this differs from
// the context files. A skill is "how to do this kind of task", which has nothing
// to do with how deep in the tree the work happens to be; reading the ancestors
// too would make the list change when the user cd's into a subdirectory, and
// "why can I see three skills here and five there" has no answer a reader can
// reconstruct.
func skillRoots(cwd string) []skill.Root {
	roots := make([]skill.Root, 0, 6)
	home, homeErr := config.DefaultHome()
	if homeErr == nil {
		roots = append(roots, skill.Root{Dir: filepath.Join(home, "skills"), Scope: skill.User})
	}
	if user, err := os.UserHomeDir(); err == nil {
		roots = append(roots, skill.Root{Dir: filepath.Join(user, ".agents", "skills"), Scope: skill.Shared})
	}
	if homeErr == nil {
		for _, plugin := range config.LoadPlugins(home).Plugins {
			if plugin.SkillsDir != "" {
				roots = append(roots, skill.Root{Dir: plugin.SkillsDir, Scope: skill.Plugin})
			}
		}
	}
	return append(roots,
		skill.Root{Dir: filepath.Join(cwd, ".atlas", "skills"), Scope: skill.Project},
		skill.Root{Dir: filepath.Join(cwd, ".agents", "skills"), Scope: skill.ProjectShared},
	)
}

// skillLimits are read's numbers, not this feature's. Both the warning and the
// refusal of /skill have to quote the number the read will actually stop at, and
// a copy of it here would drift the first time the tool changes.
func skillLimits() skill.Limits {
	return skill.Limits{MaxBytes: tool.DefaultMaxBytes, MaxLines: tool.DefaultMaxLines}
}

// availableSkills is the scan both halves of the feature run: the prompt block
// and the command. One scan means the list a reader sees is the list the model
// was given.
func availableSkills(cwd string) ([]skill.Skill, []string) {
	return skill.Load(skillRoots(cwd), skillLimits())
}

// skillDirectory renders the available-skills block for cwd, with one warning
// per file that was skipped or accepted with a caveat.
func skillDirectory(cwd string) (string, []string) {
	found, notes := availableSkills(cwd)
	return skill.Directory(found), notes
}

// skillCommand is /skill: bare, it lists what is installed; with a name it loads
// that file as the next request and runs the turn, because a name the user typed
// is the whole instruction.
//
// It writes through out and asks through render like every other command, so the
// answer lands in whichever conversation the prompt belongs to and the side view
// gets the markers it draws with.
func skillCommand(ctx context.Context, application *app.App, side *sideState, args []string, out io.Writer, render *renderer) error {
	cwd := application.Session().Cwd()
	skills, notes := availableSkills(cwd)
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
	body, err := found.Text(skillLimits())
	if err != nil {
		return fmt.Errorf("/skill %s: %w", found.Name, err)
	}
	fmt.Fprintf(out, "loaded skill %s (%s)\n", found.Name, found.Path)
	message := skillMessage(found, body, strings.Join(args[1:], " "))
	if side != nil {
		if conversation := side.active(); conversation != nil {
			render.setSide(true)
			return askSide(ctx, conversation, wire.UserMessage(message), render)
		}
	}
	return ask(ctx, application, message, render)
}

// skillMessage is the request that loads a skill: the file, inside the two facts
// the body needs to be usable. The name says which skill this is; the location
// says what a relative path in the body is relative to, which is the difference
// between "see references/api.md" working and resolving against the working
// directory.
//
// The file is passed through unchanged rather than trimmed of its frontmatter.
// It is the same text the model would have read, and a second view of one file
// is a second thing that can disagree with it.
func skillMessage(found skill.Skill, body, args string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<skill name=%q location=%q>\n", found.Name, found.Path)
	b.WriteString(strings.TrimRight(body, "\n"))
	b.WriteString("\n</skill>")
	if args != "" {
		b.WriteString("\n\n")
		b.WriteString(args)
	}
	return b.String()
}

// writeSkills is the table /skill prints. It is separate from the command so the
// rendering can be tested against skills built by hand, the way /mcp's table is.
func writeSkills(out io.Writer, skills []skill.Skill, cwd string) error {
	if len(skills) == 0 {
		// The roots, not just "none": the question behind a bare /skill on a
		// machine with none is "where would I put one", and four paths answer it
		// in less space than a sentence about discovery rules.
		fmt.Fprintln(out, "no skills are installed; a skill is a directory holding a SKILL.md, in:")
		for _, root := range skillRoots(cwd) {
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
