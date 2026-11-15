package catalog

// Level is one thinking gear. The names are the ones the rest of the tree
// already uses (config.ThinkingLevels and the copy in internal/rpc). This
// package repeats the vocabulary rather than importing it, so that the table
// stays a leaf that can be tested with no config file and so that a future
// rename has exactly one more place to change, visibly.
type Level string

const (
	// LevelOff is a switch rather than an effort: it says "do not think", which
	// every model has to be able to hear.
	LevelOff    Level = "off"
	LevelLow    Level = "low"
	LevelMedium Level = "medium"
	LevelHigh   Level = "high"
	LevelXHigh  Level = "xhigh"
	LevelMax    Level = "max"
	LevelUltra  Level = "ultra"
)

// Levels lists every gear, cheapest first. The order is the canonical one: it
// decides what Gears returns, and it is the tie-breaker in Clamp.
var Levels = []Level{LevelOff, LevelLow, LevelMedium, LevelHigh, LevelXHigh, LevelMax, LevelUltra}

// rank returns the position of level in Levels, or -1 when it is not a gear
// this build knows. Matching is exact: a vendor's "HIGH" or "high " is a
// different string and is reported as unknown instead of guessed at.
func rank(level Level) int {
	for i, known := range Levels {
		if known == level {
			return i
		}
	}
	return -1
}

// ValidLevel reports whether name is one of the seven gears. It is the check a
// loaded table runs against its own data, and the one /model runs against a
// request.
func ValidLevel(name string) bool {
	return rank(Level(name)) >= 0
}

// Clamp returns the gear in offered closest to want.
//
// Four rules, in order:
//
//   - Off is a switch, so it is never clamped: it comes back untouched even
//     when offered does not list it.
//   - A gear the model offers comes back untouched.
//   - Anything else becomes the nearest gear the model does offer. Two gears
//     can be equally near ("medium" between "low" and "high"); the cheaper one
//     wins, because spending fewer tokens is the smaller mistake.
//   - A name that is not in the vocabulary becomes off. Sending an unrecognised
//     gear would reach the endpoint as whatever the adapter defaults to, which
//     is not what the caller asked for; off is the one answer that cannot
//     surprise anyone.
//
// offered is what the model can express, not what the adapter can send: a
// vendor dialect that collapses several gears onto one effort does that
// collapsing in its own adapter, where the wire format is known.
func Clamp(offered []Level, want Level) Level {
	if want == LevelOff {
		return LevelOff
	}
	wantRank := rank(want)
	if wantRank < 0 {
		return LevelOff
	}
	best := LevelOff
	bestRank := -1
	for _, level := range offered {
		levelRank := rank(level)
		if levelRank < 0 {
			continue
		}
		if bestRank < 0 || closer(levelRank, bestRank, wantRank) {
			best, bestRank = level, levelRank
		}
	}
	if bestRank < 0 {
		// Nothing in offered is a gear: the model cannot think.
		return LevelOff
	}
	return best
}

// closer reports whether candidate is a better answer than best for a caller
// that asked for want, given all three as ranks.
func closer(candidate, best, want int) bool {
	candidateDistance := distance(candidate, want)
	bestDistance := distance(best, want)
	if candidateDistance != bestDistance {
		return candidateDistance < bestDistance
	}
	// Equidistant gears are both expressible; the cheaper one is the smaller
	// mistake. Comparing ranks, not positions in offered, keeps the answer
	// independent of how the caller ordered the slice.
	return candidate < best
}

func distance(a, b int) int {
	if a > b {
		return a - b
	}
	return b - a
}
