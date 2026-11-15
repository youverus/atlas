package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"pi-go/internal/app"
	"pi-go/internal/config"
	"pi-go/internal/session"
	"pi-go/internal/tui"
)

const (
	// resumeTrigger is the bare command. An argument addresses a session
	// directly; with none, the command prints the table.
	resumeTrigger = "/resume"

	// resumeAllArg is the wide form's argument; the command path sees it as
	// the argument once the trigger is stripped.
	resumeAllArg = "--all"

	// recoverTrigger is the other half of the same table: the sessions the user
	// archived, which /resume deliberately leaves out.
	recoverTrigger = "/recover"
)

// sessionPicker is the state behind /resume. It lists the sessions of the
// directory the app writes into, and re-reads it every time the menu is asked
// for the trigger line: /new, /fork and a switch all change what is on disk
// while the process runs, so a cached list would outlive its own truth. Lines
// that are not a trigger never load — they just close the menu.
type sessionPicker struct {
	application *app.App
	// home is the atlas home whose sessions/ tree the wide list walks, or ""
	// when the session directory was configured. See allHome.
	home string
	// archived points the same picker at the other half of the directory: the
	// sessions the user put away. The rows, the menu and the switch are the
	// same code, because recovering a session and resuming one differ only in
	// which files they may show.
	archived bool
	infos    []session.Info
}

func newSessionPicker(application *app.App, home string) *sessionPicker {
	return &sessionPicker{application: application, home: home}
}

// newRecoverPicker is the /recover face of the picker. It borrows the wide-list
// home from the resume picker because both commands walk the same store: the
// difference between them is which half of it they show, not where it is.
func newRecoverPicker(resume *sessionPicker) *sessionPicker {
	if resume == nil {
		return nil
	}
	return &sessionPicker{application: resume.application, home: resume.home, archived: true}
}

// allHome is the atlas home the wide list walks, or "" when the session
// directory was configured. A configured directory is one store rather than a
// tree of project buckets, so the widest honest list of it is that directory
// itself; claiming to span projects from there would be a lie. The default is
// taken from config, not from the session manager: the manager reports the
// directory this session lives in, and the point of the wide list is to look
// past it.
func allHome(opts options, cfg *config.Config) string {
	if opts.sessionDir != "" || (cfg != nil && cfg.SessionDir != "") {
		return ""
	}
	home, err := config.DefaultHome()
	if err != nil {
		return ""
	}
	return home
}

// sessionLister answers the sessions one view may show. It is the single place
// that decides what "this project" and "every project" mean, so the table, the
// menu and an id prefix resolved against them cannot disagree about which
// sessions exist.
type sessionLister func() ([]session.Info, error)

// lister is that view. The plain one keeps this project's sessions, the way
// List filters them: the recorded cwd is what tells two projects apart inside
// one directory. The wide one is every bucket under the atlas home, cwd kept on
// the row.
//
// A configured session directory has no buckets to walk, so the widest honest
// list of it is that directory unfiltered.
func (p *sessionPicker) lister(wide bool) sessionLister {
	list := session.List
	if p.archived {
		list = session.ListArchived
	}
	switch {
	case !wide:
		dir, cwd := p.application.Session().Dir(), p.application.Session().Cwd()
		return func() ([]session.Info, error) { return list(dir, cwd) }
	case p.home == "":
		dir := p.application.Session().Dir()
		return func() ([]session.Info, error) { return list(dir, "") }
	default:
		all := session.ListAll
		if p.archived {
			all = session.ListArchivedAll
		}
		return func() ([]session.Info, error) { return all(p.home) }
	}
}

// load lists the sessions the given view may show.
func (p *sessionPicker) load(wide bool) error {
	infos, err := p.lister(wide)()
	if err != nil {
		return err
	}
	p.infos = infos
	return nil
}

// open resolves the argument, swaps the session and reports what is running
// now, with the conversation that came with it. An empty argument opens the
// menu: on a terminal selecting a row is the whole command, and the printed
// table is its fallback.
//
// --all widens the same command to every project, for both halves of the store:
// /resume --all lists the sessions that are live, /recover --all the ones that
// were archived. An id after it is resolved against that wider view, because a
// row a reader can see in a table has to be a row they can name.
func (p *sessionPicker) open(arg string, pick chooser, out io.Writer) error {
	wide, spec := wideArg(arg)
	if spec == "" {
		return p.menu(wide, pick, out)
	}
	path, err := p.resolve(spec, wide)
	if err != nil {
		return err
	}
	return p.switchTo(path, out)
}

