package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pi-go/internal/assembly"
	"pi-go/internal/config"
	"pi-go/internal/skill"
)

// skillFile is a SKILL.md with the two keys the prompt lists.
func skillFile(name, description string) string {
	return "---\nname: " + name + "\ndescription: " + description + "\n---\n\nDo the thing.\n"
}

// The skills go last: the base is how to be an agent, the context files are what
// this repository is, and the skill list is what this task could be. A prompt is
// read in order, so the most specific block is the one nearest the turn.
func TestTheSkillDirectoryFollowsTheContext(t *testing.T) {
	home := emptyHome(t)
	dir := t.TempDir()
	putFile(t, filepath.Join(home, "skills", "release", "SKILL.md"), skillFile("release", "Cut a release."))
	putFile(t, filepath.Join(dir, ".atlas", "skills", "deploy", "SKILL.md"), skillFile("deploy", "Deploy it."))
	putFile(t, filepath.Join(dir, "AGENTS.md"), "# project rules\n")

	build, err := assembly.NewPrompts("", false, false).Build(dir)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(build.Notes) != 0 {
		t.Fatalf("notes = %v, want none: both files are valid", build.Notes)
	}
	prompt := build.Text

	contextAt := strings.Index(prompt, "<project_instructions")
	skillsAt := strings.Index(prompt, "<available_skills>")
	if contextAt < 0 || skillsAt < 0 {
		t.Fatalf("prompt = %q, want the context files and the skills", prompt)
	}
	if contextAt > skillsAt {
		t.Fatalf("prompt = %q, want the skill list after the context files", prompt)
	}
	// Two roots, one prompt block: the user's skill and the project's are listed
	// together, because a model choosing between them does not care where they
	// live.
	for _, want := range []string{
		"<name>release</name>",
		"<location>" + filepath.Join(home, "skills", "release", "SKILL.md") + "</location>",
		"<name>deploy</name>",
		"<location>" + filepath.Join(dir, ".atlas", "skills", "deploy", "SKILL.md") + "</location>",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt = %q, want it to contain %q", prompt, want)
		}
	}
	// The body is not in the prompt: that is the whole point of listing metadata.
	if strings.Contains(prompt, "Do the thing.") {
		t.Fatalf("prompt = %q, want the bodies left on disk", prompt)
	}
}

// The shared root is ~/.agents/skills, the directory pi reads. It is here so a
// skill does not have to be copied once per agent on the same machine.
func TestTheSharedRootIsRead(t *testing.T) {
	emptyHome(t)
	user, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	putFile(t, filepath.Join(user, ".agents", "skills", "release", "SKILL.md"), skillFile("release", "Cut a release."))

	build, err := assembly.NewPrompts("", false, false).Build(t.TempDir())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(build.Notes) != 0 {
		t.Fatalf("notes = %v, want none", build.Notes)
	}
	prompt := build.Text
	if !strings.Contains(prompt, "<name>release</name>") {
		t.Fatalf("prompt = %q, want the shared skill", prompt)
	}
}

// The flag is the way back to exactly the built-in prompt, which is what makes
// "the skill list did not change the default prompt" a test rather than a claim.
func TestNoSkillsLeavesThePromptAlone(t *testing.T) {
	home := emptyHome(t)
	putFile(t, filepath.Join(home, "skills", "release", "SKILL.md"), skillFile("release", "Cut a release."))

	build, err := assembly.NewPrompts("", false, true).Build(t.TempDir())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if build.Text != builtinText(t) {
		t.Fatalf("prompt = %q, want the built-in prompt unchanged", build.Text)
	}
	if len(build.Notes) != 0 {
		t.Fatalf("notes = %v, want none: a flag that turns the scan off has nothing to report", build.Notes)
	}
}

// A skill nobody can load is a warning, not a failure: the session starts, the
// list is short one entry, and the line says which file to fix. That is the same
// trade an unreachable MCP server gets.
func TestABadSkillIsReportedAndTheSessionGoesOn(t *testing.T) {
	home := emptyHome(t)
	path := filepath.Join(home, "skills", "broken", "SKILL.md")
	putFile(t, path, "no frontmatter here\n")

	build, err := assembly.NewPrompts("", false, false).Build(t.TempDir())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if build.Text != builtinText(t) {
		t.Fatalf("prompt = %q, want the built-in prompt", build.Text)
	}
	if len(build.Notes) != 1 || !strings.HasPrefix(build.Notes[0], "skill: "+path+": ") {
		t.Fatalf("notes = %v, want one line naming %s", build.Notes, path)
	}
}

