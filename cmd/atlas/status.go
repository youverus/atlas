package main

import (
	"os"
	"strconv"
	"strings"
	"sync"

	"charm.land/lipgloss/v2"

	"pi-go/internal/app"
	"pi-go/internal/mcp"
	"pi-go/internal/tool"
	"pi-go/internal/wire"
)

// The footer under the prompt is one line of facts about the session, always
// the same six in the same order, so the eye finds the field it wants without
// reading the line:
//
//	~/proj/pi-agent-go | #01a16da2 | fs+bash | deepseek-chat | think high | ctx 188k left
//
// A side conversation adds a leading "side", which is the one field that is not
// about the session but about which conversation the prompt is typing into:
//
//	side | ~/proj/pi-agent-go | #01a16da2 | fs+bash | deepseek-chat | think high | ctx 188k left
//
// An archived session adds "archived" in the same place, for the same reason:
//
//	archived | ~/proj/pi-agent-go | #01a16da2 | fs+bash · Ask | deepseek-chat | think high | ctx 188k left
//
// The order is the order a session is set up in: where it runs, which
// conversation it is, what it may touch, who answers, how hard it is being
// asked to think, and how much room is left. Nothing here is styled: the
// editor dims the whole line, and an escape sequence in the text would be
// counted as cells by the width arithmetic.
const statusSeparator = " | "

// statusInfo is one frame of the footer.
type statusInfo struct {
	side     string
	archived string
	dir      string
	branch   string
	session  string
	grants   string
	mode     string
	model    string
	thinking string
	context  string
}

// line composes the footer for width cells. Fields are shortened and then
// dropped in a fixed order instead of being cut off at the right edge: a line
// that ends in an ellipsis hides the field that was added last, and that field
// is how much context is left, which is the one worth keeping on a narrow
// terminal.
//
// The ladder follows one rule: what describes the request — who answers, how
// much room is left — outlives what describes the place — where it runs, which
// conversation, what it may touch. The thinking level is the one request fact
// that does not follow the rule: it names how hard the next request is being
// asked to think, and the session name — which conversation the prompt belongs
// to — is the field worth keeping when the line runs out of room, so the level
// is the first field to go. Dropping it whole is safe: "high" on its own says
// nothing once the model beside it is gone, and a footer that never prints it
// says nothing false. The side marker sits in every frame for the opposite
// reason: it is empty in the main conversation, so it costs nothing, and a
// footer that dropped it would describe the main conversation while the user
// types into the side one.
//
// The mode is the one place fact that does not follow the rule, because it is
// not one: "fs+bash" says what could run, "Ask" says what happens when it tries,
// and a reader who cannot see the second has no way to know that the next call
// is going to stop and ask. So the two are separate fields here even though the
// widest frame shows them joined — the tool summary leaves at the fifth frame
// and the mode stays until only the context reading is left. A footer that
// dropped both at once is how a session came to look like it had no permission
// mode at all.
func (s statusInfo) line(width int) string {
	dirs := s.dirFrames()
	models := [...]string{s.model, shortModel(s.model)}
	frames := [...][]string{
		{s.side, s.archived, dirs[0], s.session, s.grants, models[0], s.thinking, s.context},
		{s.side, s.archived, dirs[0], s.session, s.grants, models[1], s.thinking, s.context},
		{s.side, s.archived, dirs[1], s.session, s.grants, models[1], s.thinking, s.context},
		// The thinking level is the first field to go: it names how hard the
		// next request is being asked to think, and the session name — which
		// conversation the prompt belongs to — is the one that must stay when
		// the line runs out of room.
		{s.side, s.archived, dirs[1], s.session, s.grants, models[1], s.context},
		// The tool summary goes and the mode takes its place, which is the
		// whole point of holding them apart.
		{s.side, s.archived, dirs[1], s.session, s.mode, models[1], s.context},
		{s.side, s.archived, dirs[2], s.session, s.mode, models[1], s.context},
		{s.side, s.archived, s.session, s.mode, models[1], s.context},
		{s.side, s.archived, s.mode, models[1], s.context},
		{s.side, s.archived, s.mode, s.context},
	}
	for _, fields := range frames {
		if line := joinFields(fields); lipgloss.Width(line) <= width {
			return line
		}
	}
	// Below the narrowest frame the markers go too, but the reading and the
	// mode stay: they are the two facts a terminal this small can still act on.
	return joinFields([]string{s.mode, s.context})
}

