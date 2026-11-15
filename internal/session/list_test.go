package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pi-go/internal/wire"
)

func TestDefaultDirEncodesTheWorkingDirectoryLikePi(t *testing.T) {
	agentDir := t.TempDir()
	got := DefaultDir("/work/project", agentDir)
	want := filepath.Join(agentDir, "sessions", "--work-project--")
	if got != want {
		t.Fatalf("default dir = %q, want %q", got, want)
	}
	// A trailing separator must not produce a double dash.
	if alias := DefaultDir("/work/project/", agentDir); alias != want {
		t.Fatalf("default dir with trailing slash = %q, want %q", alias, want)
	}
}

func TestAgentDirHonoursTheEnvironmentOverride(t *testing.T) {
	custom := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", custom)
	if got := AgentDir(); got != custom {
		t.Fatalf("agent dir = %q, want %q", got, custom)
	}
}

func TestReadInfoSummarizesASession(t *testing.T) {
	manager := newTestSession(t)
	if _, err := manager.AppendSessionInfo("Release notes"); err != nil {
		t.Fatalf("session info: %v", err)
	}
	user := wire.UserMessage("first question")
	user.Timestamp = 1_000
	if _, err := manager.AppendMessage(user); err != nil {
		t.Fatalf("append user: %v", err)
	}
	assistant := wire.AssistantMessage(wire.TextContent("first answer"))
	assistant.Timestamp = 5_000
	if _, err := manager.AppendMessage(assistant); err != nil {
		t.Fatalf("append assistant: %v", err)
	}

	info, err := ReadInfo(manager.File())
	if err != nil {
		t.Fatalf("read info: %v", err)
	}
	if info == nil {
		t.Fatal("read info returned nil for a session")
	}
	if info.ID != manager.ID() || info.Cwd != manager.Cwd() {
		t.Fatalf("info = %+v", info)
	}
	if info.Name != "Release notes" {
		t.Fatalf("name = %q", info.Name)
	}
	if info.MessageCount != 2 {
		t.Fatalf("message count = %d, want 2", info.MessageCount)
	}
	if info.FirstMessage != "first question" {
		t.Fatalf("first message = %q", info.FirstMessage)
	}
	if info.AllMessagesText != "first question first answer" {
		t.Fatalf("all messages = %q", info.AllMessagesText)
	}
	if !info.Modified.Equal(time.UnixMilli(5_000)) {
		t.Fatalf("modified = %v, want the newest message timestamp", info.Modified)
	}
	if !info.Created.Equal(epoch.Add(2 * time.Second)) {
		t.Fatalf("created = %v", info.Created)
	}

	// A file that is not a session yields nil rather than an error.
	if info, err := ReadInfo(writeFixture(t, `{"type":"message","id":"a1"}`)); err != nil || info != nil {
		t.Fatalf("non-session info = %+v, %v", info, err)
	}
}

func TestListSkipsBrokenFilesAndSortsByActivity(t *testing.T) {
	dir := t.TempDir()
	older, err := Create(dir, dir, Options{Clock: testClock()})
	if err != nil {
		t.Fatalf("create older: %v", err)
	}
	message := wire.UserMessage("older")
	message.Timestamp = 1_000
	if _, err := older.AppendMessage(message); err != nil {
		t.Fatalf("append older: %v", err)
	}
	newer, err := Create(dir, dir, Options{Clock: testClock()})
	if err != nil {
		t.Fatalf("create newer: %v", err)
	}
	message = wire.UserMessage("newer")
	message.Timestamp = 9_000
	if _, err := newer.AppendMessage(message); err != nil {
		t.Fatalf("append newer: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.jsonl"), []byte("not a session\n"), 0o644); err != nil {
		t.Fatalf("write broken file: %v", err)
	}

	infos, err := List(dir, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("sessions = %d, want 2: %+v", len(infos), infos)
	}
	if infos[0].ID != newer.ID() || infos[1].ID != older.ID() {
		t.Fatalf("order = %q, %q; want newest first", infos[0].ID, infos[1].ID)
	}

	if infos, err := List(filepath.Join(dir, "missing"), ""); err != nil || len(infos) != 0 {
		t.Fatalf("list of a missing directory = %+v, %v", infos, err)
	}
}