// -no-context-files and -no-skills are separate switches: turning off one must
// not turn off the other, or the two escapes collapse into one.
func TestTheTwoSwitchesAreIndependent(t *testing.T) {
	home := emptyHome(t)
	dir := t.TempDir()
	putFile(t, filepath.Join(home, "skills", "release", "SKILL.md"), skillFile("release", "Cut a release."))
	putFile(t, filepath.Join(dir, "AGENTS.md"), "# project rules\n")

	skillsBuild, err := assembly.NewPrompts("", true, false).Build(dir)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	withSkills := skillsBuild.Text
	if strings.Contains(withSkills, "<project_instructions") || !strings.Contains(withSkills, "<available_skills>") {
		t.Fatalf("prompt = %q, want the skills and no context files", withSkills)
	}

	contextBuild, err := assembly.NewPrompts("", false, true).Build(dir)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	withContext := contextBuild.Text
	if strings.Contains(withContext, "<available_skills>") || !strings.Contains(withContext, "<project_instructions") {
		t.Fatalf("prompt = %q, want the context files and no skills", withContext)
	}
}

// The four roots are wiring, and the wiring is the half a unit test of the
// scanner cannot see: which directory is which scope, and in what order they are
// read is decided here.
func TestTheFourRootsAreReadInOrder(t *testing.T) {
	home := emptyHome(t)
	user, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	cwd := t.TempDir()

	want := []struct {
		dir   string
		scope skill.Scope
	}{
		{filepath.Join(home, "skills"), skill.User},
		{filepath.Join(user, ".agents", "skills"), skill.Shared},
		{filepath.Join(cwd, ".atlas", "skills"), skill.Project},
		{filepath.Join(cwd, ".agents", "skills"), skill.ProjectShared},
	}
	roots := assembly.SkillRoots(cwd)
	if len(roots) != len(want) {
		t.Fatalf("roots = %v, want %d", roots, len(want))
	}
	for i, w := range want {
		if roots[i].Dir != w.dir || roots[i].Scope != w.scope {
			t.Fatalf("root %d = %+v, want %+v", i, roots[i], w)
		}
	}
}

// A plugin's skills join the same list. The root sits between the shared
// directory and the project: a plugin is installed the way a user skill is, so
// it outranks a checkout, but a skill the user wrote themselves still wins the
// name — which is the same first-root-wins rule the other four follow.
func TestAPluginSkillRootSitsBeforeTheProject(t *testing.T) {
	home := emptyHome(t)
	cwd := t.TempDir()
	pluginSkill := filepath.Join(home, config.PluginDirName, "review", "skills", "deploy", "SKILL.md")
	putFile(t, filepath.Join(home, config.PluginDirName, "review", "plugin.json"), `{"skills": "skills"}`)
	putFile(t, pluginSkill, skillFile("deploy", "Deploy from the plugin."))
	putFile(t, filepath.Join(cwd, ".atlas", "skills", "deploy", "SKILL.md"), skillFile("deploy", "Deploy from the checkout."))

	roots := assembly.SkillRoots(cwd)
	want := []skill.Scope{skill.User, skill.Shared, skill.Plugin, skill.Project, skill.ProjectShared}
	if len(roots) != len(want) {
		t.Fatalf("roots = %v, want %d", roots, len(want))
	}
	for i, scope := range want {
		if roots[i].Scope != scope {
			t.Fatalf("root %d = %+v, want scope %q", i, roots[i], scope)
		}
	}

	found, notes := assembly.AvailableSkills(cwd)
	if len(found) != 1 {
		t.Fatalf("skills = %v, notes = %v, want one skill and the shadowed note", found, notes)
	}
	if found[0].Path != pluginSkill || found[0].Scope != skill.Plugin {
		t.Fatalf("skill = %+v, want the plugin's", found[0])
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "comes earlier in the search order") {
		t.Fatalf("notes = %v, want the note about the shadowed project skill", notes)
	}
}

// A plugin that is not installed adds no root, so the four-root answer stays the
// answer on the machine that has none.
func TestAPluginAddsNothingUnlessItHasSkills(t *testing.T) {
	home := emptyHome(t)
	cwd := t.TempDir()
	putFile(t, filepath.Join(home, config.PluginDirName, "servers-only", "plugin.json"),
		`{"mcpServers": {"lint": {"command": "true"}}}`)

	for _, root := range assembly.SkillRoots(cwd) {
		if root.Scope == skill.Plugin {
			t.Fatalf("root = %+v, want no plugin root for a plugin without skills", root)
		}
	}
}

// builtinText is the prompt with nothing added to it, which is what a build of
// an empty directory has to equal byte for byte.
func builtinText(t *testing.T) string {
	t.Helper()
	text, err := assembly.BasePrompt("")
	if err != nil {
		t.Fatalf("base prompt: %v", err)
	}
	return text
}
