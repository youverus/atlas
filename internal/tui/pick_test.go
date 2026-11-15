package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// This file is the search half of the menu: what the query does to the rows, to
// the window, and to the keys. The rest of the list — the marker, the columns,
// the axis — is in list_test.go, and the keys that predate the query are in
// tui_test.go. The two menus are checked together throughout, because the
// search is one behaviour that two commands happen to open.

// menuRows builds rows from "label|note" pairs. The note is where the words a
// reader remembers usually live (a session's preview, a model's provider), so
// most of these tests search the column that is not the name.
func menuRows(pairs ...string) []Choice {
	rows := make([]Choice, len(pairs))
	for i, pair := range pairs {
		label, note, _ := strings.Cut(pair, "|")
		rows[i] = Choice{Value: label, Label: label, Note: note}
	}
	return rows
}

// pickRowsMenu is pickAxisMenu without an axis: the shape /resume has.
func pickRowsMenu(title, current string, rows []Choice) PickMsg {
	return PickMsg{
		Request: PickRequest{Title: title, Items: rows, Current: current},
		Reply:   make(chan PickAnswer, 1),
	}
}

// openMenu puts one list on screen.
func openMenu(t *testing.T, msg PickMsg) Model {
	t.Helper()
	m := sized(t, New(Config{Styles: testStyles(t)}))
	updated, _ := m.Update(msg)
	return updated.(Model)
}

// typeQuery presses one key per character, the way a reader types rather than
// the way a paste arrives.
func typeQuery(t *testing.T, m Model, query string) Model {
	t.Helper()
	for _, r := range query {
		updated, _ := m.Update(typeText(string(r)))
		m = updated.(Model)
	}
	return m
}

// TestQueryNarrowsBothMenus is the whole point of A5: the filter has to work on
// the model menu and the session menu alike, because it is one list drawn twice.
// The query is what the reader would actually type — a name for a model, a word
// remembered from a message for a session.
func TestQueryNarrowsBothMenus(t *testing.T) {
	cases := []struct {
		name  string
		msg   PickMsg
		query string
		kept  string
		gone  []string
	}{
		{
			name: "model",
			msg: pickAxisMenu("thinking", "model", "", menuRows(
				"sonnet-4.5|anthropic · ctx 200000",
				"opus-4|anthropic · ctx 200000",
				"haiku-3|anthropic · ctx 200000",
			)),
			query: "sonnet",
			kept:  "sonnet-4.5",
			gone:  []string{"opus-4", "haiku-3"},
		},
		{
			name: "resume",
			msg: pickRowsMenu("resume", "", menuRows(
				"fix the flaky test|2m ago · 12 messages · the retry test hangs",
				"wire the sandbox|1h ago · 8 messages · seatbelt profile",
			)),
			// A word from the preview, not from the name: what a reader who
			// half-remembers a conversation actually has to go on.
			query: "sandbox",
			kept:  "wire the sandbox",
			gone:  []string{"fix the flaky test"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := typeQuery(t, openMenu(t, tc.msg), tc.query)
			frame := plain(m.View().Content)
			if !strings.Contains(frame, tc.kept) {
				t.Fatalf("the query %q dropped the row that matches it:\n%s", tc.query, frame)
			}
			for _, gone := range tc.gone {
				if strings.Contains(frame, gone) {
					t.Fatalf("the query %q kept %q:\n%s", tc.query, gone, frame)
				}
			}
			if !strings.Contains(frame, "matching \""+tc.query+"\"") {
				t.Fatalf("the title does not say what it was narrowed by:\n%s", frame)
			}
		})
	}
}

// TestQueryKeepsTheKeysOnTheirRow: the query narrows the list under the
// highlight instead of restarting the choice. The row the menu opened on is
// searched for, and it is deliberately not the first of the rows that match.
func TestQueryKeepsTheKeysOnTheirRow(t *testing.T) {
	msg := pickRowsMenu("model", "opus-4", menuRows(
		"sonnet-4.5|anthropic",
		"opus-mini|anthropic",
		"opus-4|anthropic",
	))
	m := typeQuery(t, openMenu(t, msg), "opus")

	if got := len(m.matches); got != 2 {
		t.Fatalf("the query left %d rows, want 2", got)
	}
	if got := m.chooser.Request.Items[m.matches[m.chosen]].Value; got != "opus-4" {
		t.Fatalf("the keys moved to %q, want the row the menu opened on", got)
	}
	if frame := plain(m.View().Content); !strings.Contains(frame, "▸ opus-4") {
		t.Fatalf("the highlight is not drawn on the row it stayed on:\n%s", frame)
	}
}

