package rtp

import (
	"testing"

	"github.com/pion/rtp"

	"mediasync/internal/model"
)

func mkPacket(seq uint16, ts uint32) *rtp.Packet {
	return &rtp.Packet{
		Header:  rtp.Header{SequenceNumber: seq, Timestamp: ts, PayloadType: 96, SSRC: 1},
		Payload: []byte{byte(seq), byte(seq >> 8)},
	}
}

func pushSeq(b *ReorderBuffer, ext uint64, arrival int) {
	p := Packet{Raw: mkPacket(uint16(ext), uint32(ext*160)), ExtSeq: ext, Arrival: arrival}
	if ok, err := b.Push(p); err != nil || !ok {
		b.rec.Add(model.ErrorRecord{Code: model.ErrCorruptPacket, Message: "push failed"})
	}
}

func TestExtendSeqWraparound(t *testing.T) {
	var prev uint64
	seen := false
	feed := func(seq16 uint16) uint64 {
		ext := ExtendSeq(prev, seen, seq16)
		prev, seen = ext, true
		return ext
	}
	if feed(65534) != 65534 || feed(65535) != 65535 {
		t.Fatal("pre-wrap extension wrong")
	}
	if got := feed(0); got != 65536 {
		t.Fatalf("wrap 65535->0 = %d, want 65536", got)
	}
	if got := feed(1); got != 65537 {
		t.Fatalf("post-wrap = %d, want 65537", got)
	}
	// Wrap again into the third cycle.
	prev = (2 << 16) - 1 // 131071
	if got := ExtendSeq(prev, true, 0); got != 131072 {
		t.Fatalf("second wrap = %d, want 131072", got)
	}
}

func TestReorderInOrder(t *testing.T) {
	b := NewReorderBuffer(DefaultReorderConfig(), model.NewRecorder())
	for i := 0; i < 50; i++ {
		pushSeq(b, uint64(i), i)
	}
	out := b.Drain()
	if len(out) != 50 {
		t.Fatalf("emitted %d, want 50", len(out))
	}
	for i, p := range out {
		if p.ExtSeq != uint64(i) {
			t.Fatalf("position %d got seq %d", i, p.ExtSeq)
		}
	}
	if b.Emitted != 50 || b.Duplicates != 0 || b.MissingCount() != 0 {
		t.Fatalf("counters wrong: %+v", b)
	}
}

func TestReorderDuplicatesAndReorder(t *testing.T) {
	rec := model.NewRecorder()
	b := NewReorderBuffer(ReorderConfig{WaitWindow: 5, MaxBuffered: 64}, rec)
	order := []uint64{0, 1, 3, 2, 2, 4, 6, 5, 0}
	for i, s := range order {
		pushSeq(b, s, i)
	}
	out := b.Drain()
	// 0..6 each once; duplicates of 2 and 0 dropped.
	if len(out) != 7 {
		t.Fatalf("emitted %d, want 7", len(out))
	}
	for i, p := range out {
		if p.ExtSeq != uint64(i) {
			t.Fatalf("position %d got seq %d", i, p.ExtSeq)
		}
	}
	if b.Duplicates != 2 {
		t.Fatalf("duplicates = %d, want 2", b.Duplicates)
	}
	if b.HighWater < 1 {
		t.Fatal("high water mark never rose")
	}
}

