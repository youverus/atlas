package main

// What one turn cost, in the two units a reader asks about: tokens, and time.
// It lives in the host rather than in the interface because only the host holds
// both ends of the turn — the callback that starts it and the result that ends
// it — and because the interface draws a frame per event and has no business
// keeping a stopwatch of its own.

import (
	"fmt"
	"math"
	"strings"
	"time"

	"pi-go/internal/wire"
)

// turnTimer measures the two halves of a turn's clock: all of it, and the part
// the model spent writing.
//
// The split is what keeps tokens per second honest. A turn that ran six tools
// and wrote for two seconds did not write at output/duration — dividing by the
// whole turn reports a rate that falls whenever a tool is slow, which is a fact
// about the tool and not about the model. The two numbers also check each
// other: with no tool calls they nearly agree, so a rate that disagrees with
// the duration says one of them is being measured wrong.
type turnTimer struct {
	began   time.Time     // when the turn started
	writing time.Time     // when the current assistant message started, zero between them
	wrote   time.Duration // assistant messages that already ended

	// now is a field rather than the package function for the same reason the
	// transcript has one: a test can pin every reading instead of sleeping
	// through the interval it wants to measure.
	now func() time.Time
}

func newTurnTimer() *turnTimer {
	timer := &turnTimer{now: time.Now}
	timer.began = timer.now()
	return timer
}

// observe notes when the model started and stopped writing. Only an assistant
// message is the model talking: the prompt and the tool results arrive as
// messages too, and counting those would fold the tools' time back into the
// rate.
func (t *turnTimer) observe(event wire.AgentEvent) {
	if event.Message == nil || event.Message.Role != wire.RoleAssistant {
		return
	}
	switch event.Type {
	case wire.AgentEventMessageStart:
		t.writing = t.now()
	case wire.AgentEventMessageEnd:
		if !t.writing.IsZero() {
			t.wrote += t.now().Sub(t.writing)
			t.writing = time.Time{}
		}
	}
}

// cost freezes what the timer saw into the numbers the front ends print.
func (t *turnTimer) cost() turnCost {
	ended := t.now()
	wrote := t.wrote
	if !t.writing.IsZero() {
		// The message never ended: the turn was cancelled, or the provider
		// broke mid-answer. That time was still spent writing, and dropping it
		// would divide the tokens by a span shorter than the one that produced
		// them.
		wrote += ended.Sub(t.writing)
	}
	return turnCost{Took: ended.Sub(t.began), Wrote: wrote, Ended: ended}
}

// turnCost is one turn's receipt. It is a value, not the timer, so both front
// ends can print it after the turn is over without reaching back into a running
// clock.
type turnCost struct {
	Took  time.Duration // wall clock, tools included
	Wrote time.Duration // the part of it the model spent writing
	Ended time.Time
}

// fields are the parts of the line, in reading order. Both front ends print the
// same parts in the same order; only the separator between them differs, and it
// differs because the two transcripts already read differently.
func (c turnCost) fields(usage wire.Usage) []string {
	if usage.TotalTokens == 0 {
		// Nothing was spent, so there is no cost to report, and the reader is
		// left with the one word that explains the turn. A rejected request
		// does have a duration, but measuring a rejection tells nobody
		// anything they wanted to know.
		return nil
	}
	fields := make([]string, 0, 4)
	fields = append(fields, fmt.Sprintf("%d tokens (in %d, out %d)", usage.TotalTokens, usage.Input, usage.Output))
	if rate, ok := c.rate(usage.Output); ok {
		fields = append(fields, rate)
	}
	// "took" rather than "completed in": a turn that failed or was cancelled
	// prints this line too, and it did take that long.
	return append(fields, "took "+humanDuration(c.Took), "ended at "+c.Ended.Format("15:04:05"))
}

// line is the full-screen session's notice. It joins with the same dot the
// transcript already uses between a tool's outcome and its duration.
func (c turnCost) line(usage wire.Usage) string {
	return strings.Join(c.fields(usage), " · ")
}

// rate is output tokens per second of writing, in the precision the number
// deserves: whole numbers past ten, one decimal below it, where a tenth is the
// difference between seeing a slow model and a stalled one.
//
// It is omitted when the model wrote nothing, because "0 tok/s" under an empty
// answer reads as a failure rather than as a turn that had no words in it.
func (c turnCost) rate(output int) (string, bool) {
	if output <= 0 || c.Wrote <= 0 {
		return "", false
	}
	value := float64(output) / c.Wrote.Seconds()
	if value >= 10 {
		return fmt.Sprintf("%d tok/s", int(math.Round(value))), true
	}
	return fmt.Sprintf("%.1f tok/s", value), true
}
