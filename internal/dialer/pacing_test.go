package dialer

import "testing"

// The pacing rules are the part of the dialer that decides how many strangers'
// phones ring, so they are worth testing directly rather than only observing on
// a live switch. Everything here is pure arithmetic over a Pace value.

func TestAgentDrivenMethodsNeverDial(t *testing.T) {
	for _, method := range []string{"MANUAL", "PREVIEW", "INBOUND_MAN", ""} {
		got := LinesToOpen(Pace{Method: method, AvailableAgents: 5, DialLevel: 3})
		if got != 0 {
			t.Errorf("%s: opened %d lines; agent-driven methods must never dial", method, got)
		}
	}
}

func TestNoAgentsNoCalls(t *testing.T) {
	got := LinesToOpen(Pace{Method: "RATIO", AvailableAgents: 0, DialLevel: 3})
	if got != 0 {
		t.Errorf("opened %d lines with no available agent; every one could only be abandoned", got)
	}
}

func TestRatioDialsConfiguredLevel(t *testing.T) {
	// 4 agents at 2.0 lines each = 8 lines, none in flight.
	got := LinesToOpen(Pace{Method: "RATIO", AvailableAgents: 4, DialLevel: 2})
	if got != 4 {
		// Capped at one new line per agent per tick.
		t.Errorf("got %d, want 4 (per-tick cap of one line per agent)", got)
	}
}

func TestLinesInFlightAreSubtracted(t *testing.T) {
	p := Pace{Method: "RATIO", AvailableAgents: 4, DialLevel: 2, LinesInFlight: 6}
	if got := LinesToOpen(p); got != 2 {
		t.Errorf("got %d, want 2 (target 8 minus 6 already ringing)", got)
	}
	p.LinesInFlight = 8
	if got := LinesToOpen(p); got != 0 {
		t.Errorf("got %d, want 0 when the target is already in flight", got)
	}
	p.LinesInFlight = 99
	if got := LinesToOpen(p); got != 0 {
		t.Errorf("got %d, want 0 when more lines are in flight than the target", got)
	}
}

func TestDropCeilingIsAHardBrake(t *testing.T) {
	p := Pace{
		Method: "ADAPT_AVERAGE", AvailableAgents: 10, DialLevel: 1, AdaptiveMax: 5,
		AnswerRate:  0.2, // would otherwise want 5 lines per agent
		DropRate:    3.0,
		DropCeiling: 3.0,
		SampleSize:  100,
	}
	if got := LinesToOpen(p); got != 0 {
		t.Errorf("got %d, want 0: at the drop ceiling the engine must stop dialing", got)
	}

	// Above the ceiling, likewise.
	p.DropRate = 7.5
	if got := LinesToOpen(p); got != 0 {
		t.Errorf("got %d, want 0 above the ceiling", got)
	}

	// Recovered below it, dialing resumes.
	p.DropRate = 1.0
	if got := LinesToOpen(p); got <= 0 {
		t.Errorf("got %d, want > 0 once the drop rate recovers", got)
	}
}

func TestSmallSampleDoesNotTripTheBrake(t *testing.T) {
	// Two calls, one of them dropped, is a 50% drop rate — and means nothing.
	// The brake must not fire on noise, or a campaign could never start.
	p := Pace{
		Method: "RATIO", AvailableAgents: 3, DialLevel: 1,
		DropRate: 50, DropCeiling: 3, SampleSize: 2,
	}
	if got := LinesToOpen(p); got != 3 {
		t.Errorf("got %d, want 3: a 2-call sample must not brake the dialer", got)
	}
}

func TestAdaptiveRaisesLevelFromAnswerRate(t *testing.T) {
	// One in four answers: to keep 4 agents busy the engine should want about
	// four lines each, capped by AdaptiveMax.
	p := Pace{
		Method: "ADAPT_AVERAGE", AvailableAgents: 4, DialLevel: 1, AdaptiveMax: 4,
		AnswerRate: 0.25, SampleSize: 100, DropCeiling: 3,
	}
	if got := effectiveLevel(p); got != 4 {
		t.Errorf("effectiveLevel = %v, want 4 (1/0.25)", got)
	}

	// AdaptiveMax is a ceiling on that derivation.
	p.AdaptiveMax = 2
	if got := effectiveLevel(p); got != 2 {
		t.Errorf("effectiveLevel = %v, want 2 (capped by AdaptiveMax)", got)
	}
}