func TestFindMostRecentAndFindByID(t *testing.T) {
	dir := t.TempDir()
	first, err := Create(dir, dir, Options{Clock: testClock()})
	if err != nil {
		t.Fatalf("create first: %v", err)
	}
	appendUser(t, first, "one")
	second, err := Create(dir, dir, Options{Clock: testClock()})
	if err != nil {
		t.Fatalf("create second: %v", err)
	}
	appendUser(t, second, "two")

	newest := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(second.File(), newest, newest); err != nil {
		t.Fatalf("touch: %v", err)
	}
	if got := FindMostRecent(dir, ""); got != second.File() {
		t.Fatalf("most recent = %q, want %q", got, second.File())
	}
	if got := FindMostRecent(dir, first.Cwd()); got != second.File() {
		t.Fatalf("most recent for the session cwd = %q", got)
	}
	if got := FindMostRecent(dir, filepath.Join(dir, "elsewhere")); got != "" {
		t.Fatalf("most recent for another cwd = %q, want empty", got)
	}

	if got := FindByID(dir, second.ID(), ""); got != second.File() {
		t.Fatalf("find by id = %q, want %q", got, second.File())
	}
	if got := FindByID(dir, "nope", ""); got != "" {
		t.Fatalf("find by unknown id = %q, want empty", got)
	}
	if got := FindByID(filepath.Join(dir, "missing"), second.ID(), ""); got != "" {
		t.Fatalf("find in a missing directory = %q, want empty", got)
	}
}

// A header-only file carries no conversation, yet it is newer on disk than
// every real conversation. -continue must skip it: resuming an empty husk over
// the last real conversation is the one way to lose what the user meant to
// continue. Older atlas builds wrote such a file on every switch away; the
// guard stays for them and for a write that died before its first entry.
func TestFindMostRecentSkipsEntrylessSessions(t *testing.T) {
	dir := t.TempDir()
	placeholder, err := Create(dir, dir, Options{Clock: testClock()})
	if err != nil {
		t.Fatalf("create placeholder: %v", err)
	}
	if err := CreateFile(placeholder.File(), &File{Header: placeholder.Header()}); err != nil {
		t.Fatalf("write placeholder: %v", err)
	}
	real, err := Create(dir, dir, Options{Clock: testClock()})
	if err != nil {
		t.Fatalf("create real: %v", err)
	}
	appendUser(t, real, "one")

	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(placeholder.File(), future, future); err != nil {
		t.Fatalf("touch: %v", err)
	}
	if got := FindMostRecent(dir, ""); got != real.File() {
		t.Fatalf("most recent = %q, want the conversational session, not the header-only placeholder", got)
	}
}

func TestSessionFileNameReplacesTimestampSeparators(t *testing.T) {
	name := sessionFileName("2026-10-24T12:00:00.000Z", "0199-abc")
	if name != "2026-10-24T12-00-00-000Z_0199-abc.jsonl" {
		t.Fatalf("file name = %q", name)
	}
	if stamp, _, found := strings.Cut(name, "_"); found && strings.ContainsAny(stamp, ":.") {
		t.Fatalf("timestamp %q kept characters that are awkward in file names", stamp)
	}
}

