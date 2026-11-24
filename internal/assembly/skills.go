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
	"slices"
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

// AvailableSkills scans the enabled skills for the model's prompt and commands.
func AvailableSkills(cwd string) ([]skill.Skill, []string) {
	settings, err := loadSkillSettings()
	if err != nil {
		return nil, []string{err.Error()}
	}
	found, notes := skill.Load(SkillRoots(cwd), SkillLimits())
	found = slices.DeleteFunc(found, func(item skill.Skill) bool { return settings.DisabledSkills[item.Name] })
	return found, notes
}

func loadSkillSettings() (config.CapabilitySettings, error) {
	home, err := config.DefaultHome()
	if err != nil {
		return config.CapabilitySettings{}, err
	}
	return config.LoadCapabilities(home)
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

// SkillRootInfo describes one scanned skill root directory.
type SkillRootInfo struct {
	Scope string
	Dir   string
}

// SkillInfo is a plain metadata snapshot of one discovered skill.
type SkillInfo struct {
	Name        string
	Description string
	Path        string
	Scope       string
	Disabled    bool
}

// SkillsSnapshot describes all skills visible for a working directory along
// with the scanned root paths and any scan/collision warnings.
type SkillsSnapshot struct {
	Cwd    string
	Roots  []SkillRootInfo
	Skills []SkillInfo
	Notes  []string
}

// DescribeSkills scans skills visible to cwd and returns plain snapshot values.
func DescribeSkills(cwd string) (SkillsSnapshot, error) {
	settings, err := loadSkillSettings()
	if err != nil {
		return SkillsSnapshot{}, err
	}
	rawRoots := SkillRoots(cwd)
	roots := make([]SkillRootInfo, 0, len(rawRoots))
	for _, root := range rawRoots {
		roots = append(roots, SkillRootInfo{
			Scope: string(root.Scope),
			Dir:   root.Dir,
		})
	}
	found, notes := skill.Load(rawRoots, SkillLimits())
	items := make([]SkillInfo, 0, len(found))
	for _, s := range found {
		item := skillInfoOf(s)
		item.Disabled = settings.DisabledSkills[s.Name]
		items = append(items, item)
	}
	return SkillsSnapshot{
		Cwd:    cwd,
		Roots:  roots,
		Skills: items,
		Notes:  notes,
	}, nil
}

// SetSkillEnabled persists the user's setting for an installed skill.
func SetSkillEnabled(cwd, name string, enabled bool) error {
	found, err := findSkill(cwd, name)
	if err != nil {
		return err
	}
	home, err := config.DefaultHome()
	if err != nil {
		return err
	}
	return config.SaveSkillEnabled(home, found.Name, enabled)
}

// ReadSkillContent reads the SKILL.md body for the named skill visible in cwd.
func ReadSkillContent(cwd, name string) (SkillInfo, string, error) {
	found, err := findSkill(cwd, name)
	if err != nil {
		return SkillInfo{}, "", err
	}
	body, err := found.Text(SkillLimits())
	if err != nil {
		return SkillInfo{}, "", err
	}
	return skillInfoOf(found), body, nil
}

// BuildSkillPrompt constructs the formatted <skill ...> user prompt for name.
func BuildSkillPrompt(cwd, name, args string) (string, error) {
	settings, err := loadSkillSettings()
	if err != nil {
		return "", err
	}
	if target := strings.TrimSpace(name); settings.DisabledSkills[target] {
		return "", fmt.Errorf("skill: %q is disabled", target)
	}
	found, err := findSkill(cwd, name)
	if err != nil {
		return "", err
	}
	body, err := found.Text(SkillLimits())
	if err != nil {
		return "", err
	}
	return SkillMessage(found, body, strings.TrimSpace(args)), nil
}

func findSkill(cwd, name string) (skill.Skill, error) {
	target := strings.TrimSpace(name)
	if target == "" {
		return skill.Skill{}, fmt.Errorf("skill: empty skill name")
	}
	found, _ := skill.Load(SkillRoots(cwd), SkillLimits())
	for _, candidate := range found {
		if candidate.Name == target {
			return candidate, nil
		}
	}
	return skill.Skill{}, fmt.Errorf("skill: no skill named %q", target)
}

func skillInfoOf(s skill.Skill) SkillInfo {
	return SkillInfo{
		Name:        s.Name,
		Description: s.Description,
		Path:        s.Path,
		Scope:       string(s.Scope),
	}
}
