package main

import (
	"charm.land/lipgloss/v2"
	"strings"
	"testing"

	"pi-go/internal/app"
	"pi-go/internal/task"
	"pi-go/internal/tool"
	"pi-go/internal/tool/echo"
	"pi-go/internal/tool/git"
	"pi-go/internal/tool/tasks"
	"pi-go/internal/tool/web"
	"pi-go/internal/wire"
)

// sampleStatus is a footer width test bed: a cwd deep enough to shorten, a named
// session, one tool group, a provider-qualified model, a thinking level and a
// context reading.
func sampleStatus() statusInfo {
	return statusInfo{
		dir:      "/Volumes/MacData/Projects/vibe_project/pi-agent-go",
		session:  "#01a16da2",
		grants:   "fs+bash",
		model:    "deepseek/deepseek-chat",
		thinking: "think high",
		context:  "ctx 188k left",
	}
}

// TestStatusLineKeepsEveryFieldWhenItFits is the reference frame: six facts,
// one line, in the order the session is set up in.
func TestStatusLineKeepsEveryFieldWhenItFits(t *testing.T) {
	want := "/Volumes/MacData/Projects/vibe_project/pi-agent-go | #01a16da2 | fs+bash | deepseek/deepseek-chat | think high | ctx 188k left"
	if got := sampleStatus().line(200); got != want {
		t.Fatalf("line = %q, want %q", got, want)
	}
}

// TestStatusLineLeavesOutAnAbsentLevel: off is the default and spends cells on
// saying nothing, so the field is absent rather than empty — and the line closes
// up without a double separator where it would have been.
func TestStatusLineLeavesOutAnAbsentLevel(t *testing.T) {
	s := sampleStatus()
	s.thinking = ""
	want := "/Volumes/MacData/Projects/vibe_project/pi-agent-go | #01a16da2 | fs+bash | deepseek/deepseek-chat | ctx 188k left"
	if got := s.line(200); got != want {
		t.Fatalf("line = %q, want %q", got, want)
	}
}

// TestThinkingFieldNamesTheEffortTheRequestCarries: the footer shows a level
// only when a request would carry one, which is the same rule the banner and
// the `/model` feedback follow.
func TestThinkingFieldNamesTheEffortTheRequestCarries(t *testing.T) {
	for _, tc := range []struct {
		reasoning bool
		level     string
		want      string
	}{
		{true, "low", "think low"},
		{true, "high", "think high"},
		{true, "max", "think max"},
		{true, "off", ""},
		{true, "", ""},
		{false, "high", ""},
		{false, "off", ""},
	} {
		model := wire.Model{ID: "m1", Provider: "test", Reasoning: tc.reasoning}
		if got := thinkingField(model, tc.level); got != tc.want {
			t.Fatalf("thinkingField(reasoning=%v, %q) = %q, want %q", tc.reasoning, tc.level, got, tc.want)
		}
	}
}

// TestStatusLineTracksALevelSetAfterStartup: the footer is a callback the editor
// asks on every repaint, so /model has to show up on the next line without
// rebuilding the bar.
func TestStatusLineTracksALevelSetAfterStartup(t *testing.T) {
	application, err := app.New(app.Config{
		Model:    wire.Model{ID: "m1", Provider: "test", ContextWindow: 100000, Reasoning: true},
		Provider: cancelled{},
	}, inMemory(t))
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	bar := newStatusBar(application, nil, "/tmp/project", "fs+bash")

	if got := bar.line(200); strings.Contains(got, "think ") {
		t.Fatalf("fresh line = %q, want no level while the level is off", got)
	}
	if err := application.SetThinkingLevel("high"); err != nil {
		t.Fatalf("set level: %v", err)
	}
	if got := bar.line(200); !strings.Contains(got, "| think high |") {
		t.Fatalf("line after the switch = %q, want the level beside the model", got)
	}
}

