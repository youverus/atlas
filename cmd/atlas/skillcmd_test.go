package main

// The command tests below use the same scripted provider as the rest of this
// package. What they pin is the contract between the list the prompt carries and
// the read /skill performs: one scan, one name, one file.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pi-go/internal/app"
	"pi-go/internal/provider/faux"
	"pi-go/internal/tool"
	"pi-go/internal/wire"
)

// A bare /skill answers "what is installed" with the same skills the prompt was
// built from, and with the scope each one came from: two skills with one name are
// one entry, so the scope is the only thing that says which file won.
func TestTheSkillCommandListsWhatIsInstalled(t *testing.T) {
	home := emptyHome(t)
	cwd := t.TempDir()
	release := filepath.Join(home, "skills", "release", "SKILL.md")
	deploy := filepath.Join(cwd, ".atlas", "skills", "deploy", "SKILL.md")
	putFile(t, release, skillFile("release", "Cut a release."))
	putFile(t, deploy, skillFile("deploy", "Deploy it."))

	application := commandApp(t, memoryAt(t, cwd), cwd)
	out, err := runCommand(t, application, "/skill")
	if err != nil {
		t.Fatalf("/skill: %v", err)
	}
	for _, want := range []string{"NAME", "SCOPE", "DESCRIPTION", "LOCATION", "release", "user", "deploy", "project", release, deploy} {
		if !strings.Contains(out, want) {
			t.Fatalf("/skill = %q, want it to contain %q", out, want)
		}
	}
	if !strings.Contains(out, "/skill <name> loads one") {
		t.Fatalf("/skill = %q, want the footer naming the loaded form", out)
	}
	// Listing is a read: no turn runs, so nothing is appended.
	if got := len(application.Messages()); got != 0 {
		t.Fatalf("a bare /skill wrote %d messages", got)
	}
}

// Loading a skill is the model's own read, done early: the message carries the
// file, the name it was listed under and the directory its relative paths resolve
// against. Everything the user typed after the name is the rest of the request.
func TestTheSkillCommandLoadsTheBody(t *testing.T) {
	home := emptyHome(t)
	cwd := t.TempDir()
	path := filepath.Join(home, "skills", "release", "SKILL.md")
	putFile(t, path, skillFile("release", "Cut a release."))

	application := commandApp(t, memoryAt(t, cwd), cwd, faux.Response{Text: "on it"})
	out, err := runCommand(t, application, "/skill release tag v2")
	if err != nil {
		t.Fatalf("/skill release: %v", err)
	}
	if !strings.Contains(out, "loaded skill release ("+path+")") {
		t.Fatalf("/skill = %q, want the load reported with its file", out)
	}

	messages := application.Messages()
	if len(messages) != 2 {
		t.Fatalf("transcript = %d messages, want the request and the answer", len(messages))
	}
	prompt := messages[0]
	if prompt.Role != wire.RoleUser {
		t.Fatalf("first message role = %q, want user", prompt.Role)
	}
	text := prompt.Text()
	if !strings.HasPrefix(text, fmt.Sprintf("<skill name=%q location=%q>\n", "release", path)) {
		t.Fatalf("request = %q, want the skill wrapped with its name and file", text)
	}
	if !strings.Contains(text, "Do the thing.") {
		t.Fatalf("request = %q, want the body", text)
	}
	if !strings.HasSuffix(text, "</skill>\n\ntag v2") {
		t.Fatalf("request = %q, want the text after the name as the rest of the request", text)
	}
	if got := messages[1].Text(); got != "on it" {
		t.Fatalf("answer = %q, want the turn to have run", got)
	}
}

// A name that is not there is the one moment when "this file was skipped because"
// answers what was just asked, so the warnings come out with the refusal.
func TestTheSkillCommandSaysWhatIsMissing(t *testing.T) {
	home := emptyHome(t)
	cwd := t.TempDir()
	putFile(t, filepath.Join(home, "skills", "release", "SKILL.md"), skillFile("release", "Cut a release."))
	broken := filepath.Join(cwd, ".atlas", "skills", "broken", "SKILL.md")
	putFile(t, broken, "no frontmatter here\n")

	application := commandApp(t, memoryAt(t, cwd), cwd)
	out, err := runCommand(t, application, "/skill deploi")
	if err == nil {
		t.Fatal("/skill deploi loaded something")
	}
	for _, want := range []string{`no skill named "deploi"`, "the 1 installed"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %q, want it to contain %q", err, want)
		}
	}
	if !strings.HasPrefix(out, "warning: skill: "+broken+": ") {
		t.Fatalf("output = %q, want the skipped file named", out)
	}
	if got := len(application.Messages()); got != 0 {
		t.Fatalf("a missing name wrote %d messages", got)
	}
}

// /skill:name is the spelling people bring from other agents. Answering it with
// "unknown command" would read as "atlas has no skills", so it names the fix.
func TestTheColonSpellingIsRedirected(t *testing.T) {
	cwd := t.TempDir()
	application := commandApp(t, memoryAt(t, cwd), cwd)

	_, err := runCommand(t, application, "/skill:release")
	if err == nil {
		t.Fatal("/skill:release was accepted")
	}
	if want := `/skill takes a space, not a colon: did you mean "/skill release"?`; err.Error() != want {
		t.Fatalf("err = %q, want %q", err, want)
	}
}

