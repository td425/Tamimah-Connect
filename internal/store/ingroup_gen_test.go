package store

import (
	"strings"
	"testing"
)

// The dialplan generator is worth testing directly: the bug this phase exists
// to fix was a generated dialplan that looked right and silently produced no
// call-center reporting. A wrong line here is invisible until a live call.

func TestIngroupCompilesToQueue(t *testing.T) {
	got := ingroupLines("sales")
	want := []string{"Answer()", "Queue(SALES,tTn,,,)", "Hangup()"}
	if len(got) != len(want) {
		t.Fatalf("got %d lines %v, want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

func TestIngroupUsesAppQueueNotDial(t *testing.T) {
	// This is the whole point of the phase. app_queue is the only thing that
	// writes queue_log, and queue_log is what the dashboard reads; a generated
	// Dial() loop reports nothing.
	joined := strings.Join(ingroupLines("SUPPORT"), " ")
	if !strings.Contains(joined, "Queue(") {
		t.Errorf("in-group must compile to Queue(): %q", joined)
	}
	if strings.Contains(joined, "Dial(") || strings.Contains(joined, "While(") {
		t.Errorf("in-group must not fall back to a ring loop: %q", joined)
	}
}

func TestIngroupWaitLimitAndGiveUpDestination(t *testing.T) {
	got := ingroupLines("SUPPORT;120;voicemail:2000")
	if got[1] != "Queue(SUPPORT,tTn,,,120)" {
		t.Errorf("queue line: got %q", got[1])
	}
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "VoiceMail(2000@default,u)") {
		t.Errorf("give-up destination missing: %q", joined)
	}
}

func TestIngroupZeroWaitMeansNoLimit(t *testing.T) {
	// "0" must render as an empty timeout argument, not a literal 0, which
	// app_queue would read as "give up immediately".
	if got := ingroupLines("SALES;0"); got[1] != "Queue(SALES,tTn,,,)" {
		t.Errorf("got %q, want an empty timeout argument", got[1])
	}
}

func TestIngroupHangupGiveUpAddsNoExtraSteps(t *testing.T) {
	got := ingroupLines("SALES;60;hangup")
	if len(got) != 3 || got[2] != "Hangup()" {
		t.Errorf("got %v, want a bare hangup after the queue", got)
	}
}

func TestEmptyIngroupIsAHangupNotAMalformedLine(t *testing.T) {
	for _, in := range []string{"", "   ", ";60"} {
		got := ingroupLines(in)
		if len(got) != 1 || got[0] != "Hangup()" {
			t.Errorf("ingroupLines(%q) = %v, want a single Hangup()", in, got)
		}
	}
}

func TestIngroupIsAKnownDestType(t *testing.T) {
	if destType("ingroup") != "ingroup" {
		t.Error("ingroup must survive destType, or the UI cannot save one")
	}
}

func TestLegacyQueueStillRingsAGroup(t *testing.T) {
	// The old ring-group action is kept for deployments already using it. It
	// must keep working — it simply produces no ACD reporting.
	joined := strings.Join(queueDialLines("1001&1002"), " ")
	if !strings.Contains(joined, "Dial(PJSIP/1001&PJSIP/1002") {
		t.Errorf("legacy ring group changed behaviour: %q", joined)
	}
	if strings.Contains(joined, "Queue(") {
		t.Errorf("legacy ring group must not silently become a queue: %q", joined)
	}
}