// TestStatusLineDropsFieldsInsteadOfCuttingTheEdge walks the ladder. Every frame
// has to fit, and the field that survives longest is the context reading: it is
// the one that answers "can I keep going".
func TestStatusLineDropsFieldsInsteadOfCuttingTheEdge(t *testing.T) {
	s := sampleStatus()
	for _, tc := range []struct {
		width int
		want  string
	}{
		// 126 cells: everything fits.
		{126, "/Volumes/MacData/Projects/vibe_project/pi-agent-go | #01a16da2 | fs+bash | deepseek/deepseek-chat | think high | ctx 188k left"},
		// The provider goes first.
		{117, "/Volumes/MacData/Projects/vibe_project/pi-agent-go | #01a16da2 | fs+bash | deepseek-chat | think high | ctx 188k left"},
		// Then the cwd loses its head.
		{93, "…/vibe_project/pi-agent-go | #01a16da2 | fs+bash | deepseek-chat | think high | ctx 188k left"},
		// Then the thinking level: the session name outlives the effort the
		// request is asked to make.
		{80, "…/vibe_project/pi-agent-go | #01a16da2 | fs+bash | deepseek-chat | ctx 188k left"},
		// Then the grant.
		{70, "…/vibe_project/pi-agent-go | #01a16da2 | deepseek-chat | ctx 188k left"},
		// Then the cwd's parent.
		{57, "…/pi-agent-go | #01a16da2 | deepseek-chat | ctx 188k left"},
		// Then the cwd.
		{41, "#01a16da2 | deepseek-chat | ctx 188k left"},
		// Then the session name.
		{29, "deepseek-chat | ctx 188k left"},
		// Only the reading is left.
		{13, "ctx 188k left"},
	} {
		// The width in the table is the frame's own width, so a frame that
		// changes shape fails here rather than sliding one step up the ladder
		// unnoticed.
		if measured := lipgloss.Width(tc.want); measured != tc.width {
			t.Fatalf("frame %q is %d cells wide, not %d", tc.want, measured, tc.width)
		}
		got := s.line(tc.width)
		if got != tc.want {
			t.Fatalf("line(%d) = %q, want %q", tc.width, got, tc.want)
		}
	}
}

// TestStatusLineNeverExceedsItsWidth covers the frames above one by one; this
// one checks the promise they all make, at every width in between.
func TestStatusLineNeverExceedsItsWidth(t *testing.T) {
	s := sampleStatus()
	for width := 13; width <= 200; width++ {
		if line := s.line(width); lipgloss.Width(line) > width {
			t.Fatalf("line(%d) = %q is %d cells wide", width, line, lipgloss.Width(line))
		}
	}
}

// TestStatusLineKeepsTheModeWhenTheToolsGo is the footer's own promise: the
// permission mode is in every frame a terminal can hold, at every width, and it
// is the last field to leave. The tools go first, and they go alone — 0.12.18
// named the three task tools one per column, which made the joined field long
// enough that a 100- or 120-cell terminal dropped the whole thing, mode
// included, and the footer stopped saying how a call would be approved.
func TestStatusLineKeepsTheModeWhenTheToolsGo(t *testing.T) {
	s := sampleStatus()
	// What the bar hands the line once a session has the task tools: the joined
	// field for the frames with room, and the mode alone for the frames without.
	s.grants = "fs+bash+git+web+tasks · Ask"
	s.mode = "Ask"
	for _, tc := range []struct {
		width int
		want  string
	}{
		{146, "/Volumes/MacData/Projects/vibe_project/pi-agent-go | #01a16da2 | fs+bash+git+web+tasks · Ask | deepseek/deepseek-chat | think high | ctx 188k left"},
		// The provider goes, then the cwd's head: the joined field survives
		// both.
		{137, "/Volumes/MacData/Projects/vibe_project/pi-agent-go | #01a16da2 | fs+bash+git+web+tasks · Ask | deepseek-chat | think high | ctx 188k left"},
		{113, "…/vibe_project/pi-agent-go | #01a16da2 | fs+bash+git+web+tasks · Ask | deepseek-chat | think high | ctx 188k left"},
		// The thinking level goes first, and the session name survives it.
		{100, "…/vibe_project/pi-agent-go | #01a16da2 | fs+bash+git+web+tasks · Ask | deepseek-chat | ctx 188k left"},
		// The tools go and the mode takes their place, which is the frame the
		// regression was missing.
		{76, "…/vibe_project/pi-agent-go | #01a16da2 | Ask | deepseek-chat | ctx 188k left"},
		{63, "…/pi-agent-go | #01a16da2 | Ask | deepseek-chat | ctx 188k left"},
		{47, "#01a16da2 | Ask | deepseek-chat | ctx 188k left"},
		{35, "Ask | deepseek-chat | ctx 188k left"},
		{19, "Ask | ctx 188k left"},
	} {
		if measured := lipgloss.Width(tc.want); measured != tc.width {
			t.Fatalf("frame %q is %d cells wide, not %d", tc.want, measured, tc.width)
		}
		if got := s.line(tc.width); got != tc.want {
			t.Fatalf("line(%d) = %q, want %q", tc.width, got, tc.want)
		}
	}

	// The three widths the bug was reported at: the mode is on all of them. The
	// thinking level freed the cells the joined field needed at 100, so the
	// tools are still beside the mode there; at 80 the tools are gone and the
	// mode stands alone.
	for _, width := range []int{120, 100, 80} {
		line := s.line(width)
		if !strings.Contains(line, "Ask") {
			t.Fatalf("line(%d) = %q, want the mode", width, line)
		}
		if width < 100 && strings.Contains(line, "fs+bash") {
			t.Fatalf("line(%d) = %q, want the tool summary gone", width, line)
		}
	}

	// Every width above the floor keeps the mode, and none of them exceeds the
	// terminal it was composed for.
	for width := 19; width <= 200; width++ {
		line := s.line(width)
		if !strings.Contains(line, "Ask") {
			t.Fatalf("line(%d) = %q, want the mode in every frame above the floor", width, line)
		}
		if lipgloss.Width(line) > width {
			t.Fatalf("line(%d) = %q is %d cells wide", width, line, lipgloss.Width(line))
		}
	}
}

