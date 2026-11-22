package ingest

import "testing"

// TestReorderMemoryBoundedByWindow verifies that a long well-ordered stream
// does not accumulate pending packets: the window bounds live state.
func TestReorderMemoryBoundedByWindow(t *testing.T) {
	r := NewReorder(ReorderConfig{Window: 16})
	for i := 0; i < 100000; i++ {
		r.Add(mkPacket(uint16(i), uint32(i)*3000, 64))
	}
	for id, st := range r.streams {
		if len(st.pending) > 16 {
			t.Fatalf("stream %d kept %d pending packets, window is 16", id, len(st.pending))
		}
		// Delivered packets are retained for the result, but gap history must
		// stay bounded (no gaps here).
		if len(st.gaps) != 0 {
			t.Fatalf("unexpected gaps: %d", len(st.gaps))
		}
	}
}

// TestReorderGapHistoryBounded ensures declared gaps are trimmed so a stream
// with many holes cannot grow the late-detection structure without limit.
func TestReorderGapHistoryBounded(t *testing.T) {
	cfg := ReorderConfig{Window: 2, LateHistory: 4}
	r := NewReorder(cfg)
	// Every other packet missing -> a gap whenever the window advances.
	for i := uint16(0); i < 4000; i += 2 {
		r.Add(mkPacket(i, uint32(i), 8))
	}
	st := r.streams[7]
	if len(st.gaps) > cfg.LateHistory {
		t.Fatalf("gap history=%d exceeds limit %d", len(st.gaps), cfg.LateHistory)
	}
}
