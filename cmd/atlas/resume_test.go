package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pi-go/internal/app"
	"pi-go/internal/assembly"
	"pi-go/internal/provider/faux"
	"pi-go/internal/session"
	"pi-go/internal/tui"
	"pi-go/internal/wire"
)

// resumeSetup builds a seeded session and an app running on an empty one in
// the same directory, which is the state a /resume starts from.
func resumeSetup(t *testing.T) (*app.App, *sessionPicker, *session.Manager) {
	t.Helper()
	cwd, dir := t.TempDir(), t.TempDir()
	target := sessionAt(t, cwd, dir)
	targetApp := commandApp(t, target, cwd, faux.Response{Text: "answer"})
	if _, err := targetApp.Ask(context.Background(), wire.UserMessage("hello"), nil); err != nil {
		t.Fatalf("ask: %v", err)
	}
	if err := targetApp.SetName("alpha"); err != nil {
		t.Fatalf("set name: %v", err)
	}
	current := sessionAt(t, cwd, dir)
	application := commandApp(t, current, cwd, faux.Response{Text: "onward"})
	return application, newSessionPicker(application, ""), target
}

// sessionAt creates a session file for one project directory.
func sessionAt(t *testing.T, cwd, dir string) *session.Manager {
	t.Helper()
	manager, err := session.Create(cwd, dir, session.Options{})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return manager
}

// A path argument is taken literally, blanks and all.
func TestResumeTakesAPathWithSpaces(t *testing.T) {
	cwd, dir := t.TempDir(), filepath.Join(t.TempDir(), "my sessions")
	target := sessionAt(t, cwd, dir)
	targetApp := commandApp(t, target, cwd, faux.Response{Text: "answer"})
	if _, err := targetApp.Ask(context.Background(), wire.UserMessage("hello"), nil); err != nil {
		t.Fatalf("ask: %v", err)
	}
	current := sessionAt(t, cwd, t.TempDir())
	application := commandApp(t, current, cwd, faux.Response{Text: "onward"})

	out, err := runCommand(t, application, "/resume "+target.File())
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if !strings.Contains(out, "resumed:") {
		t.Fatalf("output = %q, want the resumed line", out)
	}
	if got := application.Session().ID(); got != target.ID() {
		t.Fatalf("id = %q, want the target's %q", got, target.ID())
	}
}

// An id prefix finds the session in the directory, which is what a user who
// read the id off the status bar types.
func TestResumeSwitchesByIdPrefix(t *testing.T) {
	application, _, target := resumeSetup(t)

	out, err := runCommand(t, application, "/resume "+target.ID()[:8])
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if !strings.Contains(out, "resumed: alpha") {
		t.Fatalf("output = %q, want the resumed name", out)
	}
	if got := application.Session().ID(); got != target.ID() {
		t.Fatalf("id = %q, want the target's %q", got, target.ID())
	}
	if got := application.MessageCount(); got != 2 {
		t.Fatalf("messages = %d, want the restored conversation (2)", got)
	}
}

// Several sessions sharing a prefix cannot be told apart, and the error says
// so instead of guessing. The running session is seeded too, because a
// session without entries is not on disk and cannot match anything.
func TestResumeReportsAnAmbiguousPrefix(t *testing.T) {
	application, _, target := resumeSetup(t)
	if _, err := application.Ask(context.Background(), wire.UserMessage("now"), nil); err != nil {
		t.Fatalf("ask: %v", err)
	}
	dir := filepath.Dir(target.File())

	if _, err := resolveResumeTarget("0", dir, target.Cwd()); err == nil || !strings.Contains(err.Error(), "several sessions match") {
		t.Fatalf("err = %v, want an ambiguity error", err)
	}
}

func TestResumeReportsNoMatch(t *testing.T) {
	_, _, target := resumeSetup(t)
	dir := filepath.Dir(target.File())

	_, err := resolveResumeTarget("zzzzzzzz", dir, target.Cwd())
	if err == nil || !strings.Contains(err.Error(), "no session matches") {
		t.Fatalf("err = %v, want a no-match error", err)
	}
}