// TestStatusLineLeavesOutAnAbsentMode: a session with no permission engine has
// no mode, and the ladder is then exactly the one it was before the field was
// split — no separator, no empty column.
func TestStatusLineLeavesOutAnAbsentMode(t *testing.T) {
	s := sampleStatus()
	for _, width := range []int{200, 120, 100, 80, 60, 40} {
		line := s.line(width)
		if strings.Contains(line, " |  | ") || strings.HasSuffix(line, " | ") {
			t.Fatalf("line(%d) = %q, want no empty field", width, line)
		}
	}
}

// TestStatusLineNamesTheSideConversationFirst: the marker is not a seventh fact
// about the session, it is the answer to "which conversation am I typing into",
// so it leads. It also has to survive the ladder down to the frame that has
// nothing else left, because a footer that described the main conversation
// while the prompt belonged to the side one would be worse than a short footer.
func TestStatusLineNamesTheSideConversationFirst(t *testing.T) {
	s := sampleStatus()
	s.side = "side"
	if got, want := s.line(200), "side | "+sampleStatus().line(200); got != want {
		t.Fatalf("line = %q, want %q", got, want)
	}
	if got, want := s.line(20), "side | ctx 188k left"; got != want {
		t.Fatalf("line(20) = %q, want %q", got, want)
	}
	// Below the narrowest frame the line falls back to the reading alone, which
	// is the one frame the marker cannot survive; that is the existing floor.
	if got, want := s.line(13), "ctx 188k left"; got != want {
		t.Fatalf("line(13) = %q, want %q", got, want)
	}
}

// TestSideFieldIsEmptyUntilThePromptMoves: the field costs nothing in the main
// conversation, so every frame is unchanged there.
func TestSideFieldIsEmptyUntilThePromptMoves(t *testing.T) {
	if got := sideField(nil); got != "" {
		t.Fatalf("sideField(nil) = %q, want an empty field", got)
	}
	state := newSideState()
	if got := sideField(state); got != "" {
		t.Fatalf("sideField(fresh) = %q, want an empty field", got)
	}
	state.enter(&app.Side{})
	if got := sideField(state); got != "side" {
		t.Fatalf("sideField(in side) = %q, want side", got)
	}
	state.toMain()
	if got := sideField(state); got != "" {
		t.Fatalf("sideField(back in main) = %q, want an empty field", got)
	}
}

// TestStatusBarFollowsTheSideConversation checks the bar through the state it
// actually reads, so /side and /parent show up on the next repaint.
func TestStatusBarFollowsTheSideConversation(t *testing.T) {
	application, err := app.New(app.Config{
		Model:    wire.Model{ID: "m1", Provider: "test", ContextWindow: 100000},
		Provider: cancelled{},
	}, inMemory(t))
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	state := newSideState()
	bar := newStatusBar(application, state, "/tmp/project", "fs+bash")

	if got := bar.line(200); strings.HasPrefix(got, "side") {
		t.Fatalf("fresh line = %q, want the main conversation", got)
	}
	side, err := application.BeginSide()
	if err != nil {
		t.Fatalf("BeginSide: %v", err)
	}
	state.enter(side)
	if got := bar.line(200); !strings.HasPrefix(got, "side | ") {
		t.Fatalf("line in the side conversation = %q, want the marker first", got)
	}
	state.toMain()
	if got := bar.line(200); strings.HasPrefix(got, "side") {
		t.Fatalf("line after /parent = %q, want the main conversation", got)
	}
}

