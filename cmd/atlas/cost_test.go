package main

// The receipt a turn leaves behind: what it spent, how fast the model wrote,
// how long the turn took, and when it stopped.

import (
	"testing"
	"time"

	"pi-go/internal/wire"
)

// pinnedClock is a clock a test advances by hand, so an interval can be
// measured without waiting for it.
type pinnedClock struct{ at time.Time }

func (c *pinnedClock) now() time.Time { return c.at }

func (c *pinnedClock) advance(by time.Duration) { c.at = c.at.Add(by) }

// pinnedTimer starts a timer at a fixed time with a clock the test controls.
func pinnedTimer(clock *pinnedClock) *turnTimer {
	timer := &turnTimer{now: clock.now}
	timer.began = timer.now()
	return timer
}

func message(kind string, role wire.Role) wire.AgentEvent {
	return wire.AgentEvent{Type: kind, Message: &wire.Message{Role: role}}
}

func TestTheCostLineSaysWhatTheTurnSpent(t *testing.T) {
	cost := turnCost{
		Took:  8400 * time.Millisecond,
		Wrote: 8400 * time.Millisecond,
		Ended: time.Date(2026, 10, 21, 15, 3, 11, 0, time.Local),
	}
	usage := wire.Usage{Input: 1204, Output: 519, TotalTokens: 1723}

	want := "1723 tokens (in 1204, out 519) · 62 tok/s · took 8.4s · ended at 15:03:11"
	if got := cost.line(usage); got != want {
		t.Fatalf("line = %q, want %q", got, want)
	}
}

// TestTheRateIsTheWritingTimeNotTheTurn is the point of measuring twice: a turn
// that spent its time in tools did not write slowly, and dividing by the whole
// turn would report the tools' latency as the model's speed.
func TestTheRateIsTheWritingTimeNotTheTurn(t *testing.T) {
	cost := turnCost{
		Took:  30 * time.Second,
		Wrote: 2 * time.Second,
		Ended: time.Date(2026, 10, 21, 15, 3, 11, 0, time.Local),
	}
	usage := wire.Usage{Input: 1204, Output: 519, TotalTokens: 1723}

	want := "1723 tokens (in 1204, out 519) · 260 tok/s · took 30.0s · ended at 15:03:11"
	if got := cost.line(usage); got != want {
		t.Fatalf("line = %q, want %q", got, want)
	}
}

// TestTheRateKeepsADecimalWhenItIsSmall: below ten, a tenth is the difference
// between a slow model and a stalled one.
func TestTheRateKeepsADecimalWhenItIsSmall(t *testing.T) {
	cost := turnCost{
		Took:  2 * time.Second,
		Wrote: 2 * time.Second,
		Ended: time.Date(2026, 10, 21, 15, 3, 11, 0, time.Local),
	}
	usage := wire.Usage{Input: 40, Output: 8, TotalTokens: 48}

	want := "48 tokens (in 40, out 8) · 4.0 tok/s · took 2.0s · ended at 15:03:11"
	if got := cost.line(usage); got != want {
		t.Fatalf("line = %q, want %q", got, want)
	}
}

// TestNoRateWhenTheModelWroteNothing keeps "0 tok/s" off the screen: under an
// answer with no words in it, a zero rate reads as a failure rather than as a
// turn that had nothing to say.
func TestNoRateWhenTheModelWroteNothing(t *testing.T) {
	cost := turnCost{
		Took:  40 * time.Millisecond,
		Ended: time.Date(2026, 10, 21, 15, 3, 11, 0, time.Local),
	}
	usage := wire.Usage{Input: 1204, TotalTokens: 1204}

	want := "1204 tokens (in 1204, out 0) · took 40ms · ended at 15:03:11"
	if got := cost.line(usage); got != want {
		t.Fatalf("line = %q, want %q", got, want)
	}
}

