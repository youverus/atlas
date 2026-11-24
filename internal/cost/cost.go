// Package cost measures what one turn spent, in the two units a reader asks
// about: tokens, and time.
//
// It lives in the host rather than in an interface because only the host holds
// both ends of the turn — the callback that starts it and the result that ends
// it — and because a window draws a frame per event and has no business keeping
// a stopwatch of its own. The terminal and the desktop shell therefore print the
// same numbers, in the same words, from this one implementation.
package cost

import (
	"fmt"
	"math"
	"strings"
	"time"

	"pi-go/internal/wire"
)

// Timer measures the two halves of a turn's clock: all of it, and the part the
// model spent writing.
//
// The split is what keeps tokens per second honest. A turn that ran six tools
// and wrote for two seconds did not write at output/duration — dividing by the
// whole turn reports a rate that falls whenever a tool is slow, which is a fact
// about the tool and not about the model. The two numbers also check each
// other: with no tool calls they nearly agree, so a rate that disagrees with
// the duration says one of them is being measured wrong.
type Timer struct {
	began   time.Time     // when the turn started
	writing time.Time     // when the current assistant message started, zero between them
	wrote   time.Duration // assistant messages that already ended

	// now is a field rather than the package function for the same reason the
	// transcript has one: a test can pin every reading instead of sleeping
	// through the interval it wants to measure.
	now func() time.Time
}

// NewTimer starts a turn's clock at the moment it is called.
func NewTimer() *Timer {
	timer := &Timer{now: time.Now}
	timer.began = timer.now()
	return timer
}

// Observe notes when the model started and stopped writing. Only an assistant
// message is the model talking: the prompt and the tool results arrive as
// messages too, and counting those would fold the tools' time back into the
// rate.
func (t *Timer) Observe(event wire.AgentEvent) {
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

// Cost freezes what the timer saw into the numbers the front ends print.
func (t *Timer) Cost() Cost {
	ended := t.now()
	wrote := t.wrote
	if !t.writing.IsZero() {
		// The message never ended: the turn was cancelled, or the provider
		// broke mid-answer. That time was still spent writing, and dropping it
		// would divide the tokens by a span shorter than the one that produced
		// them.
		wrote += ended.Sub(t.writing)
	}
	return Cost{Took: ended.Sub(t.began), Wrote: wrote, Ended: ended}
}

// Cost is one turn's receipt. It is a value, not the timer, so every front end
// can print it after the turn is over without reaching back into a running
// clock.
type Cost struct {
	Took  time.Duration // wall clock, tools included
	Wrote time.Duration // the part of it the model spent writing
	Ended time.Time
}

// Fields are the parts of the line, in reading order. Every front end prints
// the same parts in the same order; only the separator between them differs,
// and it differs because the transcripts already read differently.
func (c Cost) Fields(usage wire.Usage) []string {
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
	return append(fields, "took "+HumanDuration(c.Took), "ended at "+c.Ended.Format("15:04:05"))
}

// Line is the full-screen session's notice. It joins with the same dot the
// transcript already uses between a tool's outcome and its duration.
func (c Cost) Line(usage wire.Usage) string {
	return strings.Join(c.Fields(usage), " · ")
}

// rate is output tokens per second of writing, in the precision the number
// deserves: whole numbers past ten, one decimal below it, where a tenth is the
// difference between seeing a slow model and a stalled one.
//
// It is omitted when the model wrote nothing, because "0 tok/s" under an empty
// answer reads as a failure rather than as a turn that had no words in it.
// TokensPerSec reports output tokens per second of writing, or 0 when
// unavailable.
func (c Cost) TokensPerSec(output int) float64 {
	if output <= 0 || c.Wrote <= 0 {
		return 0
	}
	return float64(output) / c.Wrote.Seconds()
}

func (c Cost) rate(output int) (string, bool) {
	value := c.TokensPerSec(output)
	if value <= 0 {
		return "", false
	}
	if value >= 10 {
		return fmt.Sprintf("%d tok/s", int(math.Round(value))), true
	}
	return fmt.Sprintf("%.1f tok/s", value), true
}

// HumanDuration is how long something took, in the unit a human would say it
// in: milliseconds under a second, tenths under a minute, and a clock reading
// past that. The receipt and the tool cards print the same spans, so the words
// live here instead of once per front end.
func HumanDuration(duration time.Duration) string {
	switch {
	case duration < time.Second:
		return fmt.Sprintf("%dms", duration.Milliseconds())
	case duration < time.Minute:
		return fmt.Sprintf("%.1fs", duration.Seconds())
	}
	seconds := int(duration.Seconds())
	return fmt.Sprintf("%dm%02ds", seconds/60, seconds%60)
}