// TestFormatTokensMatchesPisFooter pins the number format to pi's, so the same
// session read in either tool shows the same figure.
func TestFormatTokensMatchesPisFooter(t *testing.T) {
	for _, tc := range []struct {
		count int
		want  string
	}{
		{0, "0"},
		{999, "999"},
		{1000, "1.0k"},
		{9500, "9.5k"},
		{10000, "10k"},
		{188000, "188k"},
		{999999, "1000k"},
		{1000000, "1.0M"},
		{9500000, "9.5M"},
		{10000000, "10M"},
		{123000000, "123M"},
	} {
		if got := formatTokens(tc.count); got != tc.want {
			t.Fatalf("formatTokens(%d) = %q, want %q", tc.count, got, tc.want)
		}
	}
}

// TestContextFieldReportsWhatIsLeft keeps the reading honest when the window is
// unknown or already full.
func TestContextFieldReportsWhatIsLeft(t *testing.T) {
	for _, tc := range []struct {
		tokens, window int
		want           string
	}{
		{12000, 200000, "ctx 188k left"},
		{0, 0, "ctx 0 used"},
		{12000, 0, "ctx 12k used"},
		{5000, 4000, "ctx 0 left"},
	} {
		if got := contextField(tc.tokens, tc.window); got != tc.want {
			t.Fatalf("contextField(%d, %d) = %q, want %q", tc.tokens, tc.window, got, tc.want)
		}
	}
}

func TestSessionFieldPrefersTheName(t *testing.T) {
	for _, tc := range []struct {
		name, id string
		started  bool
		want     string
	}{
		{"atlas rewrite", "01a16da2-3c4d", true, "atlas rewrite"},
		{"", "01a16da2-3c4d", true, "#01a16da2"},
		{"", "short", true, "#short"},
		{"", "", true, ""},
	} {
		if got := sessionField(tc.name, tc.id, tc.started); got != tc.want {
			t.Fatalf("sessionField(%q, %q) = %q, want %q", tc.name, tc.id, got, tc.want)
		}
	}
}

// TestSessionFieldWaitsForTheFirstMessage pins the footer's half of "opening
// atlas does not start a session": the id is what proves a conversation exists,
// so it appears with the first entry and not with the process. Before this, the
// home page carried "| #01a17b1f" for a session whose file had never been
// written.
func TestSessionFieldWaitsForTheFirstMessage(t *testing.T) {
	for _, tc := range []struct{ name, id string }{
		{"", "01a17b1f-3f01-79b5-9213-505ae4812302"},
		{"atlas rewrite", "01a17b1f-3f01-79b5-9213-505ae4812302"},
	} {
		if got := sessionField(tc.name, tc.id, false); got != "" {
			t.Fatalf("an empty session was named %q in the footer", got)
		}
	}
}

// TestStatusLineKeepsTheNameWhenTheThinkingGoes is the reported bug: at a
// common eighty columns the old ladder dropped the session name while "think
// high" was still on the line. The name is the fact the footer must keep, and
// the thinking level is the one it spends first, so the name survives the
// level, the tool summary, and the directory.
func TestStatusLineKeepsTheNameWhenTheThinkingGoes(t *testing.T) {
	s := sampleStatus()
	s.session = "release notes review"
	if got, want := s.line(80), "…/pi-agent-go | release notes review | deepseek-chat | ctx 188k left"; got != want {
		t.Fatalf("line(80) = %q, want %q", got, want)
	}
	if got := s.line(80); strings.Contains(got, "think ") {
		t.Fatalf("line(80) = %q, want the thinking level gone", got)
	}

	// A session under a permission mode keeps both the name and the mode at the
	// same width: the mode never leaves, and the level is what buys the room.
	s.mode = "Ask"
	if got, want := s.line(80), "…/pi-agent-go | release notes review | Ask | deepseek-chat | ctx 188k left"; got != want {
		t.Fatalf("line(80) with mode = %q, want %q", got, want)
	}
}

func TestShortHomeOnlyRewritesPathsUnderHome(t *testing.T) {
	for _, tc := range []struct {
		path, home string
		want       string
	}{
		{"/Users/crane/proj", "/Users/crane", "~/proj"},
		{"/Users/crane", "/Users/crane", "~"},
		{"/Users/crane", "/Users/crane/", "~"},
		{"/Users/cranefield/proj", "/Users/crane", "/Users/cranefield/proj"},
		{"/tmp/proj", "/Users/crane", "/tmp/proj"},
		{"/Users/crane/proj", "", "/Users/crane/proj"},
	} {
		if got := shortHome(tc.path, tc.home); got != tc.want {
			t.Fatalf("shortHome(%q, %q) = %q, want %q", tc.path, tc.home, got, tc.want)
		}
	}
}