// A shared session directory holds several projects, and the header cwd is what
// tells them apart. An empty cwd keeps everything: the wide list needs the
// unfiltered view.
func TestListFiltersByCwd(t *testing.T) {
	dir := t.TempDir()
	cwdA := filepath.Join(t.TempDir(), "alpha")
	cwdB := filepath.Join(t.TempDir(), "beta")
	for _, cwd := range []string{cwdA, cwdB} {
		if err := os.MkdirAll(cwd, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", cwd, err)
		}
	}
	first, err := Create(cwdA, dir, Options{Clock: testClock()})
	if err != nil {
		t.Fatalf("create in %s: %v", cwdA, err)
	}
	appendUser(t, first, "from alpha")
	second, err := Create(cwdB, dir, Options{Clock: testClock()})
	if err != nil {
		t.Fatalf("create in %s: %v", cwdB, err)
	}
	appendUser(t, second, "from beta")

	all, err := List(dir, "")
	if err != nil {
		t.Fatalf("list everything: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("sessions = %d, want both projects in one directory", len(all))
	}

	mine, err := List(dir, cwdA)
	if err != nil {
		t.Fatalf("list %s: %v", cwdA, err)
	}
	if len(mine) != 1 || mine[0].ID != first.ID() {
		t.Fatalf("sessions for %s = %+v", cwdA, mine)
	}

	// The same directory spelled with a trailing separator is the same
	// directory; the filter must not depend on how the caller wrote it.
	aliased, err := List(dir, cwdA+string(filepath.Separator))
	if err != nil || len(aliased) != 1 || aliased[0].ID != first.ID() {
		t.Fatalf("sessions for the aliased path = %+v, %v", aliased, err)
	}

	if none, err := List(dir, filepath.Join(cwdA, "elsewhere")); err != nil || len(none) != 0 {
		t.Fatalf("sessions for an unrelated cwd = %+v, %v", none, err)
	}
}

// ListAll is the wide view: every project bucket under the agent home, merged
// newest activity first, with no guess about which project is current.
func TestListAllSpansProjects(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", home)
	cwdA := filepath.Join(t.TempDir(), "alpha")
	cwdB := filepath.Join(t.TempDir(), "beta")
	for _, cwd := range []string{cwdA, cwdB} {
		if err := os.MkdirAll(cwd, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", cwd, err)
		}
	}
	if DefaultDir(cwdA, "") == DefaultDir(cwdB, "") {
		t.Fatalf("the two projects share one bucket, so this test proves nothing")
	}

	older, err := Create(cwdA, "", Options{Clock: testClock()})
	if err != nil {
		t.Fatalf("create %s: %v", cwdA, err)
	}
	olderMessage := wire.UserMessage("from alpha")
	olderMessage.Timestamp = 1_000
	if _, err := older.AppendMessage(olderMessage); err != nil {
		t.Fatalf("append: %v", err)
	}
	newer, err := Create(cwdB, "", Options{Clock: testClock()})
	if err != nil {
		t.Fatalf("create %s: %v", cwdB, err)
	}
	newerMessage := wire.UserMessage("from beta")
	newerMessage.Timestamp = 9_000
	if _, err := newer.AppendMessage(newerMessage); err != nil {
		t.Fatalf("append: %v", err)
	}

	infos, err := ListAll(home)
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("sessions = %d, want one from each project: %+v", len(infos), infos)
	}
	if infos[0].ID != newer.ID() || infos[1].ID != older.ID() {
		t.Fatalf("order = %q, %q; want the newest activity first", infos[0].ID, infos[1].ID)
	}
	for _, info := range infos {
		if parent := filepath.Dir(info.Path); parent == filepath.Join(home, "sessions") {
			t.Fatalf("session %q sits at the root of the session tree", info.Path)
		}
		if !strings.HasPrefix(info.Path, filepath.Join(home, "sessions")+string(filepath.Separator)) {
			t.Fatalf("session %q is not under %s", info.Path, filepath.Join(home, "sessions"))
		}
	}

	if none, err := ListAll(filepath.Join(home, "missing")); err != nil || len(none) != 0 {
		t.Fatalf("list all of a missing home = %+v, %v", none, err)
	}
}

