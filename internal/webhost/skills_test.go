package webhost

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pi-go/internal/assembly"
)

func TestHostSkillsListsReadsAndFormatsPrompt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ATLAS_HOME", home)
	t.Setenv("HOME", t.TempDir())

	host, cwd, _ := sessionHost(t)
	_ = cwd

	// Put a skill in user home and another in active project cwd.
	userSkill := filepath.Join(home, "skills", "alpha", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(userSkill), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(userSkill, []byte("---\nname: alpha\ndescription: Alpha helper\n---\nRun step one.\n"), 0o600); err != nil {
		t.Fatalf("write user skill: %v", err)
	}

	projSkill := filepath.Join(host.SessionDirectory(), ".atlas", "skills", "beta", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(projSkill), 0o755); err != nil {
		t.Fatalf("mkdir proj: %v", err)
	}
	if err := os.WriteFile(projSkill, []byte("---\nname: beta\ndescription: Beta helper\n---\nRun beta.\n"), 0o600); err != nil {
		t.Fatalf("write proj skill: %v", err)
	}

	state, err := host.Skills()
	if err != nil {
		t.Fatalf("host.Skills: %v", err)
	}
	if len(state.Skills) != 2 {
		t.Fatalf("skills = %+v, want 2 skills (alpha, beta)", state.Skills)
	}

	detail, err := host.SkillContent("alpha")
	if err != nil {
		t.Fatalf("host.SkillContent: %v", err)
	}
	if !strings.Contains(detail.Body, "Run step one.") || detail.Scope != "user" {
		t.Fatalf("detail = %+v", detail)
	}

	prompt, err := host.LoadSkillPrompt("beta", "now")
	if err != nil {
		t.Fatalf("host.LoadSkillPrompt: %v", err)
	}
	if !strings.Contains(prompt, `<skill name="beta"`) || !strings.Contains(prompt, "Run beta.") || !strings.HasSuffix(prompt, "now") {
		t.Fatalf("prompt = %q", prompt)
	}

	source := assembly.NewPrompts("", true, false).Source()
	initial, err := source.Read(host.SessionDirectory())
	if err != nil || !strings.Contains(initial.Text, "Alpha helper") {
		t.Fatalf("starting prompt = %+v, error = %v", initial, err)
	}
	disabled, err := host.SetSkillEnabled(context.Background(), "alpha", false)
	if err != nil || !disabled.CanToggle || len(disabled.Skills) != 2 || !disabled.Skills[0].Disabled || disabled.Skills[0].Name != "alpha" {
		t.Fatalf("disabled snapshot = %+v, error = %v", disabled, err)
	}
	updated, err := source.Read(host.SessionDirectory())
	if err != nil || updated.Skills != 1 || !strings.Contains(updated.Text, "Beta helper") {
		t.Fatalf("updated prompt = %+v, error = %v", updated, err)
	}
	for _, value := range []string{"<name>alpha</name>", "Alpha helper", userSkill} {
		if strings.Contains(updated.Text, value) {
			t.Fatalf("updated prompt includes disabled Skill metadata %q", value)
		}
	}
	if _, err := host.LoadSkillPrompt("alpha", ""); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("loading disabled Skill returned %v", err)
	}
	if _, err := host.SkillContent("alpha"); err != nil {
		t.Fatalf("preview of disabled Skill: %v", err)
	}
	if _, err := host.SetSkillEnabled(context.Background(), "alpha", true); err != nil {
		t.Fatal(err)
	}
	reenabled, err := source.Read(host.SessionDirectory())
	if err != nil || !strings.Contains(reenabled.Text, "Alpha helper") {
		t.Fatalf("reenabled prompt = %+v, error = %v", reenabled, err)
	}
}