// wideArg splits the one flag these two commands have from the session it may
// name. Only an exact "--all", with the argument after it, is the flag: a path
// never looks like that, and folding the search would rewrite what the reader
// typed.
func wideArg(arg string) (bool, string) {
	if arg == resumeAllArg {
		return true, ""
	}
	if rest, ok := strings.CutPrefix(arg, resumeAllArg+" "); ok {
		return true, strings.TrimSpace(rest)
	}
	return false, arg
}

// menu offers the listed sessions as rows to move through, and drops to the
// printed table when there is no menu to draw. Closing the menu changes
// nothing: the reader asked to look at what is recorded.
func (p *sessionPicker) menu(wide bool, pick chooser, out io.Writer) error {
	if pick == nil {
		return p.table(wide, out)
	}
	if err := p.load(wide); err != nil {
		return err
	}
	if len(p.infos) == 0 {
		fmt.Fprintln(out, p.emptyText())
		return nil
	}
	// The session in effect is where the list opens, so that the menu says
	// where the reader is now. A session held in memory has no file, and then
	// the menu opens at the top.
	current := ""
	if p.application.Session().Persisted() {
		current = p.application.Session().File()
	}
	answer := pick(tui.PickRequest{Title: strings.TrimPrefix(p.trigger(), "/"), Items: sessionChoices(p.infos), Current: current})
	if !answer.Taken {
		return nil
	}
	return p.switchTo(answer.Value, out)
}

// switchTo swaps the session in and replays what it holds. It is the tail of
// both forms of the command: the argument form resolved a path, the menu was
// handed one.
func (p *sessionPicker) switchTo(path string, out io.Writer) error {
	before := p.application.Session().Cwd()
	if err := p.application.SwitchSession(path); err != nil {
		return err
	}
	fmt.Fprintf(out, "%s: %s\n", p.verb(), p.currentLabel())
	fmt.Fprintf(out, "file: %s\n", p.application.Session().File())
	// Where the tools now run. It is said only when the session brought a
	// different directory with it: a line that repeats the one the reader
	// started in on every resume of the same project is noise, and the fact
	// that matters is exactly the one that changed.
	if after := p.application.Session().Cwd(); after != before {
		fmt.Fprintf(out, "dir: %s\n", after)
	}
	if note := p.application.RestoreNote(); note != "" {
		fmt.Fprintln(out, "note:", note)
	}
	if p.application.Archived() {
		// The one thing to know about the session that just opened: it takes no
		// prompts. Saying it before the first refusal is cheaper than being
		// told by the error.
		fmt.Fprintln(out, archivedLocked)
	}
	// The conversation itself is not printed here. The screen the command is
	// read on owns what is on it: the full-screen session drops the old
	// transcript and rebuilds it from the same messages, and a second replay
	// from this function would show every turn twice.
	return nil
}

// resolve turns what the user typed into a session file. The view is the one
// the command asked for: an id under /recover finds an archived session and the
// same id under /resume does not, and --all widens both to every project.
func (p *sessionPicker) resolve(spec string, wide bool) (string, error) {
	return resolveIn(spec, p.lister(wide), p.trigger())
}

// resolveResumeTarget maps a /resume argument or -resume flag value to a
// session file. Anything that looks like a path is one — a session id never
// carries a slash — and a path that is not there stays an error instead of a
// fresh session. What is left prefixes one session id recorded for the working
// directory.
func resolveResumeTarget(spec, dir, cwd string) (string, error) {
	return resolveIn(spec, func() ([]session.Info, error) { return session.List(dir, cwd) }, resumeTrigger)
}