// Without an argument the piped reader gets the table: one row per session,
// ids first, because the ids are what /resume takes next.
func TestResumeWithoutArgumentPrintsTheTable(t *testing.T) {
	_, picker, _ := resumeSetup(t)

	out := &strings.Builder{}
	if err := picker.table(false, out); err != nil {
		t.Fatalf("list: %v", err)
	}
	text := out.String()
	if !strings.Contains(text, "alpha") {
		t.Fatalf("table = %q, want the session's name", text)
	}
	if !strings.Contains(text, "2 messages") {
		t.Fatalf("table = %q, want the message count", text)
	}
	if !strings.Contains(text, "resume one with /resume") {
		t.Fatalf("table = %q, want the way to pick", text)
	}
}

// An empty directory has nothing to resume, and the table says so plainly.
func TestResumeWithoutSessions(t *testing.T) {
	current := sessionAt(t, t.TempDir(), t.TempDir())
	application := commandApp(t, current, current.Cwd(), faux.Response{Text: "onward"})
	picker := newSessionPicker(application, "")

	out := &strings.Builder{}
	if err := picker.table(false, out); err != nil {
		t.Fatalf("list: %v", err)
	}
	if got := out.String(); got != "no sessions to resume\n" {
		t.Fatalf("table = %q, want the empty statement", got)
	}
}

// A path in another project opens, and says where the tools now run: the
// session records the directory it belongs to, and the tool tree follows it.
func TestResumeOpensAnotherProject(t *testing.T) {
	application, _, _ := resumeSetup(t)
	otherCwd := t.TempDir()
	other := sessionAt(t, otherCwd, t.TempDir())
	otherApp := commandApp(t, other, otherCwd, faux.Response{Text: "answer"})
	if _, err := otherApp.Ask(context.Background(), wire.UserMessage("hello"), nil); err != nil {
		t.Fatalf("ask: %v", err)
	}

	out, err := runCommand(t, application, "/resume "+other.File())
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if got := application.Session().Cwd(); got != otherCwd {
		t.Fatalf("cwd = %q, want the session's %q", got, otherCwd)
	}
	if !strings.Contains(out, "dir: "+otherCwd) {
		t.Fatalf("out = %q, want the directory the tools run in", out)
	}
}

// Two sessions created in the same millisecond share the first eight id
// characters; the prefix a row shows grows until the rows differ.
func TestUniqueShortIDExtendsClashes(t *testing.T) {
	ids := []string{"01a16eee-aaaa-1111", "01a16eee-bbbb-2222", "01a17185-cccc-3333"}
	if got := uniqueShortID("01a16eee-aaaa-1111", ids); got != "01a16eee-a" {
		t.Fatalf("id = %q, want the prefix that tells the clash apart", got)
	}
	if got := uniqueShortID("01a17185-cccc-3333", ids); got != "01a17185" {
		t.Fatalf("id = %q, want the plain eight characters", got)
	}
}

// resumeRequest is the -resume half of runConfigured: the prefix is resolved
// against the session list here, because the search and the candidates it
// refused belong to the terminal, and only the resolved path reaches the kernel.
func resumeRequest(t *testing.T, opts options, dir, cwd string) assembly.Options {
	t.Helper()
	request := opts.assembly()
	if opts.resumePath == "" {
		return request
	}
	target, err := resolveResumeTarget(opts.resumePath, dir, cwd)
	if err != nil {
		t.Fatalf("resolve %q: %v", opts.resumePath, err)
	}
	request.SessionPath = target
	return request
}

