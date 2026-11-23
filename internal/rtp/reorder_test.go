package rtp

import (
	"math"
	"testing"

	"github.com/pion/rtp"

	"mediakernel/internal/mediaerr"
)

func pkt(seq uint16, ts uint32) *rtp.Packet {
	return &rtp.Packet{
		Header:  rtp.Header{SequenceNumber: seq, Timestamp: ts, SSRC: 1, PayloadType: 96},
		Payload: []byte{byte(seq)},
	}
}

func collect(out []Ordered) []uint16 {
	got := make([]uint16, 0, len(out))
	for _, o := range out {
		got = append(got, o.Packet.SequenceNumber)
	}
	return got
}

func eq(a, b []uint16) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestInOrderAndDuplicate(t *testing.T) {
	r, err := NewReorderer(Config{Window: 8})
	if err != nil {
		t.Fatal(err)
	}
	var delivered []uint16
	for i := uint16(0); i < 10; i++ {
		out, errs := r.Push("v", pkt(i, uint32(i)*3000))
		if len(errs) != 0 {
			t.Fatalf("unexpected errors: %v", errs)
		}
		delivered = append(delivered, collect(out)...)
	}
	if !eq(delivered, []uint16{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}) {
		t.Fatalf("delivered = %v", delivered)
	}
	// Duplicates must not be counted again and produce nothing.
	out, errs := r.Push("v", pkt(5, 15000))
	if len(out) != 0 || len(errs) != 0 {
		t.Fatalf("duplicate produced output=%v errs=%v", out, errs)
	}
	out, _ = r.Push("v", pkt(0, 0))
	if len(out) != 0 {
		t.Fatalf("old duplicate produced output")
	}
	st := r.Stats()[0]
	if st.Received != 10 || st.Duplicates != 2 {
		t.Fatalf("stats = %+v", st)
	}
	if st.RecoveryRate() != 1 {
		t.Fatalf("clean stream recovery must be 1, got %f", st.RecoveryRate())
	}
}

func TestReorderWithinWindow(t *testing.T) {
	r, _ := NewReorderer(Config{Window: 8})
	// Arrival: 0,1,3,2,4  -> 2 fills the hole and 2,3 drain together.
	r.Push("v", pkt(0, 0))
	r.Push("v", pkt(1, 3000))
	out, _ := r.Push("v", pkt(3, 9000))
	if len(out) != 0 {
		t.Fatalf("gap must hold delivery, got %v", collect(out))
	}
	out, _ = r.Push("v", pkt(2, 6000))
	if got := collect(out); !eq(got, []uint16{2, 3}) {
		t.Fatalf("hole fill drain = %v", got)
	}
	out, _ = r.Push("v", pkt(4, 12000))
	if got := collect(out); !eq(got, []uint16{4}) {
		t.Fatalf("tail = %v", got)
	}
	st := r.Stats()[0]
	if st.Reordered != 1 {
		t.Fatalf("reordered = %d, want 1", st.Reordered)
	}
	if st.UnrecoveredPackets != 0 || st.RecoveryRate() != 1 {
		t.Fatalf("recoverable loss must not hurt recovery: %+v", st)
	}
	if st.RecoverableBursts < 1 {
		t.Fatalf("expected a recoverable burst marker, got %+v", st)
	}
}

func TestBurstLossRecoveredAndUnrecoverable(t *testing.T) {
	r, _ := NewReorderer(Config{Window: 4})
	r.Push("v", pkt(0, 0))
	// Burst: 1,2,3 lost, packet 4 arrives while the hole is within window.
	out, errs := r.Push("v", pkt(4, 12000))
	if len(out) != 0 || len(errs) != 0 {
		t.Fatalf("windowed gap should stay recoverable: out=%v errs=%v", out, errs)
	}
	// The missing run is one recoverable burst.
	st := r.Stats()[0]
	if st.RecoverableBursts != 1 {
		t.Fatalf("bursts = %d, want 1", st.RecoverableBursts)
	}
	// Recover the burst.
	out, _ = r.Push("v", pkt(1, 3000))
	out2, _ := r.Push("v", pkt(2, 6000))
	out3, _ := r.Push("v", pkt(3, 9000))
	all := append(append(collect(out), collect(out2)...), collect(out3)...)
	if !eq(all, []uint16{1, 2, 3, 4}) {
		t.Fatalf("recovered drain = %v", all)
	}

	// Unrecoverable: jump 5 -> 20 exceeds the window of 4.
	_, errs = r.Push("v", pkt(20, 60000))
	if len(errs) == 0 {
		t.Fatal("oversized jump must report an unrecoverable burst")
	}
	me, ok := mediaerr.As(errs[0])
	if !ok || me.Code != "burst_unrecoverable" || me.Recoverable {
		t.Fatalf("error = %v", errs[0])
	}
	st = r.Stats()[0]
	if st.UnrecoveredPackets == 0 {
		t.Fatalf("missing slots must be counted: %+v", st)
	}
	if st.RecoveryRate() >= 1 {
		t.Fatalf("recovery rate must drop below 1 after loss: %f", st.RecoveryRate())
	}
}