// resolveIn is the shared body: a path is taken literally, anything else is an
// id prefix among the sessions the given view lists.
func resolveIn(spec string, list sessionLister, trigger string) (string, error) {
	if strings.ContainsRune(spec, '/') || strings.HasPrefix(spec, "~") {
		expanded, err := config.ExpandHome(spec)
		if err != nil {
			return "", err
		}
		abs, err := filepath.Abs(expanded)
		if err != nil {
			return "", err
		}
		if info, statErr := os.Stat(abs); statErr == nil && !info.IsDir() {
			return abs, nil
		}
		return "", fmt.Errorf("no session file at %s", abs)
	}
	infos, err := list()
	if err != nil {
		return "", err
	}
	var matches []session.Info
	for _, info := range infos {
		if strings.HasPrefix(info.ID, spec) {
			matches = append(matches, info)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0].Path, nil
	case 0:
		return "", fmt.Errorf("no session matches %q; %s lists them", spec, trigger)
	default:
		// The report shows what to type instead: each match's prefix extended
		// until it tells the matches apart. With the plain eight characters a
		// same-millisecond clash would print the same id twice and help
		// nobody.
		ids := make([]string, len(matches))
		for i, info := range matches {
			ids[i] = info.ID
		}
		labels := make([]string, len(matches))
		for i, info := range matches {
			labels[i] = uniqueShortID(info.ID, ids)
		}
		return "", fmt.Errorf("several sessions match %q: %s; type more characters", spec, strings.Join(labels, ", "))
	}
}

// table prints the listed sessions as a table for the piped reader, which has
// no menu. The ids lead the row because they are what the command takes.
func (p *sessionPicker) table(wide bool, out io.Writer) error {
	if err := p.load(wide); err != nil {
		return err
	}
	writeSessionTable(out, p.infos, wide, p.trigger(), p.emptyText())
	return nil
}

// trigger is the command this picker belongs to. Rows are taken, not typed, so
// the only place it shows is the footer that names what to type instead.
func (p *sessionPicker) trigger() string {
	if p.archived {
		return recoverTrigger
	}
	return resumeTrigger
}

// verb is what the switch reports, in the command's own words.
func (p *sessionPicker) verb() string {
	if p.archived {
		return "recovered"
	}
	return "resumed"
}

// emptyText is what an empty list says. "no sessions to resume" under /recover
// would name the wrong command.
func (p *sessionPicker) emptyText() string {
	if p.archived {
		return "nothing archived"
	}
	return "no sessions to resume"
}

// writeSessionTable renders the loaded rows. The wide form adds a cwd column:
// without it the table would look like one unlabelled pile of sessions, and two
// projects would be indistinguishable. The footer names the way back in — a bare
// id prefix only resolves against the view that listed it, so the wide form says
// the flag as well — and, on the wide form, what opening a row does: the tools
// run in that directory, which is the reason the column is there.
//
// The alignment counts cells, not bytes, so a CJK name or a shortened path does
// not push the columns out of line.
func writeSessionTable(out io.Writer, infos []session.Info, wide bool, trigger, empty string) {
	if len(infos) == 0 {
		fmt.Fprintln(out, empty)
		return
	}
	type row struct {
		id, ago, label, count, cwd, first string
	}
	rows := make([]row, len(infos))
	ids := make([]string, len(infos))
	idWidth, agoWidth, labelWidth, countWidth, cwdWidth := 0, 0, 0, 0, 0
	for i, info := range infos {
		ids[i] = info.ID
	}
	for i, info := range infos {
		rows[i] = row{
			id:    uniqueShortID(info.ID, ids),
			ago:   timeAgo(info.Modified),
			label: sessionLabel(info, ids),
			count: countText(info.MessageCount),
			first: preview(info.FirstMessage, 48),
		}
		if wide {
			rows[i].cwd = shortenPath(info.Cwd, 2)
		}
		if n := len(rows[i].id); n > idWidth {
			idWidth = n
		}
		if n := lipgloss.Width(rows[i].ago); n > agoWidth {
			agoWidth = n
		}
		if n := lipgloss.Width(rows[i].label); n > labelWidth {
			labelWidth = n
		}
		if n := lipgloss.Width(rows[i].count); n > countWidth {
			countWidth = n
		}
		if n := lipgloss.Width(rows[i].cwd); n > cwdWidth {
			cwdWidth = n
		}
	}
	for _, r := range rows {
		if wide {
			fmt.Fprintf(out, "%-*s  %-*s  %-*s  %-*s  %-*s  %s\n",
				idWidth, r.id, agoWidth, r.ago, labelWidth, r.label,
				countWidth, r.count, cwdWidth, r.cwd, r.first)
			continue
		}
		fmt.Fprintf(out, "%-*s  %-*s  %-*s  %-*s  %s\n",
			idWidth, r.id, agoWidth, r.ago, labelWidth, r.label, countWidth, r.count, r.first)
	}
	fmt.Fprintln(out)
	if wide {
		fmt.Fprintf(out, "%s one with %s %s <id prefix>; its tools run in the directory on its row\n",
			strings.TrimPrefix(trigger, "/"), trigger, resumeAllArg)
		return
	}
	fmt.Fprintf(out, "%s one with %s <id prefix>\n", strings.TrimPrefix(trigger, "/"), trigger)
}

