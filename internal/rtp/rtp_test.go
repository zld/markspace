package rtp

import (
	"errors"
	"testing"

	"github.com/pion/rtp"

	"mediaarchive/internal/timeline"
)

func pkt(seq uint16, ts uint32) *rtp.Packet {
	return &rtp.Packet{
		Header:  rtp.Header{SequenceNumber: seq, Timestamp: ts},
		Payload: []byte{byte(seq)},
	}
}

func seqOf(slots []Slot) []uint64 {
	out := make([]uint64, len(slots))
	for i, s := range slots {
		out[i] = s.Sequence
	}
	return out
}

func TestInOrderDelivery(t *testing.T) {
	buf := NewJitterBuffer(DefaultConfig())
	for i := uint16(0); i < 5; i++ {
		if err := buf.Push(pkt(i, uint32(i)*90)); err != nil {
			t.Fatal(err)
		}
	}
	slots := buf.Pop()
	got := seqOf(slots)
	want := []uint64{0, 1, 2, 3, 4}
	if len(got) != len(want) {
		t.Fatalf("slots=%v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("slots=%v want %v", got, want)
		}
	}
}

func TestReorderRecovery(t *testing.T) {
	buf := NewJitterBuffer(Config{Window: 8, Policy: PolicyGap})
	order := []uint16{0, 1, 3, 2, 4} // 2 and 3 swapped
	for _, s := range order {
		if err := buf.Push(pkt(s, uint32(s)*90)); err != nil {
			t.Fatal(err)
		}
	}
	// Pop returns the contiguous prefix immediately (0,1); Flush then drains
	// the reordered remainder without inventing any gaps.
	prefix := buf.Pop()
	slots := append(prefix, buf.Flush()...)
	got := seqOf(slots)
	want := []uint64{0, 1, 2, 3, 4}
	if len(got) != 5 {
		t.Fatalf("slots=%v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("reordered slots=%v want %v", got, want)
		}
	}
	st := buf.Stats()
	if st.Reordered < 1 {
		t.Fatalf("expected reorder count, got %+v", st)
	}
}

func TestDuplicatePacketsCountedOnce(t *testing.T) {
	buf := NewJitterBuffer(DefaultConfig())
	_ = buf.Push(pkt(0, 0))
	_ = buf.Push(pkt(1, 90))
	if err := buf.Push(pkt(0, 0)); err != nil {
		t.Fatal(err) // duplicate of an already buffered seq
	}
	slots := buf.Pop()
	if len(slots) != 2 {
		t.Fatalf("duplicate emitted: %v", seqOf(slots))
	}
	// Re-push after emission: still a duplicate, must not reappear.
	_ = buf.Push(pkt(0, 0))
	if st := buf.Stats(); st.Duplicates != 2 {
		t.Fatalf("duplicates=%d want 2", st.Duplicates)
	}
}

func TestSequenceNumberWraparound(t *testing.T) {
	buf := NewJitterBuffer(Config{Window: 16, Policy: PolicyGap})
	seqs := []uint16{65534, 65535, 0, 1, 2}
	for i, s := range seqs {
		if err := buf.Push(pkt(s, uint32(i)*90)); err != nil {
			t.Fatal(err)
		}
	}
	slots := buf.Pop()
	want := []uint64{65534, 65535, 65536, 65537, 65538}
	got := seqOf(slots)
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("wrap slots=%v want %v", got, want)
		}
	}
}

func TestInWindowLossRecoveredByPLC(t *testing.T) {
	// Missing seq 2; window 4 means it resolves once seq 6 is buffered.
	buf := NewJitterBuffer(Config{Window: 4, Policy: PolicyPLC})
	for _, s := range []uint16{0, 1, 3, 4, 5, 6} {
		_ = buf.Push(pkt(s, uint32(s)*90))
	}
	slots := buf.Flush()
	var recovered int
	for _, s := range slots {
		if s.Recovered && s.Class == SlotRecovered {
			recovered++
			if s.Sequence != 2 {
				t.Fatalf("recovered slot seq=%d want 2", s.Sequence)
			}
		}
	}
	if recovered != 1 {
		t.Fatalf("recovered=%d want 1, slots=%v", recovered, seqOf(slots))
	}
	if st := buf.Stats(); st.RecoveredPackets != 1 || st.UnrecoveredPackets != 0 {
		t.Fatalf("stats wrong: %+v", st)
	}
}

func TestGapPolicyKeepsUnrecoveredCount(t *testing.T) {
	buf := NewJitterBuffer(Config{Window: 2, Policy: PolicyGap})
	for _, s := range []uint16{0, 1, 4, 5} { // 2,3 missing, window slides
		_ = buf.Push(pkt(s, uint32(s)*90))
	}
	slots := buf.Flush()
	gaps := 0
	for _, s := range slots {
		if s.Class == SlotGap {
			gaps++
		}
	}
	if gaps != 2 {
		t.Fatalf("gaps=%d want 2, slots=%v", gaps, seqOf(slots))
	}
	st := buf.Stats()
	if st.UnrecoveredPackets != 2 || st.WindowGaps != 2 {
		t.Fatalf("stats wrong: %+v", st)
	}
}

func TestRecoverEndToEndAndNanos(t *testing.T) {
	pkts := []*rtp.Packet{pkt(0, 0), pkt(2, 180), pkt(1, 90)}
	stream, err := Recover(pkts, Config{Window: 4, Policy: PolicyPLC}, 9000)
	if err != nil {
		t.Fatal(err)
	}
	if len(stream.Slots) != 3 {
		t.Fatalf("slots=%d", len(stream.Slots))
	}
	// 9000 Hz clock: 180 ticks = 20 ms.
	if stream.TimeNanos[2] != 20_000_000 {
		t.Fatalf("nanos=%v", stream.TimeNanos)
	}
	if stream.Expected != 3 {
		t.Fatalf("expected span=%d", stream.Expected)
	}
	if err := stream.IntegrityError(); err != nil {
		t.Fatalf("PLC stream should have no integrity error: %v", err)
	}
}

func TestIntegrityErrorOnGap(t *testing.T) {
	pkts := []*rtp.Packet{pkt(0, 0), pkt(3, 270)} // 1,2 gap
	stream, err := Recover(pkts, Config{Window: 1, Policy: PolicyGap}, 9000)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.IntegrityError(); !errors.Is(err, timeline.ErrGap) {
		t.Fatalf("want ErrGap, got %v", err)
	}
	if missing := stream.MissingSequenceNumbers(); len(missing) != 2 {
		t.Fatalf("missing=%v", missing)
	}
}

func TestBufferCapacityBound(t *testing.T) {
	buf := NewJitterBuffer(Config{Window: 4, MaxBuffered: 3})
	// Push spaced-out sequences so they all stay held (none pop).
	for _, s := range []uint16{0, 5, 10, 15} {
		if err := buf.Push(pkt(s, 0)); err != nil {
			if !errors.Is(err, timeline.ErrLimitExceeded) {
				t.Fatalf("want limit error, got %v", err)
			}
			return
		}
	}
	t.Fatal("expected capacity error")
}
