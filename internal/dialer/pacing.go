// Package dialer is the outbound calling engine: it keeps each running
// campaign's hopper full, decides how many lines to open, places the calls, and
// connects the people who answer to the agents who are free.
//
// See docs/VICIDIAL_PARITY.md §3.2 for the design. The short version: agents
// wait in an ARI holding bridge with their leg already up, and an answered
// customer is *moved into* that bridge — which is why a predictive connect has
// no ring on the agent side.
package dialer

import "math"

// Pace is the input to one pacing decision: what the campaign asked for, and
// what is actually happening right now.
type Pace struct {
	Method string // MANUAL | PREVIEW | RATIO | ADAPT_AVERAGE | ADAPT_HARD_LIMIT | ADAPT_TAPERED

	DialLevel   float64 // configured lines per available agent
	AdaptiveMax float64 // ceiling the adaptive methods may raise DialLevel to

	AvailableAgents int // ready, not on a call, not in wrap-up
	LinesInFlight   int // calls already ringing but not yet connected

	// Measured over the campaign's recent automatic calls.
	AnswerRate  float64 // answered / placed, 0..1
	DropRate    float64 // dropped / answered, as a percentage
	DropCeiling float64 // the campaign's regulatory limit, as a percentage
	SampleSize  int     // how many calls the two rates were measured over
}

// MinSample is how many recent calls the pacing rules need before they trust a
// measured rate. Below it, a single unlucky call would swing the dial level
// wildly — or trip the brake on noise — so the configured level is used as-is.
//
// Exported so the console reports the brake on exactly the engine's terms
// instead of keeping its own copy of the threshold.
const MinSample = 20

// minSample is the internal spelling used throughout this file.
const minSample = MinSample

// LinesToOpen decides how many new calls to place on this tick.
//
// The rules, in the order they bind:
//
//  1. Agent-driven methods never place calls; that is what makes them
//     agent-driven.
//  2. No available agent means no calls. A call placed with nobody to hand it
//     to can only end in an abandoned call.
//  3. The drop rate is a hard brake. At or above the campaign's ceiling the
//     engine stops opening lines entirely until the measured rate recovers —
//     the ceiling is a legal limit, not a target to oscillate around.
//  4. Otherwise: target = agents × level, adjusted per method, minus what is
//     already in flight.
//
// It never returns more than one line per available agent per tick, so a burst
// cannot outrun the drop-rate feedback that is measured from completed calls.
func LinesToOpen(p Pace) int {
	if !Automatic(p.Method) {
		return 0
	}
	if p.AvailableAgents <= 0 {
		return 0
	}
	// Rule 3: the brake. Applied before any pacing arithmetic so no method can
	// reason its way past it.
	if p.DropCeiling > 0 && p.SampleSize >= minSample && p.DropRate >= p.DropCeiling {
		return 0
	}

	level := effectiveLevel(p)
	target := float64(p.AvailableAgents) * level

	open := int(math.Round(target)) - p.LinesInFlight
	if open <= 0 {
		return 0
	}
	// Never open more than one extra line per agent in a single tick.
	if open > p.AvailableAgents {
		open = p.AvailableAgents
	}
	return open
}

// effectiveLevel is the lines-per-agent this tick, after the method's own
// adjustment. The configured DialLevel is the floor for the adaptive methods
// and the exact value for RATIO.
func effectiveLevel(p Pace) float64 {
	level := p.DialLevel
	if level <= 0 {
		level = 1
	}
	max := p.AdaptiveMax
	if max < level {
		max = level
	}

	switch p.Method {
	case "RATIO":
		// Fixed: dial exactly what was configured, whatever the answer rate.
		return level

	case "ADAPT_AVERAGE", "ADAPT_HARD_LIMIT", "ADAPT_TAPERED":
		if p.SampleSize < minSample || p.AnswerRate <= 0 {
			return level
		}
		// The point of adaptive dialing: if only a third of calls are answered,
		// three lines per agent produce one conversation per agent. Derive the
		// level from the measured answer rate rather than guessing.
		want := 1 / p.AnswerRate
		if want < level {
			want = level
		}

		if p.Method == "ADAPT_TAPERED" {
			// Taper back as the drop rate approaches the ceiling, instead of
			// running flat out until the brake slams on. Below half the
			// ceiling, no taper; at the ceiling, back to the configured level.
			if p.DropCeiling > 0 && p.DropRate > p.DropCeiling/2 {
				span := p.DropCeiling / 2
				over := p.DropRate - span
				factor := 1 - (over / span) // 1 at half the ceiling, 0 at it
				if factor < 0 {
					factor = 0
				}
				want = level + (want-level)*factor
			}
		}

		if want > max {
			want = max
		}
		return want
	}
	return level
}

// Automatic reports whether a dial method places calls without an agent asking.
// MANUAL and PREVIEW are agent-driven and the engine leaves them alone.
func Automatic(method string) bool {
	switch method {
	case "RATIO", "ADAPT_AVERAGE", "ADAPT_HARD_LIMIT", "ADAPT_TAPERED":
		return true
	default:
		return false
	}
}

// DropRatePercent computes the abandoned-call rate the governor reads back:
// answered calls with no agent to give them to, over all answered calls.
//
// The denominator is answered calls, not placed calls — a number that rang out
// was never a person to abandon, and including those would flatter the rate
// exactly where it matters most.
func DropRatePercent(answered, dropped int) float64 {
	if answered <= 0 {
		return 0
	}
	return float64(dropped) / float64(answered) * 100
}