func TestAdaptiveNeverGoesBelowConfiguredLevel(t *testing.T) {
	// A high answer rate implies fewer lines than configured; the operator's
	// floor still wins, otherwise "dial 2 lines per agent" would silently
	// become 1.
	p := Pace{
		Method: "ADAPT_AVERAGE", AvailableAgents: 2, DialLevel: 2, AdaptiveMax: 5,
		AnswerRate: 0.9, SampleSize: 100,
	}
	if got := effectiveLevel(p); got != 2 {
		t.Errorf("effectiveLevel = %v, want 2 (the configured floor)", got)
	}
}

func TestAdaptiveIgnoresTinySamples(t *testing.T) {
	p := Pace{
		Method: "ADAPT_AVERAGE", AvailableAgents: 4, DialLevel: 1, AdaptiveMax: 5,
		AnswerRate: 0.1, SampleSize: minSample - 1,
	}
	if got := effectiveLevel(p); got != 1 {
		t.Errorf("effectiveLevel = %v, want 1: too few calls to trust the rate", got)
	}
}

func TestTaperedBacksOffApproachingTheCeiling(t *testing.T) {
	base := Pace{
		Method: "ADAPT_TAPERED", AvailableAgents: 4, DialLevel: 1, AdaptiveMax: 4,
		AnswerRate: 0.25, SampleSize: 100, DropCeiling: 4,
	}

	// Well below half the ceiling: no taper, full derived level.
	base.DropRate = 1
	if got := effectiveLevel(base); got != 4 {
		t.Errorf("effectiveLevel = %v, want 4 with the drop rate well clear", got)
	}

	// Halfway between half-ceiling and ceiling: halfway back to the floor.
	base.DropRate = 3
	if got := effectiveLevel(base); got != 2.5 {
		t.Errorf("effectiveLevel = %v, want 2.5 (halfway taper)", got)
	}

	// At the ceiling the taper is complete — though the brake would have
	// stopped dialing before this is reached.
	base.DropRate = 4
	if got := effectiveLevel(base); got != 1 {
		t.Errorf("effectiveLevel = %v, want 1 (fully tapered to the floor)", got)
	}
}

func TestTaperedIsGentlerThanAverage(t *testing.T) {
	// The same conditions must never make the tapered method dial harder than
	// the plain average one; that is the whole point of the taper.
	p := Pace{
		AvailableAgents: 4, DialLevel: 1, AdaptiveMax: 4,
		AnswerRate: 0.25, SampleSize: 100, DropCeiling: 4, DropRate: 3,
	}
	p.Method = "ADAPT_AVERAGE"
	avg := effectiveLevel(p)
	p.Method = "ADAPT_TAPERED"
	tapered := effectiveLevel(p)
	if tapered > avg {
		t.Errorf("tapered %v > average %v: the taper must not dial harder", tapered, avg)
	}
}

func TestDropRatePercent(t *testing.T) {
	cases := []struct {
		answered, dropped int
		want              float64
	}{
		{0, 0, 0},   // nothing answered yet: not an infinite drop rate
		{100, 3, 3}, // the common regulatory limit
		{50, 0, 0},
		{4, 1, 25},
	}
	for _, c := range cases {
		if got := DropRatePercent(c.answered, c.dropped); got != c.want {
			t.Errorf("DropRatePercent(%d, %d) = %v, want %v", c.answered, c.dropped, got, c.want)
		}
	}
}

func TestDropRateDenominatorIsAnsweredNotPlaced(t *testing.T) {
	// 1000 calls placed, 10 answered, 1 abandoned is a 10% drop rate — not
	// 0.1%. Measuring against placed calls would flatter it by the exact factor
	// that matters, so this guards the denominator directly.
	if got := DropRatePercent(10, 1); got != 10 {
		t.Errorf("got %v, want 10: the denominator must be answered calls", got)
	}
}