func TestShortenPathKeepsTheTail(t *testing.T) {
	for _, tc := range []struct {
		path string
		keep int
		want string
	}{
		{"~/vibe_project/pi-agent-go", 2, "…/vibe_project/pi-agent-go"},
		{"~/vibe_project/pi-agent-go", 1, "…/pi-agent-go"},
		{"~/vibe_project/pi-agent-go", 3, "~/vibe_project/pi-agent-go"},
		{"/pi-agent-go", 1, "/pi-agent-go"},
	} {
		if got := shortenPath(tc.path, tc.keep); got != tc.want {
			t.Fatalf("shortenPath(%q, %d) = %q, want %q", tc.path, tc.keep, got, tc.want)
		}
	}
}

// TestDirFramesShortenThePathAndNotTheBranch: a branch name is one name that
// happens to contain a slash, so it is appended to the path after the path has
// been shortened. Joining first and shortening second would cut
// "personal/dfbuddy" at its slash and leave "…/dfbuddy)" — a branch that does
// not exist, printed in brackets that no longer close.
func TestDirFramesShortenThePathAndNotTheBranch(t *testing.T) {
	s := sampleStatus()
	s.branch = "personal/dfbuddy"
	want := [3]string{
		"/Volumes/MacData/Projects/vibe_project/pi-agent-go (personal/dfbuddy)",
		"…/vibe_project/pi-agent-go (personal/dfbuddy)",
		"…/pi-agent-go (personal/dfbuddy)",
	}
	if got := s.dirFrames(); got != want {
		t.Fatalf("dirFrames() = %#v, want %#v", got, want)
	}

	s.branch = ""
	want = [3]string{
		"/Volumes/MacData/Projects/vibe_project/pi-agent-go",
		"…/vibe_project/pi-agent-go",
		"…/pi-agent-go",
	}
	if got := s.dirFrames(); got != want {
		t.Fatalf("outside a repository dirFrames() = %#v, want %#v", got, want)
	}
}

// TestStatusLineNeverCutsTheBranchInHalf is the property behind the rule, checked
// at every width: the footer may drop the directory, it may lengthen it, but the
// branch it prints is either whole or absent.
func TestStatusLineNeverCutsTheBranchInHalf(t *testing.T) {
	s := sampleStatus()
	s.branch = "personal/dfbuddy"
	for width := 1; width <= 200; width++ {
		line := s.line(width)
		if strings.Contains(line, "(personal/dfbuddy)") {
			continue
		}
		if strings.Contains(line, "dfbuddy") {
			t.Fatalf("width %d prints part of the branch: %q", width, line)
		}
		if strings.Count(line, "(") != strings.Count(line, ")") {
			t.Fatalf("width %d leaves a bracket open: %q", width, line)
		}
	}
}

// TestStatusLineDropsTheDirectoryRatherThanHalfABranch: at a width where the
// shortest directory frame no longer fits, the field leaves whole. The old code
// answered this width with "…/dfbuddy) | deepseek-chat | think high | ctx 188k
// left", which is the bug as reported.
func TestStatusLineDropsTheDirectoryRatherThanHalfABranch(t *testing.T) {
	s := sampleStatus()
	s.branch = "personal/dfbuddy"
	want := "#01a16da2 | deepseek-chat | ctx 188k left"
	if got := s.line(55); got != want {
		t.Fatalf("line = %q, want %q", got, want)
	}
}

// TestStatusLineKeepsTheWholeBranchAtEightyColumns pins the frame the bug was
// reported from. It does not fail against the old code — shortening the joined
// string by two segments happened to reassemble this branch, because the slash
// inside it fell on a segment boundary — so the guard is the property test
// above. What this one holds down is the other half of the rule: a longer
// directory frame must still buy the branch the room to be shown whole at the
// width people actually use.
func TestStatusLineKeepsTheWholeBranchAtEightyColumns(t *testing.T) {
	s := sampleStatus()
	s.branch = "personal/dfbuddy"
	want := "…/pi-agent-go (personal/dfbuddy) | #01a16da2 | deepseek-chat | ctx 188k left"
	if got := s.line(80); got != want {
		t.Fatalf("line = %q, want %q", got, want)
	}
}

