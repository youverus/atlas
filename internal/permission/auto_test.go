package permission

import (
	"context"
	"errors"
	"strings"
	"testing"

	"pi-go/internal/tool"
)

// recorder is a Classifier that answers from a script and remembers what it was
// asked. It is how these tests separate "the ladder asked the classifier" from
// "the ladder asked the user": the second one is the absence of a question here.
type recorder struct {
	verdict Verdict
	err     error
	asked   []ClassifyRequest
}

func (r *recorder) Classify(_ context.Context, request ClassifyRequest) (Verdict, error) {
	r.asked = append(r.asked, request)
	return r.verdict, r.err
}

// declined is an approve function that refuses and counts the questions, so a
// test can tell "the user was asked" from "the classifier answered".
func declined(asked *int) func(tool.Call) bool {
	return func(tool.Call) bool {
		*asked++
		return false
	}
}

// TestOffDisablesTheGate is the whole difference between Off and Full access. Off
// runs no rule and no checker, which is the only reading that makes it a fourth
// mode rather than a second name for Full access: the latter still refuses a
// denied call.
func TestOffDisablesTheGate(t *testing.T) {
	store := mustParse(t, `{
		"version": 2,
		"deny": [{"tool_name": "bash"}]
	}`)
	engine := New(Options{Mode: ModeOff, Store: store})
	if decision := engine.Decide(bashCall("rm -rf /")); decision.Behavior != Allow {
		t.Fatalf("off: got %q (%s), want allow even for a denied call", decision.Behavior, decision.Reason)
	}

	asked := 0
	if blocked, reason := engine.Gate(context.Background(), bashCall("rm -rf /"), declined(&asked)); blocked {
		t.Fatalf("off blocked the call: %s", reason)
	}
	if asked != 0 {
		t.Fatalf("off asked the user %d times, want 0", asked)
	}
}

// TestTheModeCanBeSwitchedAtRuntime is the runtime half of the four modes: the
// same engine, the same call, a different answer after SetMode.
func TestTheModeCanBeSwitchedAtRuntime(t *testing.T) {
	engine := New(Options{Mode: ModeDefault})
	write := namedCall("write")

	if decision := engine.Decide(write); decision.Behavior != Ask {
		t.Fatalf("default: got %q, want ask", decision.Behavior)
	}
	if err := engine.SetMode(ModeBypass); err != nil {
		t.Fatalf("SetMode: %v", err)
	}
	if decision := engine.Decide(write); decision.Behavior != Allow {
		t.Fatalf("bypass: got %q, want allow", decision.Behavior)
	}
	if err := engine.SetMode(ModeOff); err != nil {
		t.Fatalf("SetMode: %v", err)
	}
	if engine.Mode() != ModeOff {
		t.Fatalf("Mode: got %q want %q", engine.Mode(), ModeOff)
	}
}

// TestAnUnknownModeIsRefused: a typo that silently became "ask" would look like
// the switch did nothing, so it is an error instead of a stored value.
func TestAnUnknownModeIsRefused(t *testing.T) {
	engine := New(Options{Mode: ModeDefault})
	if err := engine.SetMode("bypass"); err == nil {
		t.Fatal("an incomplete mode name was accepted")
	}
	if engine.Mode() != ModeDefault {
		t.Fatalf("Mode: got %q, want the previous value", engine.Mode())
	}
	if err := engine.SetMode(""); err != nil {
		t.Fatalf("an empty mode means default: %v", err)
	}
	if engine.Mode() != ModeDefault {
		t.Fatalf("Mode: got %q want %q", engine.Mode(), ModeDefault)
	}
}

// TestBypassStillSitsAfterTheAskRules is the position rule from the reference
// ladder: the bypass branch is below every ask step, so a rule the user wrote
// still gets asked even in Full access.
func TestBypassStillSitsAfterTheAskRules(t *testing.T) {
	store := mustParse(t, `{
		"version": 2,
		"ask": [{"tool_name": "bash"}]
	}`)
	engine := New(Options{Mode: ModeBypass, Store: store})
	if decision := engine.Decide(bashCall("ls")); decision.Behavior != Ask {
		t.Fatalf("bypass with an ask rule: got %q, want ask", decision.Behavior)
	}
	if decision := engine.Decide(namedCall("write")); decision.Behavior != Allow {
		t.Fatalf("bypass with no rule: got %q, want allow", decision.Behavior)
	}
}

// TestAutoClassifiesOnlyTheRuleFreeAsk is the load-bearing test of the mode. A
// rule ask is the user's own sentence and must not be answered by a model; the
// fallback ask is what Auto exists for.
func TestAutoClassifiesOnlyTheRuleFreeAsk(t *testing.T) {
	store := mustParse(t, `{
		"version": 2,
		"ask": [{"tool_name": "bash"}]
	}`)
	classifier := &recorder{verdict: Verdict{Behavior: Allow, Reason: "ordinary work"}}
	engine := New(Options{Mode: ModeAuto, Store: store, Classifier: classifier})

	// A rule ask: the classifier is never consulted, and the user is asked.
	asked := 0
	if blocked, _ := engine.Gate(context.Background(), bashCall("ls"), declined(&asked)); !blocked {
		t.Fatal("a rule ask was allowed")
	}
	if asked != 1 {
		t.Fatalf("the user was asked %d times, want 1", asked)
	}
	if len(classifier.asked) != 0 {
		t.Fatalf("the classifier was asked about a rule ask: %+v", classifier.asked)
	}

	// The fallback ask: Classifiable, so the classifier answers and the user is
	// not disturbed.
	if blocked, reason := engine.Gate(context.Background(), namedCall("write"), declined(&asked)); blocked {
		t.Fatalf("the classifier allowed the write and the gate blocked it: %s", reason)
	}
	if asked != 1 {
		t.Fatalf("the user was asked %d times, want 1: the classifier answered", asked)
	}
	if len(classifier.asked) != 1 {
		t.Fatalf("the classifier was asked %d times, want 1", len(classifier.asked))
	}
	if got := classifier.asked[0]; got.Tool != "write" || got.Reason == "" {
		t.Fatalf("the question did not carry the call: %+v", got)
	}
}