// TestQueryWindowFollowsTheChosenRow: a filter that leaves more rows than the
// box holds still scrolls, and the title keeps saying where in the matches the
// window is.
func TestQueryWindowFollowsTheChosenRow(t *testing.T) {
	labels := make([]string, 12)
	for i := range labels {
		labels[i] = "row-" + string(rune('a'+i)) + "|same note"
	}
	m := typeQuery(t, openMenu(t, pickRowsMenu("resume", "", menuRows(labels...))), "row")

	if frame := plain(m.View().Content); !strings.Contains(frame, "12 of 12 matching") {
		t.Fatalf("twelve matches are not reported as twelve:\n%s", frame)
	}
	updated, _ := m.Update(press(tea.KeyEnd, 0))
	m = updated.(Model)
	if m.chosen != 11 {
		t.Fatalf("End landed on %d, want the last match", m.chosen)
	}
	frame := plain(m.View().Content)
	if !strings.Contains(frame, "▸ row-l") {
		t.Fatalf("the window did not slide to the last match:\n%s", frame)
	}
	if !strings.Contains(frame, "3–12 of 12") {
		t.Fatalf("the title does not say where the window is over the matches:\n%s", frame)
	}
	if strings.Contains(frame, "row-a") {
		t.Fatalf("a row outside the window is still drawn:\n%s", frame)
	}
}

// TestQueryWithNoMatchSaysSo is the third completion criterion: a query that
// matched nothing must read as "nothing matched", not as a panel with no rows in
// it. The keys that need a row are not advertised either, and Enter does not
// answer a list with nothing to take.
func TestQueryWithNoMatchSaysSo(t *testing.T) {
	msg := pickRowsMenu("model", "", menuRows("sonnet-4.5|anthropic", "opus-4|anthropic"))
	m := typeQuery(t, openMenu(t, msg), "zzz")

	frame := plain(m.View().Content)
	for _, want := range []string{`nothing matches "zzz"`, "Backspace widens it", "Esc leaves"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("the empty list is missing %q:\n%s", want, frame)
		}
	}
	for _, gone := range []string{"↑↓ choose", "Enter takes", "▸", "sonnet-4.5"} {
		if strings.Contains(frame, gone) {
			t.Fatalf("the empty list still offers %q:\n%s", gone, frame)
		}
	}

	// The keys that act on a row do nothing rather than acting on an index that
	// is not there, and Enter in particular must not close the list: that would
	// answer "no row" to a reader who asked for one.
	for _, key := range []rune{tea.KeyDown, tea.KeyUp, tea.KeyEnd, tea.KeyHome, tea.KeyPgDown, tea.KeyEnter} {
		updated, _ := m.Update(press(key, 0))
		m = updated.(Model)
		if m.chooser == nil {
			t.Fatalf("%q closed the list with nothing to take", key)
		}
	}
	select {
	case answer := <-msg.Reply:
		t.Fatalf("a key answered the empty list: %+v", answer)
	default:
	}
}

// TestQueryMatchesEveryWordIgnoringCase: a row is found by any of its words in
// any case, and two words narrow more than either of them alone. Both columns
// are searched, so a name and a fact can be combined.
func TestQueryMatchesEveryWordIgnoringCase(t *testing.T) {
	msg := pickRowsMenu("resume", "", menuRows(
		"fix the flaky test|2m ago · 12 messages · the retry test hangs",
		"wire the sandbox|1h ago · 8 messages · seatbelt profile",
	))
	m := typeQuery(t, openMenu(t, msg), "RETRY flaky")

	if got := len(m.matches); got != 1 {
		t.Fatalf("two words in different cases left %d rows, want 1", got)
	}
	frame := plain(m.View().Content)
	if !strings.Contains(frame, "fix the flaky test") {
		t.Fatalf("the row that has both words was dropped:\n%s", frame)
	}
	if strings.Contains(frame, "wire the sandbox") {
		t.Fatalf("a row with neither word survived:\n%s", frame)
	}
}