// The -resume flag opens the session it names, by path or by id prefix, before
// the session starts.
func TestResumeFlagOpensTheNamedSession(t *testing.T) {
	cwd, dir := t.TempDir(), t.TempDir()
	target := sessionAt(t, cwd, dir)
	targetApp := commandApp(t, target, cwd, faux.Response{Text: "answer"})
	if _, err := targetApp.Ask(context.Background(), wire.UserMessage("hello"), nil); err != nil {
		t.Fatalf("ask: %v", err)
	}

	for _, spec := range []string{target.File(), target.ID()[:8]} {
		opts, err := parseFlags([]string{"-resume", spec})
		if err != nil {
			t.Fatalf("parse -resume %q: %v", spec, err)
		}
		manager, _, err := assembly.OpenSession(resumeRequest(t, opts, dir, cwd), dir, cwd, true)
		if err != nil {
			t.Fatalf("open -resume %q: %v", spec, err)
		}
		if got := manager.ID(); got != target.ID() {
			t.Fatalf("id = %q, want the named session %q", got, target.ID())
		}
	}
}

// Regression for the empty sessions that piled up in the directory: starting
// atlas and starting a fresh conversation both leave nothing on disk. The file
// appears with the first message, so a conversation nobody talked to is not a
// session at all.
func TestNewLeavesNoSessionFileBehind(t *testing.T) {
	cwd, dir := t.TempDir(), t.TempDir()
	application := commandApp(t, sessionAt(t, cwd, dir), cwd)
	if entries := sessionFileNames(t, dir); len(entries) != 0 {
		t.Fatalf("startup wrote %v", entries)
	}

	for round := 0; round < 2; round++ {
		if _, err := runCommand(t, application, "/new"); err != nil {
			t.Fatalf("new: %v", err)
		}
		if entries := sessionFileNames(t, dir); len(entries) != 0 {
			t.Fatalf("/new wrote %v, want nothing", entries)
		}
	}
}

// Switching away is the other way an empty conversation used to gain a file.
func TestSwitchingAwayFromAnEmptySessionWritesNothing(t *testing.T) {
	cwd, dir := t.TempDir(), t.TempDir()
	target := sessionAt(t, cwd, dir)
	targetApp := commandApp(t, target, cwd, faux.Response{Text: "answer"})
	if _, err := targetApp.Ask(context.Background(), wire.UserMessage("hello"), nil); err != nil {
		t.Fatalf("ask: %v", err)
	}
	application := commandApp(t, sessionAt(t, cwd, dir), cwd)

	if _, err := runCommand(t, application, "/resume "+target.File()); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if entries := sessionFileNames(t, dir); len(entries) != 1 {
		t.Fatalf("session directory = %v, want only the recorded session", entries)
	}
}

// sessionFileNames lists what the session directory holds, which is the
// user-visible answer to "how many sessions do I have".
func sessionFileNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

