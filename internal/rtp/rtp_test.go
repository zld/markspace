package rtp

import (
	"testing"

	"mediasync/internal/clock"
)

const step = clock.Tick(900) // 10 ms packets

// tsAt wraps the linear packet timestamp into the 32-bit RTP timestamp
// space, mirroring real wraparound behaviour.
func tsAt(base uint32, s int64) uint32 {
	return base + uint32(int64(900)*s)
}

func pushSeq(t *testing.T, r *Recoverer, seq int64, ts uint32) {
	t.Helper()
	buf, err := MarshalPacket(uint16(seq), ts, 1, []byte{0xab, 0xcd})
	if err != nil {
		t.Fatal(err)
	}
	p, err := ParsePacket(buf)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Push(p); err != nil {
		t.Fatalf("push seq %d: %v", seq, err)
	}
}

func TestParseRoundTrip(t *testing.T) {
	buf, err := MarshalPacket(42, 9000, 7, []byte{1, 2, 3})
	if err != nil {
		t.Fatal(err)
	}
	p, err := ParsePacket(buf)
	if err != nil {
		t.Fatal(err)
	}
	if p.SequenceNumber != 42 || p.Timestamp != 9000 || p.SSRC != 7 {
		t.Fatalf("header mismatch: %+v", p.Header)
	}
	if _, err := ParsePacket([]byte{0x80, 0x00}); err == nil {
		t.Fatal("short datagram must be a structured truncation error")
	}
}

func TestReorderAndWrap(t *testing.T) {
	r, _ := NewRecoverer(step, 4)
	// arrival order 0xFFFF, 1(wrapped), then late 0(wrapped)
	base := uint32(0xFFFF00)
	pushSeq(t, r, 0xFFFF, base)
	pushSeq(t, r, 0x10001, base+900)
	pushSeq(t, r, 0x10000, base+900) // late, wrapped
	intervals, gaps, st := r.Flush()
	if len(gaps) != 0 || st.Lost != 0 {
		t.Fatalf("unexpected gaps: %+v stats=%+v", gaps, st)
	}
	if len(intervals) != 1 || intervals[0].StartSeq != 0xFFFF || intervals[0].EndSeq != 0x10001 {
		t.Fatalf("interval not continuous across wrap: %+v", intervals)
	}
	// reordered packet must end up between its neighbours
	if intervals[0].Packets[0].ExtSeq != 0xFFFF || intervals[0].Packets[1].ExtSeq != 0x10000 {
		t.Fatalf("ordering wrong: %+v", intervals[0].Packets)
	}
}

func TestDuplicateAndBurstConceal(t *testing.T) {
	r, _ := NewRecoverer(step, 4)
	for s := int64(0); s < 10; s++ {
		if s == 4 || s == 5 || s == 6 { // 3-packet burst
			continue
		}
		pushSeq(t, r, s, tsAt(0, s))
	}
	pushSeq(t, r, 7, tsAt(0, 7)) // exact duplicate of seq 7
	intervals, gaps, st := r.Flush()
	if st.Duplicate != 1 || st.Lost != 3 || st.Concealed != 3 || st.Blocked != 0 {
		t.Fatalf("stats=%+v", st)
	}
	if len(intervals) != 1 || intervals[0].Concealed != 3 {
		t.Fatalf("concealed gap must not split the interval: %+v", intervals)
	}
	if gaps[0].Kind != GapConcealed || gaps[0].StartTS != 4*900 || gaps[0].EndTS != 7*900 {
		t.Fatalf("gap record wrong: %+v", gaps[0])
	}
	if got := RecoveryRate(st); got < 0.999 {
		t.Fatalf("recovery rate %f", got)
	}
}

func TestLargeBurstBlocks(t *testing.T) {
	r, _ := NewRecoverer(step, 4)
	for s := int64(0); s < 20; s++ {
		if s >= 8 && s < 20-2 { // 10-packet burst > maxConceal
			continue
		}
		pushSeq(t, r, s, tsAt(0, s))
	}
	intervals, gaps, st := r.Flush()
	if st.Blocked != 10 || st.Concealed != 0 {
		t.Fatalf("stats=%+v", st)
	}
	if len(gaps) != 1 || gaps[0].Kind != GapBlocked {
		t.Fatalf("gap must be blocked: %+v", gaps)
	}
	if len(intervals) != 2 {
		t.Fatalf("blocked gap must split timeline into 2 intervals, got %d", len(intervals))
	}
}

func TestDeterministicFlush(t *testing.T) {
	build := func() ([]Interval, []Gap, Stats) {
		r, _ := NewRecoverer(step, 3)
		order := []int64{0, 2, 1, 5, 3, 4, 8, 7} // 6 missing
		for _, s := range order {
			pushSeq(t, r, s, tsAt(0, s))
		}
		return r.Flush()
	}
	i1, g1, s1 := build()
	i2, g2, s2 := build()
	if len(i1) != len(i2) || len(g1) != len(g2) || s1 != s2 {
		t.Fatal("non-deterministic flush output")
	}
	for i := range g1 {
		if g1[i] != g2[i] {
			t.Fatalf("gap %d differs", i)
		}
	}
}
