package metrics

import "testing"

func TestContinuityClassification(t *testing.T) {
	tr := NewTracker()
	// 0,1,2 normal
	tr.Observe(0x100, 0, true, false, false, false)
	tr.Observe(0x100, 1, true, false, false, false)
	tr.Observe(0x100, 2, true, false, false, false)
	// duplicate of 2
	tr.Observe(0x100, 2, true, false, false, false)
	// jump 2 -> 5 (two packets lost)
	tr.Observe(0x100, 5, true, false, false, false)
	// adaptation field only repeats CC 5 legally
	tr.Observe(0x100, 5, false, true, false, false)
	// payload continues at 6
	tr.Observe(0x100, 6, true, false, false, false)
	// transport error resets judgement
	tr.Observe(0x100, 7, true, false, false, true)
	// the first packet after the error is an unjudgeable gap, not a jump
	if v := tr.Observe(0x100, 12, true, false, false, false); v != VerdictUnjudgeable {
		t.Fatalf("packet after transport error = %v, want unjudgeable", v)
	}

	s, _ := tr.Get(0x100)
	if s.Packets != 9 {
		t.Fatalf("packets = %d, want 9", s.Packets)
	}
	if s.Duplicates != 1 {
		t.Fatalf("duplicates = %d, want 1", s.Duplicates)
	}
	if s.Jumps != 1 {
		t.Fatalf("jumps = %d, want 1", s.Jumps)
	}
	if s.TransportErrors != 1 || s.AFOnly != 1 || s.UnknownGaps != 1 {
		t.Fatalf("unexpected counters: %+v", s)
	}
	if !s.DiscontinuitySeen {
		t.Fatal("discontinuity not flagged")
	}
}

func TestExplicitDiscontinuityReanchors(t *testing.T) {
	tr := NewTracker()
	tr.Observe(0x200, 3, true, false, false, false)
	// PCR discontinuity: the counter may jump arbitrarily without being a
	// loss, but the gap cannot be measured.
	if v := tr.Observe(0x200, 9, true, false, true, false); v != VerdictDiscontinuity {
		t.Fatalf("discontinuity verdict = %v", v)
	}
	// The next packet cannot be judged against the old counter.
	if v := tr.Observe(0x200, 10, true, false, false, false); v != VerdictUnjudgeable {
		t.Fatalf("packet after discontinuity = %v, want unjudgeable", v)
	}
	tr.Observe(0x200, 11, true, false, false, false)
	s, _ := tr.Get(0x200)
	if s.Jumps != 0 || s.Duplicates != 0 {
		t.Fatalf("explicit discontinuity counted as loss: %+v", s)
	}
	if s.UnknownGaps != 1 {
		t.Fatalf("unjudgeable gap = %d, want 1", s.UnknownGaps)
	}
	if !s.DiscontinuitySeen {
		t.Fatal("explicit discontinuity not recorded")
	}
}

func TestCounterWrapAround(t *testing.T) {
	tr := NewTracker()
	for i := 0; i < 40; i++ {
		tr.Observe(0x300, uint8(i&0x0f), true, false, false, false)
	}
	s, _ := tr.Get(0x300)
	if s.Jumps != 0 || s.Duplicates != 0 {
		t.Fatalf("legal wrap counted as anomaly: %+v", s)
	}
}

func TestAFOnlyCounterAdvanceIsJump(t *testing.T) {
	tr := NewTracker()
	tr.Observe(0x400, 1, true, false, false, false)
	tr.Observe(0x400, 2, false, true, false, false) // must not advance
	s, _ := tr.Get(0x400)
	if s.Jumps != 1 {
		t.Fatalf("AF-only CC advance not flagged: %+v", s)
	}
}

func TestTotalsAndPIDOrdering(t *testing.T) {
	tr := NewTracker()
	tr.Observe(0x200, 0, true, false, false, false)
	tr.Observe(0x100, 0, true, false, false, false)
	tr.Observe(0x100, 0, true, false, false, false)
	pids := tr.PIDs()
	if len(pids) != 2 || pids[0] != 0x100 || pids[1] != 0x200 {
		t.Fatalf("PID order = %v", pids)
	}
	tot := tr.Totals()
	if tot.Packets != 3 || tot.Duplicates != 1 {
		t.Fatalf("totals = %+v", tot)
	}
}