// TestQueryDoesNotReadTheValue is the promise pick.go makes about Choice.Value:
// the interface places a label and a note and never looks inside the value. A
// session's value is a path, so reading it would make the project's own
// directory name match every session recorded in it.
func TestQueryDoesNotReadTheValue(t *testing.T) {
	rows := []Choice{
		{Value: "/home/ana/.atlas/projects/vibe/2026-11-14-a", Label: "fix the flaky test", Note: "2m ago"},
		{Value: "/home/ana/.atlas/projects/vibe/2026-11-14-b", Label: "wire the sandbox", Note: "1h ago"},
	}
	m := typeQuery(t, openMenu(t, pickRowsMenu("resume", "", rows)), "vibe")

	if got := len(m.matches); got != 0 {
		t.Fatalf("a word from the value matched %d rows, want 0", got)
	}
	if frame := plain(m.View().Content); !strings.Contains(frame, `nothing matches "vibe"`) {
		t.Fatalf("the value was searched after all:\n%s", frame)
	}
}

// TestBackspaceWidensTheQueryAndKeepsTheList: Backspace is a text key now. It
// gives back one character at a time, puts the rows back as it goes, and at the
// start of the query it stops rather than closing the list — a reader who held
// it down past the beginning would otherwise lose the menu they were reading.
func TestBackspaceWidensTheQueryAndKeepsTheList(t *testing.T) {
	msg := pickRowsMenu("model", "", menuRows("sonnet-4.5|anthropic", "opus-4|anthropic", "haiku-3|anthropic"))
	m := typeQuery(t, openMenu(t, msg), "sonnet")
	if got := len(m.matches); got != 1 {
		t.Fatalf("the query left %d rows, want 1", got)
	}

	back := func(m Model) Model {
		updated, _ := m.Update(press(tea.KeyBackspace, 0))
		return updated.(Model)
	}
	for i := 0; i < len("sonne"); i++ {
		m = back(m)
	}
	if m.query != "s" {
		t.Fatalf("six backspaces left %q, want %q", m.query, "s")
	}
	m = back(m)
	if m.query != "" {
		t.Fatalf("the query did not empty: %q", m.query)
	}
	if got := len(m.matches); got != 3 {
		t.Fatalf("an empty query left %d rows, want all 3", got)
	}
	if frame := plain(m.View().Content); !strings.Contains(frame, "haiku-3") {
		t.Fatalf("a row the query dropped did not come back:\n%s", frame)
	}

	// One press too many, on an empty query: the list stays up.
	m = back(m)
	if m.chooser == nil {
		t.Fatal("Backspace at the start of the query closed the list")
	}
	select {
	case answer := <-msg.Reply:
		t.Fatalf("Backspace answered the list: %+v", answer)
	default:
	}
}

// TestTakingAFilteredRowAnswersWithItsOwnStep is the index mapping, which is the
// one place the query could go wrong quietly: the box draws the matches in the
// host's order, so every row it draws and every row a key acts on has to be
// translated back. A filtered index used as a host index would answer the first
// model on screen with the second model's gear.
func TestTakingAFilteredRowAnswersWithItsOwnStep(t *testing.T) {
	msg := pickAxisMenu("thinking", "model", "", []Choice{
		{Value: "sonnet", Label: "sonnet", Note: "anthropic", Steps: []string{"off", "low", "high"}, Step: 0},
		{Value: "opus", Label: "opus", Note: "anthropic", Steps: []string{"off", "high"}, Step: 0},
	})
	m := typeQuery(t, openMenu(t, msg), "opus")
	updated, _ := m.Update(press(tea.KeyRight, 0))
	m = updated.(Model)
	updated, _ = m.Update(press(tea.KeyEnter, 0))
	m = updated.(Model)

	select {
	case answer := <-msg.Reply:
		if !answer.Taken || answer.Value != "opus" || answer.Step != 1 {
			t.Fatalf("answer = %+v, want opus on step 1", answer)
		}
	default:
		t.Fatal("Enter closed the list without answering")
	}
	if m.chooser != nil {
		t.Fatal("the list is still up after it was answered")
	}
}

// TestTheNextListOpensUnfiltered: the query belongs to the menu that is up.
func TestTheNextListOpensUnfiltered(t *testing.T) {
	m := typeQuery(t, openMenu(t, pickRowsMenu("model", "", menuRows("sonnet|a", "opus|a"))), "sonnet")
	updated, _ := m.Update(press(tea.KeyEnter, 0))
	m = updated.(Model)
	if m.query != "" {
		t.Fatalf("the query outlived the list it was typed into: %q", m.query)
	}

	updated, _ = m.Update(pickRowsMenu("resume", "", menuRows("first|a", "second|a")))
	m = updated.(Model)
	if m.query != "" || len(m.matches) != 2 {
		t.Fatalf("the second list opened with query %q and %d rows", m.query, len(m.matches))
	}
	frame := plain(m.View().Content)
	for _, want := range []string{"first", "second"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("the second list is missing %q:\n%s", want, frame)
		}
	}
}