// ListArchivedAll is the other half of the same tree: every project's archived
// sessions, and only those. /recover --all is the command that draws it, so the
// live sessions of the same buckets must stay out of it.
func TestListArchivedAllSpansProjects(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", home)
	cwdA := filepath.Join(t.TempDir(), "alpha")
	cwdB := filepath.Join(t.TempDir(), "beta")
	for _, cwd := range []string{cwdA, cwdB} {
		if err := os.MkdirAll(cwd, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", cwd, err)
		}
	}
	if DefaultDir(cwdA, "") == DefaultDir(cwdB, "") {
		t.Fatalf("the two projects share one bucket, so this test proves nothing")
	}

	// One archived session per project, plus one live session in the first
	// bucket: the wide archived list has to cross the tree without picking up a
	// conversation nobody put away.
	putAway := func(cwd, text string) *Manager {
		t.Helper()
		manager, err := Create(cwd, "", Options{Clock: testClock()})
		if err != nil {
			t.Fatalf("create %s: %v", cwd, err)
		}
		if _, err := manager.AppendMessage(wire.UserMessage(text)); err != nil {
			t.Fatalf("append: %v", err)
		}
		if err := Archive(manager.File()); err != nil {
			t.Fatalf("archive %s: %v", cwd, err)
		}
		return manager
	}
	putAway(cwdA, "from alpha")
	putAway(cwdB, "from beta")
	live, err := Create(cwdA, "", Options{Clock: testClock()})
	if err != nil {
		t.Fatalf("create %s: %v", cwdA, err)
	}
	if _, err := live.AppendMessage(wire.UserMessage("still here")); err != nil {
		t.Fatalf("append: %v", err)
	}

	infos, err := ListArchivedAll(home)
	if err != nil {
		t.Fatalf("list every archived session: %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("sessions = %d, want the two archived ones: %+v", len(infos), infos)
	}
	for _, info := range infos {
		if info.ID == live.ID() {
			t.Fatalf("the live session %q is in the archived list", info.ID)
		}
		if !Archived(info.Path) {
			t.Fatalf("session %q is not marked archived", info.Path)
		}
		if info.Cwd == "" {
			t.Fatalf("session %q lost the project it belongs to", info.Path)
		}
	}
	// Both rows are there whatever the order; the cwds are what the picker
	// prints in the column, so they have to survive the merge.
	seen := map[string]bool{}
	for _, info := range infos {
		seen[info.Cwd] = true
	}
	if !seen[cwdA] || !seen[cwdB] {
		t.Fatalf("cwds = %v, want one row per project", seen)
	}

	// The live half of the same tree is unaffected: archiving a session moves it
	// to the other list, it does not make it disappear.
	active, err := ListAll(home)
	if err != nil {
		t.Fatalf("list every session: %v", err)
	}
	if len(active) != 1 || active[0].ID != live.ID() {
		t.Fatalf("live sessions = %+v, want only %q", active, live.ID())
	}

	if none, err := ListArchivedAll(filepath.Join(home, "missing")); err != nil || len(none) != 0 {
		t.Fatalf("list every archived session of a missing home = %+v, %v", none, err)
	}
}

// A session that recorded no cwd cannot belong to any project, so a filtered
// search skips it: -session <path> stays the way to open one by hand.
func TestFindMostRecentSkipsSessionsWithoutCwd(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "2026-10-24T12-00-00-000Z_old.jsonl")
	lines := strings.Join([]string{
		`{"type":"session","version":3,"id":"old","timestamp":"2026-10-24T12:00:00.000Z"}`,
		`{"type":"message","id":"a1","parentId":null,"timestamp":"2026-10-24T12:00:01.000Z","message":{"role":"user","content":"old","timestamp":1000}}`,
	}, "\n")
	if err := os.WriteFile(path, []byte(lines), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if got := FindMostRecent(dir, ""); got != path {
		t.Fatalf("unfiltered = %q, want %q", got, path)
	}
	if got := FindMostRecent(dir, t.TempDir()); got != "" {
		t.Fatalf("filtered = %q, want nothing: a session without a cwd belongs to no project", got)
	}
}