// A prefix that matches several sessions is an error at startup too: guessing
// would open the wrong conversation and send the next prompt into it.
func TestResumeFlagReportsAnAmbiguousPrefix(t *testing.T) {
	cwd, dir := t.TempDir(), t.TempDir()
	first := sessionAt(t, cwd, dir)
	firstApp := commandApp(t, first, cwd, faux.Response{Text: "answer"})
	if _, err := firstApp.Ask(context.Background(), wire.UserMessage("one"), nil); err != nil {
		t.Fatalf("ask: %v", err)
	}
	second := sessionAt(t, cwd, dir)
	secondApp := commandApp(t, second, cwd, faux.Response{Text: "answer"})
	if _, err := secondApp.Ask(context.Background(), wire.UserMessage("two"), nil); err != nil {
		t.Fatalf("ask: %v", err)
	}

	opts, err := parseFlags([]string{"-resume", "0"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, err := resolveResumeTarget(opts.resumePath, dir, cwd); err == nil || !strings.Contains(err.Error(), "several sessions match") {
		t.Fatalf("err = %v, want an ambiguity error", err)
	}
}

func TestResumeFlagConflicts(t *testing.T) {
	for _, args := range [][]string{
		{"-resume", "x", "-session", "y"},
		{"-resume", "x", "-continue"},
	} {
		if _, err := parseFlags(args); err == nil || !strings.Contains(err.Error(), "choose one") {
			t.Fatalf("parseFlags(%v) err = %v, want a conflict", args, err)
		}
	}
}

// The help row is part of the table, which is the one place a user goes to see
// what exists.
func TestResumeIsInTheCommandTable(t *testing.T) {
	text := helpText()
	if !strings.Contains(text, "/resume") {
		t.Fatalf("help = %q, want the /resume row", text)
	}
}

// Switching is arbitrary: a conversation the user talked to got its file from
// the first message, so the picker lists it after the move and the app can come
// back to it by id.
func TestResumeComesBackToTheSessionItLeft(t *testing.T) {
	cwd, dir := t.TempDir(), t.TempDir()
	target := sessionAt(t, cwd, dir)
	targetApp := commandApp(t, target, cwd, faux.Response{Text: "answer"})
	if _, err := targetApp.Ask(context.Background(), wire.UserMessage("hello"), nil); err != nil {
		t.Fatalf("ask: %v", err)
	}
	if err := targetApp.SetName("alpha"); err != nil {
		t.Fatalf("set name: %v", err)
	}
	fresh := sessionAt(t, cwd, dir)
	application := commandApp(t, fresh, cwd, faux.Response{Text: "onward"})
	if _, err := application.Ask(context.Background(), wire.UserMessage("mine"), nil); err != nil {
		t.Fatalf("ask: %v", err)
	}
	picker := newSessionPicker(application, "")

	// Both conversations were talked to, so both have a file and both are
	// listed. Leaving one is not what created it; its first message did.
	listed := &strings.Builder{}
	if err := picker.table(false, listed); err != nil {
		t.Fatalf("list: %v", err)
	}
	if rows := sessionRows(listed.String()); len(rows) != 2 || !strings.Contains(listed.String(), "alpha") {
		t.Fatalf("list = %q, want both sessions", listed.String())
	}
	out, err := runCommand(t, application, "/resume "+target.File())
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if !strings.Contains(out, "resumed: alpha") {
		t.Fatalf("output = %q, want the resumed name", out)
	}

	// Back by id: the two sessions were created in the same millisecond in a
	// fast test, so the prefix typed here is the shortest one that tells them
	// apart — the first position where the ids diverge.
	spec := fresh.ID()
	for at := 8; at < len(spec); at++ {
		if target.ID()[at] != spec[at] {
			spec = spec[:at+1]
			break
		}
	}
	if _, err := runCommand(t, application, "/resume "+spec); err != nil {
		t.Fatalf("resume back: %v", err)
	}
	if got := application.Session().ID(); got != fresh.ID() {
		t.Fatalf("id = %q, want the session it left %q", got, fresh.ID())
	}
	if got := application.MessageCount(); got != 2 {
		t.Fatalf("messages = %d, want the restored conversation (2)", got)
	}
}

// --all is the only view that crosses projects. The plain list keeps the other
// project out; the wide one puts its row on the table with the directory that
// row belongs to.
func TestResumeAllListsOtherProjects(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	current, other := filepath.Join(root, "current"), filepath.Join(root, "other")
	for _, cwd := range []string{current, other} {
		if err := os.MkdirAll(cwd, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", cwd, err)
		}
	}
	currentSession := sessionAt(t, current, session.DefaultDir(current, home))
	otherSession := sessionAt(t, other, session.DefaultDir(other, home))
	for _, seed := range []struct {
		manager *session.Manager
		text    string
	}{{currentSession, "here"}, {otherSession, "there"}} {
		seedApp := commandApp(t, seed.manager, seed.manager.Cwd(), faux.Response{Text: "answer"})
		if _, err := seedApp.Ask(context.Background(), wire.UserMessage(seed.text), nil); err != nil {
			t.Fatalf("ask: %v", err)
		}
	}

	application := commandApp(t, currentSession, current, faux.Response{Text: "onward"})
	picker := newSessionPicker(application, home)

	// The plain list keeps the other project out; that difference is what the
	// flag buys.
	plain := &strings.Builder{}
	if err := picker.table(false, plain); err != nil {
		t.Fatalf("list: %v", err)
	}
	if rows := sessionRows(plain.String()); len(rows) != 1 {
		t.Fatalf("list = %q, want only this project's session", plain.String())
	}

	out := &strings.Builder{}
	if err := picker.table(true, out); err != nil {
		t.Fatalf("list all: %v", err)
	}
	table := out.String()
	if !strings.Contains(table, filepath.Base(other)) || !strings.Contains(table, "/resume --all <id prefix>") {
		t.Fatalf("table = %q, want the cwd column and the --all form", table)
	}
	if rows := sessionRows(table); len(rows) != 2 {
		t.Fatalf("table = %q, want one row per project", table)
	}
}

// A row the wide table shows has to be a row the reader can name: the plain id
// prefix misses a session recorded in the other project, and --all finds it and
// moves the workspace with it.
//
// The two sessions share one session directory here — the store a configured
// -session-dir gives — because that is the shape where an id prefix can be
// resolved at all: the wide list of a shared directory is that directory with
// the cwd filter lifted. The bucket-per-project store is the table's business
// above, and session.ListAll's below.
func TestResumeAllSwitchesToAnotherProject(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "sessions")
	current, other := filepath.Join(root, "current"), filepath.Join(root, "other")
	for _, cwd := range []string{current, other} {
		if err := os.MkdirAll(cwd, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", cwd, err)
		}
	}
	currentSession := sessionAt(t, current, dir)
	otherSession := sessionAt(t, other, dir)
	for _, seed := range []struct {
		manager *session.Manager
		text    string
	}{{currentSession, "here"}, {otherSession, "there"}} {
		seedApp := commandApp(t, seed.manager, seed.manager.Cwd(), faux.Response{Text: "answer"})
		if _, err := seedApp.Ask(context.Background(), wire.UserMessage(seed.text), nil); err != nil {
			t.Fatalf("ask: %v", err)
		}
	}

	application := commandApp(t, currentSession, current, faux.Response{Text: "onward"})

	if _, err := runCommand(t, application, resumeTrigger+" "+otherSession.ID()); err == nil ||
		!strings.Contains(err.Error(), "no session matches") {
		t.Fatalf("err = %v, want the narrow list to miss the other project's session", err)
	}
	switched, err := runCommand(t, application, resumeTrigger+" "+resumeAllArg+" "+otherSession.ID())
	if err != nil {
		t.Fatalf("resume --all: %v", err)
	}
	if got := application.Session().Cwd(); got != other {
		t.Fatalf("cwd = %q, want the other project %q", got, other)
	}
	if !strings.Contains(switched, "dir: "+other) {
		t.Fatalf("out = %q, want the directory the tools now run in", switched)
	}
}

// sessionRows counts the body rows of a session table: the header and the blank
// line above it are not rows.
func sessionRows(table string) []string {
	var rows []string
	for _, line := range strings.Split(table, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "ago") || strings.HasPrefix(trimmed, "resume one with") {
			continue
		}
		rows = append(rows, line)
	}
	return rows
}