// sessionChoices is the table as rows. The two forms say the same things about
// the same sessions — a menu that left out the age or the message count would be
// a second, thinner answer to "what is recorded here". The id column goes: a row
// in a menu is taken, not typed.
func sessionChoices(infos []session.Info) []tui.Choice {
	ids := make([]string, len(infos))
	for i, info := range infos {
		ids[i] = info.ID
	}
	out := make([]tui.Choice, 0, len(infos))
	for _, info := range infos {
		note := timeAgo(info.Modified) + " · " + countText(info.MessageCount)
		// An empty session has nothing to preview, and the kernel's placeholder
		// for that would read as a second way of saying the count.
		if info.MessageCount > 0 {
			if first := preview(info.FirstMessage, 44); first != "" {
				note += " · " + first
			}
		}
		out = append(out, tui.Choice{Value: info.Path, Label: sessionLabel(info, ids), Note: note})
	}
	return out
}

// currentLabel names the session in effect, the way a menu row does.
func (p *sessionPicker) currentLabel() string {
	if name := p.application.Name(); name != "" {
		return name
	}
	ids := make([]string, len(p.infos))
	for i, info := range p.infos {
		ids[i] = info.ID
	}
	return "session " + uniqueShortID(p.application.Session().ID(), ids)
}

// sessionLabel names a row: the session's name when it has one, otherwise the
// id prefix the status bar shows.
func sessionLabel(info session.Info, ids []string) string {
	if info.Name != "" {
		return info.Name
	}
	return "session " + uniqueShortID(info.ID, ids)
}

// uniqueShortID is the id prefix a row shows. Eight characters are the status
// bar's form, but two sessions created in the same millisecond share them, and
// two rows that look identical cannot be told apart; the prefix grows until it
// does. A full id is unique by construction, so the growth always ends.
func uniqueShortID(id string, ids []string) string {
	n := 8
	if len(id) < n {
		n = len(id)
	}
	for n < len(id) {
		clash := false
		for _, other := range ids {
			if other != id && strings.HasPrefix(other, id[:n]) {
				clash = true
				break
			}
		}
		if !clash {
			return id[:n]
		}
		n++
	}
	return id[:n]
}

// countText says how many messages a session holds, in the singular when
// there is one.
func countText(n int) string {
	if n == 1 {
		return "1 message"
	}
	return fmt.Sprintf("%d messages", n)
}

// timeAgo renders a last-activity time in the coarse units a row fits. Nothing
// finer than a minute is distinguishable here, and a clock that runs a little
// ahead gets 0m rather than a negative.
func timeAgo(at time.Time) string {
	elapsed := time.Since(at)
	if elapsed < 0 {
		elapsed = 0
	}
	switch {
	case elapsed < time.Hour:
		return fmt.Sprintf("%dm", int(elapsed.Minutes()))
	case elapsed < 24*time.Hour:
		return fmt.Sprintf("%dh", int(elapsed.Hours()))
	default:
		return fmt.Sprintf("%dd", int(elapsed.Hours()/24))
	}
}

// preview flattens a message to one line and cuts it, so a long first prompt
// stays a column and not a paragraph.
func preview(text string, max int) string {
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) > max {
		return string(runes[:max]) + "…"
	}
	return text
}

// resumeArg takes everything after the trigger, blanks and all: a session path
// may contain spaces, and folding them would rewrite a path the user typed.
func resumeArg(line string) string { return commandArg(line, resumeTrigger) }

// commandArg is the argument of a slash command, with its inner blanks kept.
func commandArg(line, trigger string) string {
	return strings.TrimSpace(strings.TrimPrefix(line, trigger))
}