// dirFrames is the directory field at its three lengths.
//
// The branch is appended after the path has been shortened, not before, because
// a branch name is one name that happens to contain a slash: "personal/dfbuddy"
// is a branch, not a segment of a path. Shortening the joined string would cut
// it at the slash and leave the bracket dangling after a name that no longer
// exists:
//
//	…/dfbuddy)
//
// Appending here means every frame either shows the whole branch or, further
// down the ladder, drops the directory field altogether. The cost is that a long
// branch makes the narrowest frame longer and the field leaves sooner; that is
// the right trade, because a footer is allowed to say less, not to say something
// false.
func (s statusInfo) dirFrames() [3]string {
	suffix := branchSuffix(s.branch)
	return [3]string{
		s.dir + suffix,
		shortenPath(s.dir, 2) + suffix,
		shortenPath(s.dir, 1) + suffix,
	}
}

// branchSuffix is the branch as it rides beside the directory: in brackets, one
// space before them. The two answer one question — where the session runs — and
// a field of its own would cost a separator on the frames least able to afford
// one. It is glued to the last path segment on purpose: shortening the directory
// to "…/proj" leaves "…/proj (main)", which says the same thing about a narrower
// place.
func branchSuffix(branch string) string {
	if branch == "" {
		return ""
	}
	return " (" + branch + ")"
}

// joinFields renders the non-empty fields as one line.
func joinFields(fields []string) string {
	parts := make([]string, 0, len(fields))
	for _, field := range fields {
		if field != "" {
			parts = append(parts, field)
		}
	}
	return strings.Join(parts, statusSeparator)
}

// formatTokens shortens a token count the way pi's footer does, so a number
// read in atlas and the same number read in pi look alike.
func formatTokens(count int) string {
	switch {
	case count < 1000:
		return strconv.Itoa(count)
	case count < 10000:
		return strconv.FormatFloat(float64(count)/1000, 'f', 1, 64) + "k"
	case count < 1000000:
		return strconv.Itoa((count+500)/1000) + "k"
	case count < 10000000:
		return strconv.FormatFloat(float64(count)/1000000, 'f', 1, 64) + "M"
	default:
		return strconv.Itoa((count+500000)/1000000) + "M"
	}
}

// contextField reports the room left in the model's window. A config that
// declares no window cannot be measured against, so the used count is shown
// instead of a remaining one that would be made up.
func contextField(tokens, window int) string {
	if window <= 0 {
		return "ctx " + formatTokens(tokens) + " used"
	}
	left := window - tokens
	if left < 0 {
		// An estimate can overshoot a window that was just filled; "-12k left"
		// reads as a bug rather than as a full context.
		left = 0
	}
	return "ctx " + formatTokens(left) + " left"
}

// sessionField names the session. Until /rename or the derived name gives it
// one, what a session has is its id; eight characters tell two sessions apart
// and match what the banner prints.
//
// A session with nothing in it has no field at all. Opening atlas does not start
// a conversation: the screen is the home page until the first message, and an id
// there would name a conversation that has taken no word yet — and, worse, a
// file that does not exist. The field appears with the first entry, which is the
// same moment the record does.
func sessionField(name, id string, started bool) string {
	if !started {
		return ""
	}
	if name != "" {
		return name
	}
	if len(id) > 8 {
		id = id[:8]
	}
	if id == "" {
		return ""
	}
	return "#" + id
}