func TestWaitWindowThenGap(t *testing.T) {
	rec := model.NewRecorder()
	b := NewReorderBuffer(ReorderConfig{WaitWindow: 3, MaxBuffered: 64}, rec)
	// Missing seq 2. The gap opens on arrival 2 (seq 3 first appears after
	// it); push enough later packets that WaitWindow fully elapses.
	order := []uint64{0, 1, 3, 4, 5, 6, 7}
	var emitted []uint64
	for i, s := range order {
		pushSeq(b, s, i)
		for _, p := range b.Drain() {
			emitted = append(emitted, p.ExtSeq)
		}
		_ = b.FlushExpired(false)
		// Drain again in case closing the gap released more contiguous work.
		for _, p := range b.Drain() {
			emitted = append(emitted, p.ExtSeq)
		}
	}
	gaps := b.Gaps
	if len(gaps) != 1 || gaps[0].Start != 2 || gaps[0].End != 2 || gaps[0].Count != 1 {
		t.Fatalf("want single gap at 2, got %+v", gaps)
	}
	// Every present packet 0,1,3..7 emitted exactly once and in order.
	want := []uint64{0, 1, 3, 4, 5, 6, 7}
	if len(emitted) != len(want) {
		t.Fatalf("emitted = %v, want %v", emitted, want)
	}
	for i := range want {
		if emitted[i] != want[i] {
			t.Fatalf("emitted = %v, want %v", emitted, want)
		}
	}
	if b.MissingCount() != 1 {
		t.Fatalf("missing count = %d, want 1", b.MissingCount())
	}
	// A late arrival for a closed gap must not resurrect it.
	if ok, _ := b.Push(Packet{Raw: mkPacket(2, 0), ExtSeq: 2, Arrival: 99}); !ok || b.Late != 1 {
		t.Fatalf("late packet handling wrong: ok=%v late=%d", ok, b.Late)
	}
}

func TestGapRecoveredInsideWindow(t *testing.T) {
	rec := model.NewRecorder()
	b := NewReorderBuffer(ReorderConfig{WaitWindow: 10, MaxBuffered: 64}, rec)
	pushSeq(b, 0, 0)
	pushSeq(b, 1, 1)
	pushSeq(b, 3, 2)
	_ = b.FlushExpired(false) // gap still inside window
	if len(b.Gaps) != 0 {
		t.Fatalf("gap declared too early: %+v", b.Gaps)
	}
	pushSeq(b, 2, 3) // late but recoverable
	out := b.Drain()
	if len(out) != 4 || out[2].ExtSeq != 2 {
		t.Fatalf("recovered order wrong: %+v", out)
	}
	if b.MissingCount() != 0 {
		t.Fatal("recovered packet must not count as missing")
	}
}

func TestBurstLossAndForcedFlush(t *testing.T) {
	rec := model.NewRecorder()
	b := NewReorderBuffer(ReorderConfig{WaitWindow: 100, MaxBuffered: 64}, rec)
	for _, s := range []uint64{0, 1, 5, 6} { // burst loss 2,3,4
		pushSeq(b, s, int(s))
	}
	_ = b.Drain()
	if g := b.FlushExpired(false); len(g) != 0 {
		t.Fatal("window should keep the gap open")
	}
	gaps := b.FlushExpired(true)
	if len(gaps) != 1 || gaps[0].Count != 3 || gaps[0].Start != 2 || gaps[0].End != 4 {
		t.Fatalf("forced flush gap wrong: %+v", gaps)
	}
	if b.Drain() != nil && len(b.Drain()) != 0 {
		t.Fatal("nothing should remain buffered")
	}
}

func TestMemoryCap(t *testing.T) {
	rec := model.NewRecorder()
	b := NewReorderBuffer(ReorderConfig{WaitWindow: 5, MaxBuffered: 4}, rec)
	// Hold base at 0 and fill with out-of-order packets.
	pushSeq(b, 0, 0)
	for _, s := range []uint64{5, 6, 7, 8} {
		pushSeq(b, s, int(s))
	}
	ok, err := b.Push(Packet{Raw: mkPacket(9, 0), ExtSeq: 9, Arrival: 9})
	if ok || err == nil {
		t.Fatal("memory cap must reject further packets")
	}
	if !rec.HasError() || rec.Records()[0].Code != model.ErrLimitExceeded {
		t.Fatalf("want LIMIT_EXCEEDED record, got %v", rec.Records())
	}
}