// TestABypassImmuneAskIsNotClassified: an ask that even Full access must not wave
// through cannot be handed to a model either. The two would be the same mistake
// with a different judge.
func TestABypassImmuneAskIsNotClassified(t *testing.T) {
	classifier := &recorder{verdict: Verdict{Behavior: Allow}}
	engine := New(Options{
		Mode:       ModeAuto,
		Classifier: classifier,
		Checkers: map[string]Checker{
			"write": func(tool.Call) (CheckResult, error) {
				return CheckResult{Behavior: Ask, Reason: "writes a credential file", BypassImmune: true}, nil
			},
		},
	})

	asked := 0
	if blocked, _ := engine.Gate(context.Background(), namedCall("write"), declined(&asked)); !blocked {
		t.Fatal("a bypass-immune ask was allowed")
	}
	if len(classifier.asked) != 0 {
		t.Fatalf("the classifier was asked about a bypass-immune check: %+v", classifier.asked)
	}
	if asked != 1 {
		t.Fatalf("the user was asked %d times, want 1", asked)
	}
}

// TestTheClassifierDenyStopsTheCall: auto's other answer. The model refuses, the
// call does not run, and the reason reaches the model as a decision rather than a
// failure so it stops instead of retrying.
func TestTheClassifierDenyStopsTheCall(t *testing.T) {
	classifier := &recorder{verdict: Verdict{Behavior: Deny, Reason: "this formats the volume"}}
	engine := New(Options{Mode: ModeAuto, Classifier: classifier})

	asked := 0
	blocked, reason := engine.Gate(context.Background(), namedCall("bash"), declined(&asked))
	if !blocked {
		t.Fatal("a classifier deny allowed the call")
	}
	if asked != 0 {
		t.Fatalf("the user was asked %d times after the classifier decided", asked)
	}
	if !strings.Contains(reason, "formats the volume") || !strings.Contains(reason, "Do not retry") {
		t.Fatalf("reason = %q, want the verdict and the instruction", reason)
	}
}

// TestAFailedClassifierAsksTheHuman is the fail-closed contract, from every angle
// a classifier can fail: no classifier, an error, a verdict that is neither allow
// nor deny, and a mode that was never auto to begin with.
func TestAFailedClassifierAsksTheHuman(t *testing.T) {
	for _, tc := range []struct {
		name       string
		mode       string
		classifier Classifier
	}{
		{name: "no classifier", mode: ModeAuto},
		{name: "error", mode: ModeAuto, classifier: &recorder{err: errors.New("upstream is down")}},
		{name: "undecided", mode: ModeAuto, classifier: &recorder{verdict: Verdict{Behavior: Undecided}}},
		{name: "ask", mode: ModeAuto, classifier: &recorder{verdict: Verdict{Behavior: Ask}}},
		{name: "default mode", mode: ModeDefault, classifier: &recorder{verdict: Verdict{Behavior: Allow}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine := New(Options{Mode: tc.mode, Classifier: tc.classifier})
			asked := 0
			if blocked, _ := engine.Gate(context.Background(), namedCall("write"), declined(&asked)); !blocked {
				t.Fatal("the call ran without a verdict")
			}
			if asked != 1 {
				t.Fatalf("the user was asked %d times, want 1", asked)
			}
		})
	}
}

// TestAutoCannotCrossADenyRule: the classifier is an exit for an ask, never an
// override. A denied call is decided before any ask exists.
func TestAutoCannotCrossADenyRule(t *testing.T) {
	store := mustParse(t, `{
		"version": 2,
		"deny": [{"tool_name": "bash"}]
	}`)
	classifier := &recorder{verdict: Verdict{Behavior: Allow}}
	engine := New(Options{Mode: ModeAuto, Store: store, Classifier: classifier})

	asked := 0
	blocked, reason := engine.Gate(context.Background(), bashCall("ls"), declined(&asked))
	if !blocked {
		t.Fatal("auto crossed a deny rule")
	}
	if len(classifier.asked) != 0 {
		t.Fatalf("the classifier was asked about a denied call: %+v", classifier.asked)
	}
	if !strings.Contains(reason, "denied by rule") {
		t.Fatalf("reason = %q, want the rule", reason)
	}
}

// TestTheClassifierIsNotConsultedWhenTheAnswerIsAlreadyAllow: allow and deny are
// settled before the exit, so auto costs a request only when it has to.
func TestTheClassifierIsNotConsultedWhenTheAnswerIsAlreadyAllow(t *testing.T) {
	classifier := &recorder{verdict: Verdict{Behavior: Deny}}
	engine := New(Options{Mode: ModeAuto, Classifier: classifier})

	asked := 0
	if blocked, reason := engine.Gate(context.Background(), namedCall("read"), declined(&asked)); blocked {
		t.Fatalf("a tool with no checker was blocked: %s", reason)
	}
	if len(classifier.asked) != 0 {
		t.Fatalf("the classifier was asked about a call nobody was asking about: %+v", classifier.asked)
	}
	if asked != 0 {
		t.Fatalf("the user was asked %d times, want 0", asked)
	}
}