// thinkingField reports the effort the next request carries. Off and a model
// that cannot think both leave the field out: there is no level to name, and
// "think off" would spend the most cells on a line that has none to spare.
func thinkingField(model wire.Model, level string) string {
	if !sendsThinking(model.Reasoning, level) {
		return ""
	}
	return "think " + level
}

// modelField names the model for a person: the display name the config gives
// it, or the "provider/id" spelling the endpoint is called under when the
// descriptor carries no display name. Every surface that says which model
// answers reads it from here, so the footer, the home screen and the /model
// list cannot disagree about the same model.
func modelField(model wire.Model) string {
	if model.Name != "" {
		return model.Name
	}
	return model.Provider + "/" + model.ID
}

// shortModel drops the provider from a "provider/model" name. The model is the
// part a person recognizes, and on a narrow terminal the provider is the first
// thing that can go.
func shortModel(model string) string {
	if _, id, ok := strings.Cut(model, "/"); ok {
		return id
	}
	return model
}

// shortenPath keeps the last segments of a path and marks the cut with an
// ellipsis. The tail is the part that identifies a project; the head is a home
// directory name nobody reads twice.
func shortenPath(path string, keep int) string {
	parts := strings.Split(path, "/")
	// A leading "/" produces an empty first element, which is the root and not
	// a segment.
	if len(parts) > 0 && parts[0] == "" {
		parts = parts[1:]
	}
	if len(parts) <= keep {
		return path
	}
	return "…/" + strings.Join(parts[len(parts)-keep:], "/")
}

// shortHome rewrites a path under home as "~/", which is pi's footer rule and
// the difference between a footer that fits and one that says nothing but
// "/Users/someone".
func shortHome(path, home string) string {
	if home == "" || path == "" {
		return path
	}
	home = strings.TrimSuffix(home, "/")
	if path == home {
		return "~"
	}
	if rest, ok := strings.CutPrefix(path, home+"/"); ok {
		return "~/" + rest
	}
	return path
}

// toolGroupOf maps a tool to the name the footer shows it under. Tools that
// share a package share a name because that is what a person granted: nobody
// decides to allow ls but not grep.
func toolGroupOf(name string) string {
	switch name {
	case "read", "write", "edit", "ls", "find", "grep":
		return "fs"
	case "bash":
		return "bash"
	case "deepseek_web_search", "deepseek_web_fetch", "websearch", "webfetch":
		return "web"
	case "git_status", "git_diff", "git_log", "git_show":
		// One grant, like the web pair: nobody decides to allow a diff but not a
		// log. They are also all read-only, which is why the side conversation
		// runs them without asking.
		return "git"
	case "task_query", "task_output", "task_stop":
		// One grant for the same reason, and the reason is worth stating here
		// because the three arrived later and were, for one version, named one
		// per column: a person allows "look at the background tasks", not one
		// of the three verbs.
		return "tasks"
	}
	if strings.HasPrefix(name, "browser_") {
		return "browser"
	}
	// A remote tool is grouped by its server: the grant a reader made was "this
	// server", not "this one of its forty tools", and listing them one per
	// column would push the rest of the footer off the screen.
	if rest, ok := strings.CutPrefix(name, mcp.ToolPrefix); ok {
		if server, _, ok := strings.Cut(rest, "__"); ok {
			return "mcp:" + server
		}
		return "mcp"
	}
	// A tool outside the known groups is named as itself. The footer is a
	// summary of what the agent may do, so hiding a capability would be worse
	// than a longer line.
	return name
}

// toolSummary names the tool groups in the order the session holds them. It is
// derived from the tool set rather than from a config switch, because the set is
// the grant. The sandbox is not named here for the same reason: it narrows what a
// tool may do rather than adding one, and a footer that listed it beside the tools
// would read as a capability instead of a limit.
func toolSummary(set []tool.Tool) string {
	seen := make(map[string]bool, len(set))
	groups := make([]string, 0, 2)
	for _, t := range set {
		group := toolGroupOf(t.Name())
		if seen[group] {
			continue
		}
		seen[group] = true
		groups = append(groups, group)
	}
	return strings.Join(groups, "+")
}