// TestBareResumeOpensAMenu: the bare command asks which conversation, and a
// terminal answers it with a list to move through.
func TestBareResumeOpensAMenu(t *testing.T) {
	application, picker, target := resumeSetup(t)
	out := &strings.Builder{}
	var asked tui.PickRequest
	pick := func(req tui.PickRequest) tui.PickAnswer {
		asked = req
		return tui.PickAnswer{Value: target.File(), Taken: true}
	}
	done, err := replCommand(context.Background(), application, nil, "/resume", models{}, picker, pick, emptyPrompts(), out, newRenderer(out, out, false), mcpState{}, nil)
	if done || err != nil {
		t.Fatalf("replCommand = (%v, %v), want the resume", done, err)
	}
	if asked.Title != "resume" {
		t.Fatalf("the menu is titled %q", asked.Title)
	}
	// A session has nothing to turn, so the footer does not advertise ←/→.
	if asked.Axis != "" {
		t.Fatalf("the resume menu has an axis: %q", asked.Axis)
	}
	items := asked.Items
	if len(items) == 0 {
		t.Fatal("the menu is empty, want the recorded session in it")
	}
	found := false
	for _, item := range items {
		if !strings.Contains(item.Label, "alpha") {
			continue
		}
		found = true
		if item.Value != target.File() {
			t.Fatalf("that row carries %q, want the session file", item.Value)
		}
		if !strings.Contains(item.Note, "message") {
			t.Fatalf("that row's note = %q, want what is in it", item.Note)
		}
	}
	if !found {
		t.Fatalf("the recorded session is not in the menu: %+v", items)
	}
	if !strings.Contains(out.String(), "resumed: alpha") {
		t.Fatalf("output = %q, want the resumed line", out.String())
	}
	if application.Session().File() != target.File() {
		t.Fatalf("session in effect = %q, want the picked one", application.Session().File())
	}

	// A second look opens on the session in effect: that is what makes the menu
	// a map as well as a list.
	opened := ""
	again := func(req tui.PickRequest) tui.PickAnswer {
		opened = req.Current
		return tui.PickAnswer{}
	}
	if _, err := replCommand(context.Background(), application, nil, "/resume", models{}, picker, again, emptyPrompts(), out, newRenderer(out, out, false), mcpState{}, nil); err != nil {
		t.Fatalf("replCommand: %v", err)
	}
	if opened != target.File() {
		t.Fatalf("the menu opened on %q, want the session in effect", opened)
	}
}

