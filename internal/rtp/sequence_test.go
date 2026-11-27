package rtp

import (
	"testing"

	"medtrace/internal/mederr"
	"medtrace/internal/timeline"

	pionrtp "github.com/pion/rtp"
)

func pkt(ssrc uint32, seq uint16) *pionrtp.Packet {
	return &pionrtp.Packet{
		Header:  pionrtp.Header{SSRC: ssrc, SequenceNumber: seq, PayloadType: 96},
		Payload: []byte{0xAA},
	}
}

func stats(t *testing.T, a *Analyzer, ssrc uint32) Stats {
	t.Helper()
	st, ok := a.StatsFor(ssrc)
	if !ok {
		t.Fatalf("no stats for ssrc %d", ssrc)
	}
	return st
}

func TestInOrderNoLoss(t *testing.T) {
	a, err := NewAnalyzer(1_000_000)
	if err != nil {
		t.Fatal(err)
	}
	for i := uint16(0); i < 5; i++ {
		if err := a.AddPacket(timeline.Timestamp(int64(i)*100), pkt(1, i)); err != nil {
			t.Fatal(err)
		}
	}
	st := stats(t, a, 1)
	if st.Received != 5 || st.Unique != 5 || st.Lost != 0 || st.Recovered != 0 ||
		st.Duplicates != 0 || st.Reordered != 0 || st.PendingGaps != 0 {
		t.Fatalf("clean stream misclassified: %+v", st)
	}
}

func TestLatePacketRecoveredInsideWindow(t *testing.T) {
	a, _ := NewAnalyzer(1_000) // 1000 ns window
	// seq 10, then 12 (gap on 11), then 11 arrives 400 ns later: recovered.
	if err := a.AddPacket(0, pkt(7, 10)); err != nil {
		t.Fatal(err)
	}
	if err := a.AddPacket(100, pkt(7, 12)); err != nil {
		t.Fatal(err)
	}
	if st := stats(t, a, 7); st.PendingGaps != 1 {
		t.Fatalf("want one pending gap, got %+v", st)
	}
	if err := a.AddPacket(400, pkt(7, 11)); err != nil {
		t.Fatal(err)
	}
	st := stats(t, a, 7)
	if st.Recovered != 1 || st.Lost != 0 || st.PendingGaps != 0 {
		t.Fatalf("late in-window packet must recover gap: %+v", st)
	}
}

func TestLatePacketOutsideWindowIsFinalLoss(t *testing.T) {
	a, _ := NewAnalyzer(1_000)
	_ = a.AddPacket(0, pkt(7, 10))
	_ = a.AddPacket(100, pkt(7, 12)) // gap on 11 opened at t=100
	// At t=2000 the gap expires into committed loss.
	_ = a.AddPacket(2_000, pkt(7, 13))
	st := stats(t, a, 7)
	if st.Lost != 1 || st.Recovered != 0 {
		t.Fatalf("expired gap must be finalized loss: %+v", st)
	}
	// Late arrival of 11 must NOT rewrite the committed statistics.
	_ = a.AddPacket(2_100, pkt(7, 11))
	st = stats(t, a, 7)
	if st.Lost != 1 || st.Recovered != 0 {
		t.Fatalf("late packet outside window must not rewrite stats: %+v", st)
	}
	if st.Received != 4 {
		t.Fatalf("late packet still counts as a received packet: %+v", st)
	}
}

func TestDuplicateAndReorder(t *testing.T) {
	a, _ := NewAnalyzer(1_000)
	_ = a.AddPacket(0, pkt(1, 100))
	_ = a.AddPacket(10, pkt(1, 100)) // exact duplicate
	_ = a.AddPacket(20, pkt(1, 102)) // gap on 101
	_ = a.AddPacket(30, pkt(1, 101)) // late recovery
	_ = a.AddPacket(40, pkt(1, 99))  // older, never gapped: reordered only
	_ = a.AddPacket(50, pkt(1, 99))  // duplicate of the reordered packet
	st := stats(t, a, 1)
	if st.Duplicates != 2 {
		t.Fatalf("duplicates=%d want 2", st.Duplicates)
	}
	if st.Recovered != 1 || st.Lost != 0 {
		t.Fatalf("recovery wrong: %+v", st)
	}
	if st.Reordered != 2 {
		t.Fatalf("reordered=%d want 2 (recovered gap + old seq)", st.Reordered)
	}
}

func TestSequenceWrap(t *testing.T) {
	a, _ := NewAnalyzer(1_000_000)
	_ = a.AddPacket(0, pkt(3, 65534))
	_ = a.AddPacket(10, pkt(3, 65535))
	_ = a.AddPacket(20, pkt(3, 1)) // wrap; gap on 0
	_ = a.AddPacket(30, pkt(3, 0)) // recovered across the 16-bit boundary
	st := stats(t, a, 3)
	if st.Recovered != 1 || st.Lost != 0 || st.Unique != 4 {
		t.Fatalf("wraparound handling wrong: %+v", st)
	}
}

func TestFlushFinalizesPendingAsLoss(t *testing.T) {
	a, _ := NewAnalyzer(1_000_000)
	_ = a.AddPacket(0, pkt(5, 0))
	_ = a.AddPacket(10, pkt(5, 3)) // gaps 1, 2 still inside window
	out := a.Flush()
	if len(out) != 1 || out[0].Lost != 2 || out[0].Recovered != 0 {
		t.Fatalf("flush must finalize pending gaps as loss: %+v", out)
	}
}

func TestRecoveryRate(t *testing.T) {
	s := Stats{Recovered: 4, Lost: 1}
	if r := s.RecoveryRate(); r < 0.79 || r > 0.81 {
		t.Fatalf("rate=%f want 0.8", r)
	}
	if (Stats{}).RecoveryRate() != 0 {
		t.Fatal("empty stats rate must be zero")
	}
}

func TestInvalidWindowAndNilPacket(t *testing.T) {
	if _, err := NewAnalyzer(0); mederr.AsError(err) == nil {
		t.Fatal("zero window must fail")
	}
	a, _ := NewAnalyzer(10)
	if err := a.AddPacket(0, nil); mederr.AsError(err) == nil {
		t.Fatal("nil packet must fail")
	}
}

func TestMultipleSSRCsIndependent(t *testing.T) {
	a, _ := NewAnalyzer(1_000)
	_ = a.AddPacket(0, pkt(100, 0))
	_ = a.AddPacket(0, pkt(200, 0))
	_ = a.AddPacket(10, pkt(100, 2)) // gap only on SSRC 100
	st100 := stats(t, a, 100)
	st200 := stats(t, a, 200)
	if st100.PendingGaps != 1 || st200.PendingGaps != 0 {
		t.Fatalf("per-ssrc state leaked: %+v %+v", st100, st200)
	}
}
