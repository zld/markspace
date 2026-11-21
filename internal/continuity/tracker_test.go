package continuity

import "testing"

func observe(t *Tracker, pid uint16, cc uint8) {
	t.Observe(pid, cc, true, false)
}

func TestInOrderNoGaps(t *testing.T) {
	tr := New()
	for cc := 0; cc < 16; cc++ {
		observe(tr, 256, uint8(cc))
	}
	st := tr.Stats()[0]
	if st.ValidPackets != 16 || st.GapPackets != 0 || st.GapEvents != 0 {
		t.Fatalf("got %+v, want 16 valid and no gaps", st)
	}
}

func TestWrapIsLegal(t *testing.T) {
	tr := New()
	observe(tr, 256, 14)
	observe(tr, 256, 15)
	observe(tr, 256, 0) // 15 -> 0 is a normal +1 wrap
	observe(tr, 256, 1)
	st := tr.Stats()[0]
	if st.GapPackets != 0 || st.GapEvents != 0 {
		t.Fatalf("wrap reported as gap: %+v", st)
	}
}

func TestJumpFiveToEightIsTwoMissing(t *testing.T) {
	tr := New()
	for _, cc := range []uint8{0, 1, 2, 3, 4, 5, 8, 9} {
		observe(tr, 257, cc)
	}
	st := tr.Stats()[0]
	if st.GapPackets != 2 || st.GapEvents != 1 {
		t.Fatalf("got gaps=%d events=%d, want 2 and 1", st.GapPackets, st.GapEvents)
	}
	// Counters after the jump must be tracked normally again.
	if st.LastCounter != 9 || st.ValidPackets != 8 {
		t.Fatalf("resync wrong: %+v", st)
	}
}

func TestDuplicateCounter(t *testing.T) {
	tr := New()
	observe(tr, 256, 3)
	observe(tr, 256, 3) // repeated packet
	observe(tr, 256, 4)
	st := tr.Stats()[0]
	if st.Duplicates != 1 || st.ValidPackets != 2 || st.GapPackets != 0 {
		t.Fatalf("duplicate handling wrong: %+v", st)
	}
}

func TestTransportErrorExcludedFromCC(t *testing.T) {
	tr := New()
	observe(tr, 256, 1)
	tr.Observe(256, 2, true, true) // error packet
	observe(tr, 256, 2)            // same CC as the damaged frame, still in order
	st := tr.Stats()[0]
	if st.ErrorPackets != 1 || st.GapPackets != 0 {
		t.Fatalf("error packet mishandled: %+v", st)
	}
}

func TestStatsSortedByPID(t *testing.T) {
	tr := New()
	observe(tr, 300, 0)
	observe(tr, 256, 0)
	observe(tr, 257, 0)
	got := tr.Stats()
	if got[0].PID != 256 || got[1].PID != 257 || got[2].PID != 300 {
		t.Fatalf("stats not PID-sorted: %v", []uint16{got[0].PID, got[1].PID, got[2].PID})
	}
}
