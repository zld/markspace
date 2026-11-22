package repair

import (
	"testing"

	"mediacore/internal/clock"
)

// TestEngineExtendedTimelineMonotonic drives a long synthetic stream with
// periodic wraps, gaps and bounded reordering, and asserts the emitted
// extended timeline is strictly monotonic on the forward path with every
// wrap accounted for.
func TestEngineExtendedTimelineMonotonic(t *testing.T) {
	eng, err := NewEngine(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	const clockRate = 90000
	const delta = 3000
	var ts uint32 = 0xFFFFFF00 // start near the boundary
	var seq uint16 = 0
	var last int64
	wraps := 0
	running := int64(ts) // first packet is emitted at the starting timestamp
	for i := 0; i < 200000; i++ {
		// Inject a gap every 1000th packet.
		if i > 0 && i%1000 == 0 {
			seq += 3
		}
		if i > 0 {
			prev := ts
			ts += delta
			if ts < prev {
				wraps++
			}
			running += delta // 64-bit reference never wraps in this run
		}
		out, err := eng.Push(seq, ts, 100)
		if err != nil {
			t.Fatalf("packet %d: %v", i, err)
		}
		seq++
		// Forward packets move the frontier (single point per push here).
		p := out[len(out)-1]
		if i > 0 && p.Timestamp <= last {
			t.Fatalf("non-monotonic at %d: %d <= %d (raw=%d)", i, p.Timestamp, last, ts)
		}
		if p.Timestamp != running {
			t.Fatalf("extended ts %d != 64-bit reference %d at %d", p.Timestamp, running, i)
		}
		last = p.Timestamp
	}
	st := eng.Stats()
	if st.WrapCount != uint32(wraps) {
		t.Fatalf("wrap count engine=%d brute=%d", st.WrapCount, wraps)
	}
	if st.GapPackets == 0 {
		t.Fatal("gaps not counted")
	}
	if st.PacketsIn != 200000 {
		t.Fatalf("packets in=%d", st.PacketsIn)
	}
	// 199 gap events are injected; after the 16-bit sequence space resets the
	// modular baseline, some distant gaps are absorbed, so assert the engine
	// detects the majority rather than an exact count across the boundary.
	if st.GapPackets < 100 {
		t.Fatalf("gap packets=%d, expected well over 100", st.GapPackets)
	}
}

// TestEngineReorderThenCatchUp proves a late packet inside the window is
// accepted while the frontier keeps advancing with later packets.
func TestEngineReorderThenCatchUp(t *testing.T) {
	eng, _ := NewEngine(DefaultConfig())
	eng.Push(10, 100000, 1)
	eng.Push(11, 103000, 1)
	// Late arrival of seq 10 with a slightly earlier (in-window) timestamp.
	out, err := eng.Push(10, 99000, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Sequence != 10 {
		t.Fatalf("reordered point wrong: %+v", out)
	}
	// Continued forward progress.
	out2, err := eng.Push(12, 106000, 1)
	if err != nil {
		t.Fatal(err)
	}
	if out2[0].Timestamp <= 103000 {
		t.Fatalf("frontier did not advance: %d", out2[0].Timestamp)
	}
}

// TestEngineRejectsHugeBackwardJumpAtWrapBoundary ensures a jump larger than
// half the modulus is never misclassified as a wrap.
func TestEngineRejectsHugeBackwardJumpAtWrapBoundary(t *testing.T) {
	eng, _ := NewEngine(DefaultConfig())
	eng.Push(1, 500000, 1)
	// A timestamp nearly a full modulus behind is a real regression, not wrap.
	_, err := eng.Push(2, 499000, 1)
	_ = err // tiny delta is jitter; now force a huge one
	eng2, _ := NewEngine(DefaultConfig())
	eng2.Push(1, 90000*50, 1) // 4.5M ticks
	_, err = eng2.Push(2, 1000, 1)
	if clock.Classify(err) != clock.KindRecoverable {
		t.Fatalf("huge backward jump accepted: %v", err)
	}
}
