package assembly

// The skills a session can see: the four directories they are read from, the
// block of prompt that lists them, and the /skill command that reads one out of
// turn.
//
// The block is metadata only. A skill's body is a file the model reads with the
// tool that is already there, which is the whole reason this feature has no
// executor: a skill cannot do anything a user could not type. /skill is that
// same read, done early because the model picked something else or nothing.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"pi-go/internal/config"
	"pi-go/internal/skill"
	"pi-go/internal/tool"
)

// SkillRoots are the directories v1 reads, in the order that decides a name
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
func SkillRoots(cwd string) []skill.Root {
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

// SkillLimits are read's numbers, not this feature's. Both the warning and the
// refusal of /skill have to quote the number the read will actually stop at, and
// a copy of it here would drift the first time the tool changes.
func SkillLimits() skill.Limits {
	return skill.Limits{MaxBytes: tool.DefaultMaxBytes, MaxLines: tool.DefaultMaxLines}
}

// AvailableSkills is the scan both halves of the feature run: the prompt block
// and the command. One scan means the list a reader sees is the list the model
// was given.
func AvailableSkills(cwd string) ([]skill.Skill, []string) {
	return skill.Load(SkillRoots(cwd), SkillLimits())
}

// SkillMessage is the request that loads a skill: the file, inside the two facts
// the body needs to be usable. The name says which skill this is; the location
// says what a relative path in the body is relative to, which is the difference
// between "see references/api.md" working and resolving against the working
// directory.
//
// The file is passed through unchanged rather than trimmed of its frontmatter.
// It is the same text the model would have read, and a second view of one file
// is a second thing that can disagree with it.
func SkillMessage(found skill.Skill, body, args string) string {
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
