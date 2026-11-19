package demux

import (
	"testing"

	"tsinspect/internal/ts"
)

func pkt(pid uint16, cc uint8, payload bool, discont bool) *ts.Packet {
	return &ts.Packet{PID: pid, ContinuityCounter: cc, HasPayload: payload, Discontinuity: discont}
}

func TestCounterNormalSequence(t *testing.T) {
	c := NewCounter()
	for cc := uint8(0); cc < 16; cc++ {
		if issue := c.Observe(pkt(0x100, cc, true, false)); issue != IssueNone {
			t.Fatalf("cc %d: %v", cc, issue)
		}
	}
	// Wrap 15 -> 0 is in sequence.
	if issue := c.Observe(pkt(0x100, 0, true, false)); issue != IssueNone {
		t.Fatalf("wrap: %v", issue)
	}
	if c.Totals().ContinuityErrs != 0 {
		t.Fatal("false continuity error")
	}
}

func TestCounterAdaptationOnlyIgnored(t *testing.T) {
	c := NewCounter()
	c.Observe(pkt(0x100, 4, true, false))
	// Adaptation-only packet carries no payload: no counter advance.
	if issue := c.Observe(pkt(0x100, 9, false, false)); issue != IssueNone {
		t.Fatalf("adaptation-only reported %v", issue)
	}
	if issue := c.Observe(pkt(0x100, 5, true, false)); issue != IssueNone {
		t.Fatalf("next payload reported %v", issue)
	}
	st, _ := c.PID(0x100)
	if st.AdaptationOnly != 1 || st.ContinuityErrs != 0 {
		t.Fatalf("stat = %+v", st)
	}
}

func TestCounterJumpAndDuplicates(t *testing.T) {
	c := NewCounter()
	c.Observe(pkt(0x100, 1, true, false))
	if issue := c.Observe(pkt(0x100, 4, true, false)); issue != IssueCCJump {
		t.Fatalf("jump: %v", issue)
	}
	if issue := c.Observe(pkt(0x100, 4, true, false)); issue != IssueDuplicate {
		t.Fatalf("duplicate: %v", issue)
	}
	st, _ := c.PID(0x100)
	if st.ContinuityErrs != 1 || st.Duplicates != 1 || st.LostPackets != 2 {
		t.Fatalf("stat = %+v", st)
	}
}

func TestCounterDiscontinuityIndicator(t *testing.T) {
	c := NewCounter()
	c.Observe(pkt(0x100, 1, true, false))
	if issue := c.Observe(pkt(0x100, 8, true, true)); issue != IssueDiscontinuity {
		t.Fatalf("discontinuity: %v", issue)
	}
	if issue := c.Observe(pkt(0x100, 9, true, false)); issue != IssueNone {
		t.Fatalf("post-discontinuity: %v", issue)
	}
	st, _ := c.PID(0x100)
	if st.ContinuityErrs != 0 || st.Discontinuities != 1 {
		t.Fatalf("stat = %+v", st)
	}
}

func TestCounterTransportErrorBreaksBaseline(t *testing.T) {
	c := NewCounter()
	c.Observe(pkt(0x100, 1, true, false))
	bad := pkt(0x100, 2, true, false)
	bad.TransportError = true
	if issue := c.Observe(bad); issue != IssueTransportError {
		t.Fatalf("transport error: %v", issue)
	}
	if issue := c.Observe(pkt(0x100, 9, true, false)); issue != IssueNone {
		t.Fatalf("first packet after error should resync, got %v", issue)
	}
	st, _ := c.PID(0x100)
	if st.TransportErrors != 1 || st.ContinuityErrs != 0 {
		t.Fatalf("stat = %+v", st)
	}
}

func TestCounterNullPID(t *testing.T) {
	c := NewCounter()
	c.Observe(pkt(NullPID, 0, true, false))
	c.Observe(pkt(NullPID, 0, true, false))
	if st, _ := c.PID(NullPID); st.Duplicates != 1 {
		t.Fatalf("stat = %+v", st)
	}
}
