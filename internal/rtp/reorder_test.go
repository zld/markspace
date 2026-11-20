package rtp

import (
	"testing"

	"github.com/pion/rtp"
)

func mkPacket(seq uint16) *rtp.Packet {
	return &rtp.Packet{
		Header: rtp.Header{
			SequenceNumber: seq,
			SSRC:           0x12345678,
			PayloadType:    96,
			Timestamp:      uint32(seq) * 3600,
		},
		Payload: []byte{0x67, byte(seq)},
	}
}

func seqs(pkts []*rtp.Packet) []uint16 {
	out := make([]uint16, len(pkts))
	for i, p := range pkts {
		out[i] = p.SequenceNumber
	}
	return out
}

func TestReorderInOrder(t *testing.T) {
	b := NewReorderBuffer()
	for seq := uint16(0); seq < 10; seq++ {
		out := b.Push(mkPacket(seq))
		if len(out) != 1 || out[0].SequenceNumber != seq {
			t.Fatalf("seq %d: expected immediate delivery, got %v", seq, seqs(out))
		}
	}
	st := b.Stats()
	if st.Gaps != 0 || st.Duplicates != 0 || st.LateOutOfWindow != 0 || st.Received != 10 {
		t.Fatalf("unexpected stats: %+v", st)
	}
}

func TestReorderWraparound(t *testing.T) {
	b := NewReorderBuffer()
	// 65533, 65534, 65535, 0, 1, 2 in order
	for _, seq := range []uint16{65533, 65534, 65535, 0, 1, 2} {
		out := b.Push(mkPacket(seq))
		if len(out) != 1 || out[0].SequenceNumber != seq {
			t.Fatalf("seq %d: got %v", seq, seqs(out))
		}
	}
	if got := b.Stats().Gaps; got != 0 {
		t.Fatalf("wraparound produced %d gaps", got)
	}
}

func TestReorderOutOfOrderOneSwap(t *testing.T) {
	b := NewReorderBuffer()
	if out := b.Push(mkPacket(5)); len(out) != 1 || out[0].SequenceNumber != 5 {
		t.Fatalf("first packet must be delivered immediately")
	}
	if out := b.Push(mkPacket(7)); out != nil {
		t.Fatalf("gap packet must be parked, got %v", seqs(out))
	}
	out := b.Push(mkPacket(6))
	if got := seqs(out); len(got) != 2 || got[0] != 6 || got[1] != 7 {
		t.Fatalf("expected [6 7], got %v", got)
	}
	st := b.Stats()
	if st.Gaps != 1 {
		t.Fatalf("expected 1 gap, got %d", st.Gaps)
	}
}

func TestReorderGapCountedOnce(t *testing.T) {
	b := NewReorderBuffer()
	b.Push(mkPacket(10))
	b.Push(mkPacket(11)) // hole at 12
	b.Push(mkPacket(13)) // parks, gap 12 counted
	b.Flush()
	if got := b.Stats().Gaps; got != 1 {
		t.Fatalf("first flush gap = %d, want 1", got)
	}
	// a second flush must not recount the same hole
	b.Flush()
	if got := b.Stats().Gaps; got != 1 {
		t.Fatalf("second flush gap = %d, want 1", got)
	}
}

func TestReorderDuplicate(t *testing.T) {
	b := NewReorderBuffer()
	b.Push(mkPacket(20))
	b.Push(mkPacket(22)) // hole at 21, packet parked
	if out := b.Push(mkPacket(20)); out != nil {
		t.Fatalf("already-delivered duplicate must not be delivered")
	}
	st := b.Stats()
	if st.Duplicates != 1 {
		t.Fatalf("duplicates = %d, want 1", st.Duplicates)
	}
	if st.Gaps != 1 {
		t.Fatalf("gaps = %d, want 1", st.Gaps)
	}
}

func TestReorderLateOutOfWindow(t *testing.T) {
	b := NewReorderBuffer()
	b.Push(mkPacket(0))
	// jump far enough that the first packet's successors are out of window
	b.Push(mkPacket(100))
	if out := b.Push(mkPacket(1)); len(out) != 0 {
		t.Fatalf("late packet must be dropped, got %v", seqs(out))
	}
	st := b.Stats()
	if st.LateOutOfWindow != 1 {
		t.Fatalf("late = %d, want 1", st.LateOutOfWindow)
	}
	if st.Duplicates != 0 {
		t.Fatalf("an unseen late packet is not a duplicate, got %d", st.Duplicates)
	}
}

func TestReorderFlushDeliversParked(t *testing.T) {
	b := NewReorderBuffer()
	b.Push(mkPacket(30))
	b.Push(mkPacket(32)) // parks, hole at 31
	out := b.Flush()
	if got := seqs(out); len(got) != 1 || got[0] != 32 {
		t.Fatalf("flush expected [32], got %v", got)
	}
	if b.Stats().Flushed != 1 || b.Stats().Gaps != 1 {
		t.Fatalf("unexpected stats: %+v", b.Stats())
	}
}

func TestReorderWindowFullResync(t *testing.T) {
	b := NewReorderBuffer()
	b.Push(mkPacket(0))
	// fill the window with parked packets, then jump beyond it
	b.Push(mkPacket(70))
	st := b.Stats()
	// holes 1..69, each counted exactly once
	if st.Gaps != 69 {
		t.Fatalf("gaps = %d, want 69", st.Gaps)
	}
	// the stream resynchronises: the next in-order packet delivers directly
	out := b.Push(mkPacket(71))
	if len(out) != 1 || out[0].SequenceNumber != 71 {
		t.Fatalf("resync failed, got %v", seqs(out))
	}
}