// statusBar builds the footer for one session. It memoizes the context
// estimate: the editor asks for a status on every repaint, and that estimate
// walks the whole branch.
type statusBar struct {
	application *app.App
	side        *sideState
	dir         string
	grants      string
	// cwd is the directory dir was shortened from, kept because the branch is
	// read from the repository there rather than from the shortened name.
	cwd string
	// git remembers the branch between frames. It is a pointer because the bar
	// is copied into the frames that read it, and a cache inside a copy would
	// be a lookup per frame.
	git *gitRef

	mu     sync.Mutex
	cached bool
	key    usageKey
	tokens int
	window int
}

// usageKey is what the estimate depends on: the branch it walks and the window
// it is measured against.
type usageKey struct {
	session  string
	entries  int
	provider string
	model    string
}

func newStatusBar(application *app.App, side *sideState, cwd, grants string) *statusBar {
	home, _ := os.UserHomeDir()
	return &statusBar{
		application: application,
		side:        side,
		dir:         shortHome(cwd, home),
		grants:      grants,
		cwd:         cwd,
		git:         &gitRef{},
	}
}

// grantsField is what the session may touch, and how a call is approved. The two
// travel on one field because they answer one question — what will happen when a
// tool is called — and the mode is appended rather than placed elsewhere because
// the tool summary is only meaningful next to it: "fs+bash" says what could run,
// "Ask" says what happens when it tries.
//
// The mode is read here rather than stored because /permission can change it
// between two frames, and a footer that needed a restart to tell the truth would
// be the wrong place to look.
func (s *statusBar) grantsField() string {
	mode := s.modeField()
	switch {
	case s.grants == "":
		return mode
	case mode == "":
		return s.grants
	}
	return s.grants + " · " + mode
}

// modeField is the half of grantsField that a narrow terminal keeps. It travels
// separately as well as joined because the ladder above drops fields one at a
// time, and the two have different lifetimes: the summary is a place fact and
// the mode is the answer to "will this call stop and ask".
func (s *statusBar) modeField() string {
	if s.side == nil {
		return ""
	}
	return permissionModeLabel(s.side.permissionMode())
}

// line is the editor's Options.Status.
func (s *statusBar) line(width int) string {
	manager := s.application.Session()
	model := s.application.Model()
	tokens, window := s.usage(model)
	return statusInfo{
		side:     sideField(s.side),
		archived: archivedField(s.application.Archived()),
		dir:      s.dir,
		branch:   s.branch(),
		session:  sessionField(s.application.Name(), manager.ID(), manager.EntryCount() > 0),
		grants:   s.grantsField(),
		mode:     s.modeField(),
		model:    modelField(model),
		thinking: thinkingField(model, s.application.ThinkingLevel()),
		context:  contextField(tokens, window),
	}.line(width)
}

// sideField names the conversation the prompt is typing into. It is empty in
// the main one, so it costs nothing until it matters.
func sideField(state *sideState) string {
	if state == nil || !state.typingInSide() {
		return ""
	}
	return "side"
}

// archivedField marks the session the prompt cannot write to. Same reason as
// the side marker: it is empty in every other session, so keeping it in every
// frame costs nothing, and a footer that dropped it would describe a session
// that takes prompts while the one on screen refuses them.
func archivedField(archived bool) string {
	if !archived {
		return ""
	}
	return "archived"
}

// usage returns the branch size and the window it is measured against,
// recomputing only when one of the inputs changed.
func (s *statusBar) usage(model wire.Model) (tokens, window int) {
	manager := s.application.Session()
	key := usageKey{
		session:  manager.ID(),
		entries:  manager.EntryCount(),
		provider: model.Provider,
		model:    model.ID,
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cached && s.key == key {
		return s.tokens, s.window
	}
	s.tokens, s.window = s.application.ContextUsage()
	s.key, s.cached = key, true
	return s.tokens, s.window
}