// In a side conversation the request lands where the prompt is: the loaded skill
// is a question about the snapshot, not an append to the conversation it came
// from.
func TestTheSkillCommandLoadsIntoASideConversation(t *testing.T) {
	home := emptyHome(t)
	cwd := t.TempDir()
	putFile(t, filepath.Join(home, "skills", "release", "SKILL.md"), skillFile("release", "Cut a release."))

	state := newSideState()
	application := sideApp(t, state, cwd, faux.Response{Text: "side answer"})
	side, err := application.BeginSide()
	if err != nil {
		t.Fatalf("BeginSide: %v", err)
	}
	state.enter(side)

	out, err := runSideCommand(t, application, state, "/skill release")
	if err != nil {
		t.Fatalf("/skill release in a side conversation: %v", err)
	}
	if !strings.Contains(out, "loaded skill release") {
		t.Fatalf("output = %q, want the load reported", out)
	}
	messages := side.Messages()
	if len(messages) != 2 {
		t.Fatalf("side transcript = %d messages, want the request and the answer", len(messages))
	}
	if !strings.Contains(messages[0].Text(), "Do the thing.") {
		t.Fatalf("side request = %q, want the body", messages[0].Text())
	}
	if got := len(application.Messages()); got != 0 {
		t.Fatalf("the side load wrote %d messages into the main conversation", got)
	}
}

// An archived conversation is read-only, and loading is the half of /skill that
// appends. The list still runs, because looking something up in a record you put
// away is exactly what a record is for.
func TestAnArchivedSessionListsButDoesNotLoad(t *testing.T) {
	home := emptyHome(t)
	putFile(t, filepath.Join(home, "skills", "release", "SKILL.md"), skillFile("release", "Cut a release."))

	application, _, id, _ := archiveSetup(t)
	if _, err := runCommand(t, application, "/recover "+id); err != nil {
		t.Fatalf("/recover: %v", err)
	}
	if !application.Archived() {
		t.Fatal("the test is not in the archived conversation")
	}

	out, err := runCommand(t, application, "/skill")
	if err != nil {
		t.Fatalf("/skill in an archived conversation: %v", err)
	}
	if !strings.Contains(out, "release") {
		t.Fatalf("/skill = %q, want the list", out)
	}

	before := len(application.Messages())
	out, err = runCommand(t, application, "/skill release")
	if !errors.Is(err, app.ErrArchived) {
		t.Fatalf("err = %v, want the archived refusal", err)
	}
	if strings.Contains(out, "loaded skill") {
		t.Fatalf("output = %q, want no note about a load that did not happen", out)
	}
	if got := len(application.Messages()); got != before {
		t.Fatalf("the refused load wrote %d messages into an archived session", got-before)
	}
}

// The read tool stops at a fixed size, so a skill longer than that is refused
// rather than cut: a model that believes it has the whole skill follows half of
// it, and nothing in the conversation says so.
func TestASkillLongerThanOneReadIsNotLoaded(t *testing.T) {
	home := emptyHome(t)
	cwd := t.TempDir()
	path := filepath.Join(home, "skills", "big", "SKILL.md")
	putFile(t, path, skillFile("big", "Lots to say.")+strings.Repeat("padding line\n", 5000))

	application := commandApp(t, memoryAt(t, cwd), cwd)
	out, err := runCommand(t, application, "/skill big")
	if err == nil {
		t.Fatal("a skill longer than one read was loaded")
	}
	want := fmt.Sprintf("reading stops at %d", tool.DefaultMaxBytes)
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %q, want the size the read stops at", err)
	}
	if !strings.Contains(err.Error(), "move the long part into a file beside it") {
		t.Fatalf("err = %q, want the fix", err)
	}
	if strings.Contains(out, "loaded skill") {
		t.Fatalf("output = %q, want no note about a load that did not happen", out)
	}
	if got := len(application.Messages()); got != 0 {
		t.Fatalf("the refused load wrote %d messages", got)
	}
}

// With none installed, the question behind a bare /skill is "where would I put
// one", and the four roots answer it without a paragraph about discovery.
func TestNoSkillsSaysWhereToPutThem(t *testing.T) {
	home := emptyHome(t)
	user, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	cwd := t.TempDir()

	application := commandApp(t, memoryAt(t, cwd), cwd)
	out, err := runCommand(t, application, "/skill")
	if err != nil {
		t.Fatalf("/skill: %v", err)
	}
	if !strings.Contains(out, "no skills are installed; a skill is a directory holding a SKILL.md") {
		t.Fatalf("output = %q, want the explanation", out)
	}
	for _, root := range []string{
		filepath.Join(home, "skills"),
		filepath.Join(user, ".agents", "skills"),
		filepath.Join(cwd, ".atlas", "skills"),
		filepath.Join(cwd, ".agents", "skills"),
	} {
		if !strings.Contains(out, root) {
			t.Fatalf("output = %q, want the root %s", out, root)
		}
	}
}