func TestTheTimerCountsOnlyTheModelWriting(t *testing.T) {
	clock := &pinnedClock{at: time.Date(2026, 10, 21, 15, 3, 3, 0, time.Local)}
	timer := pinnedTimer(clock)

	// The prompt is a message too. Reading it is not the model writing, and a
	// rate that counted it would fall with however long the reader typed.
	timer.observe(message(wire.AgentEventMessageStart, wire.RoleUser))
	clock.advance(100 * time.Millisecond)
	timer.observe(message(wire.AgentEventMessageEnd, wire.RoleUser))
	clock.advance(100 * time.Millisecond)

	// Two seconds of answer...
	timer.observe(message(wire.AgentEventMessageStart, wire.RoleAssistant))
	clock.advance(2 * time.Second)
	timer.observe(message(wire.AgentEventMessageEnd, wire.RoleAssistant))

	// ...and six of tools, which belong to the turn.
	timer.observe(wire.AgentEvent{Type: wire.AgentEventToolExecStart, ToolName: "bash"})
	clock.advance(6 * time.Second)
	timer.observe(wire.AgentEvent{Type: wire.AgentEventToolExecEnd, ToolName: "bash"})

	cost := timer.cost()
	if want := 8200 * time.Millisecond; cost.Took != want {
		t.Fatalf("took = %v, want %v", cost.Took, want)
	}
	if want := 2 * time.Second; cost.Wrote != want {
		t.Fatalf("wrote = %v, want %v", cost.Wrote, want)
	}
	if want := "15:03:11"; cost.Ended.Format("15:04:05") != want {
		t.Fatalf("ended at %q, want %q", cost.Ended.Format("15:04:05"), want)
	}
}

// TestTheTimerKeepsAddingAcrossAnswers: a turn with a tool call has two
// assistant messages, and the rate is about both of them, not the last one.
func TestTheTimerKeepsAddingAcrossAnswers(t *testing.T) {
	clock := &pinnedClock{at: time.Date(2026, 10, 21, 15, 3, 3, 0, time.Local)}
	timer := pinnedTimer(clock)

	for _, pause := range []time.Duration{time.Second, 3 * time.Second} {
		timer.observe(message(wire.AgentEventMessageStart, wire.RoleAssistant))
		clock.advance(pause)
		timer.observe(message(wire.AgentEventMessageEnd, wire.RoleAssistant))
	}

	if want := 4 * time.Second; timer.cost().Wrote != want {
		t.Fatalf("wrote = %v, want %v", timer.cost().Wrote, want)
	}
}

// TestTheTimerCountsAnAnswerThatNeverEnded covers the two ways a message ends
// without its message_end: the reader cancels, or the provider breaks. The time
// was still spent writing, and dropping it would divide the tokens by a span
// shorter than the one that produced them.
func TestTheTimerCountsAnAnswerThatNeverEnded(t *testing.T) {
	clock := &pinnedClock{at: time.Date(2026, 10, 21, 15, 3, 3, 0, time.Local)}
	timer := pinnedTimer(clock)

	timer.observe(message(wire.AgentEventMessageStart, wire.RoleAssistant))
	clock.advance(3 * time.Second)

	cost := timer.cost()
	if want := 3 * time.Second; cost.Wrote != want {
		t.Fatalf("wrote = %v, want %v", cost.Wrote, want)
	}
	if cost.Took != cost.Wrote {
		t.Fatalf("took = %v, wrote = %v: one answer with nothing behind it", cost.Took, cost.Wrote)
	}
}

// TestTheTimerIgnoresEventsWithoutAMessage keeps the switch honest: the turn's
// own start and end carry no message, and a nil dereference there would panic
// in the middle of a conversation rather than in a test.
func TestTheTimerIgnoresEventsWithoutAMessage(t *testing.T) {
	clock := &pinnedClock{at: time.Date(2026, 10, 21, 15, 3, 3, 0, time.Local)}
	timer := pinnedTimer(clock)

	timer.observe(wire.AgentEvent{Type: wire.AgentEventStart})
	timer.observe(wire.AgentEvent{Type: wire.AgentEventMessageStart})
	timer.observe(wire.AgentEvent{Type: wire.AgentEventEnd})
	clock.advance(time.Second)

	cost := timer.cost()
	if cost.Wrote != 0 {
		t.Fatalf("wrote = %v, want nothing", cost.Wrote)
	}
	if want := time.Second; cost.Took != want {
		t.Fatalf("took = %v, want %v", cost.Took, want)
	}
	if line := cost.line(wire.Usage{}); line != "" {
		t.Fatalf("line = %q, want nothing at all", line)
	}
}