// TestClosingTheResumeMenuChangesNothing: closing the list is not a switch.
func TestClosingTheResumeMenuChangesNothing(t *testing.T) {
	application, picker, _ := resumeSetup(t)
	before := application.Session().ID()
	out := &strings.Builder{}
	pick := func(tui.PickRequest) tui.PickAnswer { return tui.PickAnswer{} }

	if _, err := replCommand(context.Background(), application, nil, "/resume", models{}, picker, pick, emptyPrompts(), out, newRenderer(out, out, false), mcpState{}, nil); err != nil {
		t.Fatalf("replCommand: %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("output = %q, want nothing said", out.String())
	}
	if application.Session().ID() != before {
		t.Fatalf("session in effect = %q, want it left alone", application.Session().ID())
	}
}

// TestResumingInTheFullScreenSessionReplaysOnce: the command reports the switch
// and the screen rebuilds itself from the same messages, so the conversation
// must not be printed by both. Before this was split, a resumed turn appeared
// twice with nothing between the copies.
func TestResumingInTheFullScreenSessionReplaysOnce(t *testing.T) {
	application, picker, target := resumeSetup(t)

	var lines, answers []string
	resets := 0
	sink := tui.Sink{
		Note:   func(text string) { lines = append(lines, text) },
		Answer: func(text string) { answers = append(answers, text) },
		View:   func(int) {},
		Reset: func(view int) {
			if view != tui.ViewMain {
				t.Fatalf("resetting view %d after a session switch, want the main one", view)
			}
			resets++
			lines, answers = nil, nil
		},
	}
	if err := runTUICommand(context.Background(), application, nil, mcpState{}, models{}, picker, emptyPrompts(), nil, "/resume "+target.ID()[:8], sink); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if resets != 1 {
		t.Fatalf("the transcript was reset %d times, want once", resets)
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "resumed: alpha") {
		t.Fatalf("the report is missing:\n%s", joined)
	}
	if got := strings.Count(joined, "history: 2 messages"); got != 1 {
		t.Fatalf("history appears %d times, want once:\n%s", got, joined)
	}
	// The answer travels as an answer, not as a line of prose: the transcript
	// draws the bar and wraps the text inside it, and prose cannot be wrapped
	// back into that shape.
	if len(answers) != 1 || answers[0] != "answer" {
		t.Fatalf("answers = %q, want the recorded one", answers)
	}
}