func TestShortModelDropsTheProvider(t *testing.T) {
	for _, tc := range []struct{ model, want string }{
		{"deepseek/deepseek-chat", "deepseek-chat"},
		{"m1", "m1"},
		{"", ""},
	} {
		if got := shortModel(tc.model); got != tc.want {
			t.Fatalf("shortModel(%q) = %q, want %q", tc.model, got, tc.want)
		}
	}
}

// TestToolSummaryNamesTheGroups is the permission field: it reports what the
// session may touch, and a tool outside the known groups is named as itself
// rather than hidden.
func TestToolSummaryNamesTheGroups(t *testing.T) {
	all := workspaceTools(t.TempDir(), nil, nil, nil)
	if got := toolSummary(all); got != "fs+bash" {
		t.Fatalf("toolSummary(all) = %q, want fs+bash", got)
	}
	if got := toolSummary(all[:3]); got != "fs" {
		t.Fatalf("toolSummary(file tools) = %q, want fs", got)
	}
	if got := toolSummary(nil); got != "" {
		t.Fatalf("toolSummary(nil) = %q, want an empty field", got)
	}
	if got := toolSummary([]tool.Tool{echo.New()}); got != "echo" {
		t.Fatalf("toolSummary(echo) = %q, want the tool named as itself", got)
	}

	// The web tools share one group because they are one grant: nobody decides
	// to allow search but not fetch.
	web := web.Tools(web.Options{APIKey: "test-key"})
	if got := toolSummary(web); got != "web" {
		t.Fatalf("toolSummary(web) = %q, want web", got)
	}
	if got := toolSummary(workspaceTools(t.TempDir(), web, nil, nil)); got != "fs+bash+web" {
		t.Fatalf("toolSummary(with web) = %q, want fs+bash+web", got)
	}

	// The four git tools are one grant for the same reason. Path is set so the
	// assertion is about the grouping rather than about this machine's PATH.
	gitTools := git.Tools(git.Options{Cwd: t.TempDir(), Path: "/usr/bin/git"})
	if len(gitTools) != 4 {
		t.Fatalf("git tools = %d, want 4", len(gitTools))
	}
	if got := toolSummary(gitTools); got != "git" {
		t.Fatalf("toolSummary(git) = %q, want git", got)
	}
}

// TestToolSummaryNamesTheTaskToolsOnce is the 0.12.18 regression, in the footer
// rather than in the tool: the three task tools were named one per column,
// which made the joined grant field long enough that an ordinary terminal
// dropped the whole field and the permission mode with it.
func TestToolSummaryNamesTheTaskToolsOnce(t *testing.T) {
	dir := t.TempDir()
	service := task.New(task.Options{Dir: func() string { return dir }})
	defer func() {
		if err := service.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
	}()
	background := tasks.Tools(service)
	if len(background) != 3 {
		t.Fatalf("task tools = %d, want 3", len(background))
	}
	if got := toolSummary(background); got != "tasks" {
		t.Fatalf("toolSummary(tasks) = %q, want tasks", got)
	}
	set := append(workspaceTools(t.TempDir(), nil, nil, nil), background...)
	if got := toolSummary(set); got != "fs+bash+tasks" {
		t.Fatalf("toolSummary(with tasks) = %q, want fs+bash+tasks", got)
	}
}

// TestStatusBarRecomputesOnlyWhenTheBranchChanges guards the memo: the editor
// asks for a status on every repaint, and the estimate it needs walks the whole
// branch.
func TestStatusBarRecomputesOnlyWhenTheBranchChanges(t *testing.T) {
	application, err := app.New(app.Config{
		Model:    wire.Model{ID: "m1", Provider: "test", ContextWindow: 100000},
		Provider: cancelled{},
	}, inMemory(t))
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	bar := newStatusBar(application, nil, "/tmp/project", "fs+bash")

	if got := bar.line(200); !strings.HasSuffix(got, "ctx 100k left") {
		t.Fatalf("fresh line = %q, want the whole window free", got)
	}
	if tokens, window := bar.usage(application.Model()); tokens != 0 || window != 100000 {
		t.Fatalf("usage = (%d, %d), want the cached (0, 100000)", tokens, window)
	}

	if _, err := application.Session().AppendMessage(wire.UserMessage(strings.Repeat("x", 40000))); err != nil {
		t.Fatalf("append: %v", err)
	}
	if got := bar.line(200); !strings.HasSuffix(got, "ctx 90k left") {
		t.Fatalf("line after the append = %q, want 10k of the window spent", got)
	}
}