func TestLatePacketPastWindow(t *testing.T) {
	r, _ := NewReorderer(Config{Window: 2})
	r.Push("v", pkt(10, 0))
	// Jump far enough to abandon seq 11.. ; an old seq then arrives late.
	r.Push("v", pkt(30, 90000))
	out, errs := r.Push("v", pkt(11, 3000))
	if len(out) != 0 {
		t.Fatalf("late packet must not be delivered: %v", collect(out))
	}
	if len(errs) == 0 || errs[0].Code != "late_packet" {
		t.Fatalf("expected late_packet error, got %v", errs)
	}
	st := r.Stats()[0]
	if st.LatePackets != 1 || st.UnrecoveredPackets == 0 {
		t.Fatalf("late stats = %+v", st)
	}
}

func TestTimestampWrap(t *testing.T) {
	r, _ := NewReorderer(Config{Window: 4})
	out, _ := r.Push("v", pkt(0, 0xFFFFFFFE))
	if len(out) != 1 || out[0].MonotonicTS != 0xFFFFFFFE {
		t.Fatalf("first ts = %+v", out)
	}
	out, _ = r.Push("v", pkt(1, 0xFFFFFFFF))
	if out[0].MonotonicTS != 0xFFFFFFFF {
		t.Fatalf("second ts = %d", out[0].MonotonicTS)
	}
	out, _ = r.Push("v", pkt(2, 0))
	if out[0].MonotonicTS != 0x100000000 {
		t.Fatalf("wrapped ts = %d, want 2^32", out[0].MonotonicTS)
	}
}

func TestSequenceWrap(t *testing.T) {
	r, _ := NewReorderer(Config{Window: 4})
	r.Push("v", pkt(0xFFFE, 100))
	r.Push("v", pkt(0xFFFF, 200))
	out, errs := r.Push("v", pkt(0, 300))
	if len(errs) != 0 || len(out) != 1 || out[0].Packet.SequenceNumber != 0 {
		t.Fatalf("wrap delivery = %v errs=%v", out, errs)
	}
	out, _ = r.Push("v", pkt(1, 400))
	if out[0].Packet.SequenceNumber != 1 {
		t.Fatalf("post-wrap = %v", out)
	}
}

func TestFlushTailGap(t *testing.T) {
	r, _ := NewReorderer(Config{Window: 4})
	r.Push("v", pkt(0, 0))
	r.Push("v", pkt(1, 1))
	r.Push("v", pkt(3, 3)) // seq 2 missing at end
	out, st, errs := r.Flush("v")
	if len(errs) == 0 || errs[0].Code != "tail_gap" {
		t.Fatalf("expected tail_gap, got %v", errs)
	}
	if st.UnrecoveredPackets != 1 {
		t.Fatalf("unrecovered = %d, want 1", st.UnrecoveredPackets)
	}
	if got := collect(out); !eq(got, []uint16{3}) {
		t.Fatalf("flush drain = %v", got)
	}
}

func TestWindowValidation(t *testing.T) {
	if _, err := NewReorderer(Config{Window: 0}); err == nil {
		t.Fatal("zero window must fail")
	}
	if _, err := NewReorderer(Config{Window: math.MaxUint16}); err == nil {
		t.Fatal("oversized window must fail")
	}
}

func TestMultipleStreamsAreIndependent(t *testing.T) {
	r, _ := NewReorderer(Config{Window: 4})
	r.Push("audio", pkt(0, 0))
	r.Push("video", pkt(0, 0))
	r.Push("audio", pkt(2, 2)) // gap only on audio
	r.Push("video", pkt(1, 1))
	stats := r.Stats()
	if len(stats) != 2 || stats[0].StreamID != "audio" || stats[1].StreamID != "video" {
		t.Fatalf("stream ledgers = %+v", stats)
	}
	if stats[0].RecoverableBursts != 1 || stats[1].RecoverableBursts != 0 {
		t.Fatalf("stream isolation broken: %+v", stats)
	}
}
