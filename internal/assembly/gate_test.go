package assembly

import (
	"testing"

	"pi-go/internal/tool"
)

// asker counts the questions and answers them from a script. It is the stand-in
// for the modal: what matters here is which answer the gate takes, not how the
// box draws it (that is internal/tui's own test).
type asker struct {
	reply  Answer
	asked  []Ask
	notes  []string
	prompt func(Ask) Answer
}

func (a *asker) ask(req Ask) Answer {
	a.asked = append(a.asked, req)
	if a.prompt != nil {
		return a.prompt(req)
	}
	return a.reply
}

func (a *asker) note(text string) { a.notes = append(a.notes, text) }

// approver is the gate the questions go through, with the session fixed so a
// test can describe a conversation change as a value.
func approver(t *testing.T, session string, a *asker) func(tool.Call) bool {
	t.Helper()
	gate := NewGate(nil)
	gate.SetAsk(a.ask, a.note)
	return gate.approveFunc(a.ask, func() string { return session })
}

func call(name string) tool.Call {
	return tool.Call{ID: name + "-1", Name: name, Arguments: []byte(`{}`)}
}

// TestTheStandingAnswerSparesTheNextQuestion is the whole point of the third key:
// the same tool in the same session asks once. The note is the one trace left of
// a permission that no longer draws a box.
func TestTheStandingAnswerSparesTheNextQuestion(t *testing.T) {
	a := &asker{reply: Answer{Allowed: true, Always: true}}
	approve := approver(t, "s1", a)

	for i := 0; i < 3; i++ {
		if !approve(call("bash")) {
			t.Fatalf("call %d was refused", i)
		}
	}
	if len(a.asked) != 1 {
		t.Fatalf("asked %d times, want 1", len(a.asked))
	}
	if len(a.notes) != 1 || a.notes[0] != "bash: allowed for the rest of this session" {
		t.Fatalf("notes = %v", a.notes)
	}
	// The question named the tool it would cover, which is what lets the box
	// write the scope into its footer.
	if a.asked[0].Tool != "bash" || a.asked[0].Subject == "" {
		t.Fatalf("request = %+v", a.asked[0])
	}
}

// The standing answer covers one tool. A reader who said yes to running the test
// suite did not say yes to editing a file.
func TestTheStandingAnswerIsForOneTool(t *testing.T) {
	a := &asker{prompt: func(req Ask) Answer {
		if req.Tool == "bash" {
			return Answer{Allowed: true, Always: true}
		}
		return Answer{Allowed: true}
	}}
	approve := approver(t, "s1", a)

	approve(call("bash"))
	approve(call("bash"))
	approve(call("write"))
	approve(call("write"))

	if len(a.asked) != 3 {
		t.Fatalf("asked %d times, want 3 (one bash, two writes)", len(a.asked))
	}
}

// The standing answer ends with the session. A permission is an answer about a
// conversation — the files in it, what was said in it — and the next one is a
// different conversation.
func TestTheStandingAnswerEndsWithTheSession(t *testing.T) {
	a := &asker{reply: Answer{Allowed: true, Always: true}}
	gate := NewGate(nil)
	gate.SetAsk(a.ask, a.note)

	first := gate.approveFunc(a.ask, func() string { return "s1" })
	first(call("bash"))
	first(call("bash"))

	second := gate.approveFunc(a.ask, func() string { return "s2" })
	second(call("bash"))

	if len(a.asked) != 2 {
		t.Fatalf("asked %d times, want 2: the second session asked again", len(a.asked))
	}
	// And going back is not a way to get the old answer: the set belongs to
	// whichever session asked last, so "s1" starts over too.
	first(call("bash"))
	if len(a.asked) != 3 {
		t.Fatalf("asked %d times, want 3: the first session kept a stale permission", len(a.asked))
	}
}

// A plain yes is a yes this once. It must not become standing by accident: that
// is the difference between a gate and a formality.
func TestAPlainYesDoesNotBecomeStanding(t *testing.T) {
	a := &asker{reply: Answer{Allowed: true}}
	approve := approver(t, "s1", a)

	approve(call("bash"))
	approve(call("bash"))

	if len(a.asked) != 2 {
		t.Fatalf("asked %d times, want 2", len(a.asked))
	}
	if len(a.notes) != 0 {
		t.Fatalf("notes = %v, want none for a one-time yes", a.notes)
	}
}

// A no is a no, and it is not remembered either: the next call asks again, which
// is how a reader changes their mind without restarting anything.
func TestANoIsNotRemembered(t *testing.T) {
	// The standing bit is set as well, which is the shape a mistake in the
	// interface would take: a refusal that still carried "for this session".
	// Recording it would turn one no into a permission for every later call.
	a := &asker{reply: Answer{Always: true}}
	approve := approver(t, "s1", a)

	if approve(call("bash")) {
		t.Fatal("a refusal approved the call")
	}
	a.reply = Answer{Allowed: true}
	if !approve(call("bash")) {
		t.Fatal("the second call was refused by the first answer")
	}
	if len(a.asked) != 2 {
		t.Fatalf("asked %d times, want 2: the refusal became a standing answer", len(a.asked))
	}
	if len(a.notes) != 0 {
		t.Fatalf("notes = %v, want none", a.notes)
	}
}

// The remembered answer is read before the reader is disturbed at all: a screen
// that still asked would make the third key a keystroke that changes nothing.
func TestARememberedAnswerAsksNobody(t *testing.T) {
	a := &asker{reply: Answer{Allowed: true, Always: true}}
	approve := approver(t, "s1", a)
	approve(call("bash"))

	a.prompt = func(Ask) Answer {
		t.Fatal("asked a question that was already answered")
		return Answer{}
	}
	if !approve(call("bash")) {
		t.Fatal("the remembered answer did not approve the call")
	}
}
