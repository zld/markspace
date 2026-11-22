package rtp

import (
	"math"
	"testing"

	"github.com/pion/rtp"

	"mediapipeline/internal/media"
)

func mkPacket(seq uint16, ts uint32, pt uint8) *rtp.Packet {
	return &rtp.Packet{
		Header:  rtp.Header{SequenceNumber: seq, Timestamp: ts, PayloadType: pt, SSRC: 0x1234},
		Payload: []byte{byte(seq), byte(seq >> 8)},
	}
}

func newReceiver(t *testing.T) *Receiver {
	t.Helper()
	r, err := New(Config{MaxReorderWindow: 8, MaxBuffer: 64})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestInOrderDelivery(t *testing.T) {
	r := newReceiver(t)
	for i := 0; i < 20; i++ {
		if err := r.Push(mkPacket(uint16(i), uint32(i*3000), 96), media.Nano(i)); err != nil {
			t.Fatal(err)
		}
	}
	r.Close()
	got := r.Delivered()
	if len(got) != 20 {
		t.Fatalf("delivered = %d", len(got))
	}
	for i, ep := range got {
		if int(ep.Seq) != i {
			t.Fatalf("delivered[%d].Seq = %d", i, ep.Seq)
		}
	}
	if s := r.Stats(); s.Lost != 0 || s.RecoveryRate != 1 {
		t.Fatalf("stats = %+v", s)
	}
}

func TestReorderingWithinWindow(t *testing.T) {
	r := newReceiver(t)
	order := []uint16{0, 1, 4, 2, 3, 7, 5, 6, 9, 8}
	for i, seq := range order {
		if err := r.Push(mkPacket(seq, uint32(seq)*3000, 96), media.Nano(i)); err != nil {
			t.Fatal(err)
		}
	}
	r.Close()
	got := r.Delivered()
	if len(got) != 10 {
		t.Fatalf("delivered = %d", len(got))
	}
	for i, ep := range got {
		if int(ep.Seq) != i {
			t.Fatalf("delivered[%d].Seq = %d, want %d", i, ep.Seq, i)
		}
	}
	if s := r.Stats(); s.Reordered == 0 || s.Lost != 0 {
		t.Fatalf("stats = %+v", s)
	}
}

func TestDuplicatesIgnored(t *testing.T) {
	r := newReceiver(t)
	for i := 0; i < 5; i++ {
		if err := r.Push(mkPacket(uint16(i), 0, 96), 0); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 5; i++ {
		if err := r.Push(mkPacket(uint16(i), 0, 96), 0); err != nil {
			t.Fatalf("duplicate push: %v", err)
		}
	}
	// Retransmission of an already-delivered packet is also a duplicate.
	if err := r.Push(mkPacket(2, 0, 96), 0); err != nil {
		t.Fatal(err)
	}
	r.Close()
	if got := len(r.Delivered()); got != 5 {
		t.Fatalf("delivered = %d, want 5", got)
	}
	if s := r.Stats(); s.Duplicates != 6 {
		t.Fatalf("duplicates = %d, want 6", s.Duplicates)
	}
}

func TestSequenceWrap(t *testing.T) {
	r := newReceiver(t)
	start := uint16(65530)
	for i := 0; i < 20; i++ {
		seq := start + uint16(i)
		if err := r.Push(mkPacket(seq, uint32(i)*3000, 96), media.Nano(i)); err != nil {
			t.Fatalf("push %d: %v", seq, err)
		}
	}
	r.Close()
	got := r.Delivered()
	if len(got) != 20 {
		t.Fatalf("delivered = %d", len(got))
	}
	for i := 0; i < 20; i++ {
		want := start + uint16(i)
		if got[i].Seq != want {
			t.Fatalf("delivered[%d].Seq = %d, want %d", i, got[i].Seq, want)
		}
	}
	if s := r.Stats(); s.Wraps == 0 || s.Lost != 0 {
		t.Fatalf("stats = %+v", s)
	}
}

func TestWrapWithReordering(t *testing.T) {
	r := newReceiver(t)
	// Deliver across the wrap point out of order, within the window.
	seqs := []uint16{65531, 65533, 65532, 65534, 0, 65535, 2, 1, 3}
	for i, seq := range seqs {
		if err := r.Push(mkPacket(seq, uint32(i)*3000, 96), media.Nano(i)); err != nil {
			t.Fatalf("push %d: %v", seq, err)
		}
	}
	r.Close()
	got := r.Delivered()
	want := []uint16{65531, 65532, 65533, 65534, 65535, 0, 1, 2, 3}
	if len(got) != len(want) {
		t.Fatalf("delivered = %d", len(got))
	}
	for i := range want {
		if got[i].Seq != want[i] {
			t.Fatalf("delivered[%d] = %d, want %d", i, got[i].Seq, want[i])
		}
	}
	if s := r.Stats(); s.Lost != 0 {
		t.Fatalf("stats = %+v", s)
	}
}

func TestBoundedLossIsRecoveredOrCounted(t *testing.T) {
	r := newReceiver(t)
	// Drop sequences 3 and 4; later packets push the window past them.
	for i := 0; i < 40; i++ {
		if i == 3 || i == 4 {
			continue
		}
		if err := r.Push(mkPacket(uint16(i), uint32(i)*3000, 96), media.Nano(i)); err != nil {
			t.Fatal(err)
		}
	}
	r.Close()
	s := r.Stats()
	if s.Lost != 2 {
		t.Fatalf("lost = %d, want 2", s.Lost)
	}
	if s.Delivered != 38 {
		t.Fatalf("delivered = %d, want 38", s.Delivered)
	}
	if s.UnrecoverableGaps != 1 {
		t.Fatalf("gaps = %d, want 1", s.UnrecoverableGaps)
	}
	if math.Abs(s.RecoveryRate-38.0/40.0) > 1e-9 {
		t.Fatalf("recovery rate = %f", s.RecoveryRate)
	}
	gaps := r.Gaps()
	if len(gaps) != 1 || gaps[0].Start != 3 || gaps[0].Count != 2 {
		t.Fatalf("gaps = %+v", gaps)
	}
}

func TestLatePacketWithinWindowIsRecovered(t *testing.T) {
	r := newReceiver(t)
	for _, seq := range []uint16{0, 1, 2, 4, 5, 3, 6} {
		if err := r.Push(mkPacket(seq, 0, 96), 0); err != nil {
			t.Fatal(err)
		}
	}
	r.Close()
	if s := r.Stats(); s.Recovered == 0 || s.Lost != 0 {
		t.Fatalf("stats = %+v", s)
	}
}

func TestBurstLossStaysBounded(t *testing.T) {
	r := newReceiver(t)
	// A 5-packet burst loss, then traffic resumes.
	for i := 0; i < 30; i++ {
		if i >= 10 && i < 15 {
			continue
		}
		if err := r.Push(mkPacket(uint16(i), 0, 96), 0); err != nil {
			t.Fatal(err)
		}
	}
	r.Close()
	s := r.Stats()
	if s.Lost != 5 {
		t.Fatalf("lost = %d", s.Lost)
	}
	if s.RecoveryRate < 0.8 {
		t.Fatalf("recovery rate = %f", s.RecoveryRate)
	}
	gaps := r.Gaps()
	if len(gaps) != 1 || gaps[0].Count != 5 {
		t.Fatalf("gaps = %+v", gaps)
	}
}

func TestHugeJumpRejected(t *testing.T) {
	r, err := New(Config{MaxReorderWindow: 4, MaxBuffer: 16, MaxExtend: 100})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Push(mkPacket(0, 0, 96), 0); err != nil {
		t.Fatal(err)
	}
	err = r.Push(mkPacket(50000, 0, 96), 0)
	if err == nil {
		t.Fatal("expected error for stream restart jump")
	}
	if media.CategoryOf(err) != media.UnrecoverableGap {
		t.Fatalf("category = %v", media.CategoryOf(err))
	}
	if e := media.AsError(err); !e.HasSeq {
		t.Fatalf("error lacks sequence: %v", err)
	}
}

func TestBufferLimitFailsLoudly(t *testing.T) {
	r, err := New(Config{MaxReorderWindow: 100, MaxBuffer: 4})
	if err != nil {
		t.Fatal(err)
	}
	// A hole at the head forces buffering of everything after it.
	_ = r.Push(mkPacket(0, 0, 96), 0)
	var lastErr error
	for i := 2; i < 20; i++ {
		if err := r.Push(mkPacket(uint16(i), 0, 96), 0); err != nil {
			lastErr = err
			break
		}
	}
	if lastErr == nil || media.CategoryOf(lastErr) != media.ReplayLimitExceeded {
		t.Fatalf("expected buffer limit error, got %v", lastErr)
	}
}

func TestLatencyPercentile(t *testing.T) {
	r := newReceiver(t)
	for i := 0; i < 100; i++ {
		r.NoteLatency(media.Nano(i) * media.Nano(1_000_000))
	}
	s := r.Stats()
	if got := media.Millis(s.P95Latency); got != 95 {
		t.Fatalf("p95 = %v ms, want 95", got)
	}
	if media.Millis(s.MaxLatency) != 99 {
		t.Fatalf("max = %v ms", media.Millis(s.MaxLatency))
	}
}

func TestConfigValidation(t *testing.T) {
	if _, err := New(Config{MaxReorderWindow: 0, MaxBuffer: 10}); err == nil {
		t.Fatal("zero window accepted")
	}
	if _, err := New(Config{MaxReorderWindow: 20, MaxBuffer: 0}); err == nil {
		t.Fatal("zero buffer accepted")
	}
}
