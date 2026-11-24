package jitter

import (
	"testing"

	"mediarun/internal/fault"
)

// TestBufferLatePacketAfterFlushIsRejected proves flush commits the whole
// window and nothing can be reinserted afterwards.
func TestBufferLatePacketAfterFlushIsRejected(t *testing.T) {
	b := newBuf(t, 4, 32)
	for seq := uint16(1); seq <= 6; seq++ {
		b.Push(pkt(seq, 0))
	}
	flushed := b.Flush()
	if len(flushed) != 4 { // window of 4 holds 3..6; 1 and 2 committed
		t.Fatalf("flushed = %d", len(flushed))
	}
	res := b.Push(pkt(3, 0))
	if res.Reason != ReasonLate {
		t.Fatalf("reason = %s", res.Reason)
	}
	if b.Faults().CountKind(fault.KindLate) != 1 {
		t.Fatalf("faults = %s", b.Faults().Summary())
	}
}

// TestBufferDuplicateAfterFlushIsLate ensures a duplicate of a committed packet
// is treated as late, not buffered again.
func TestBufferDuplicateAfterFlushIsLate(t *testing.T) {
	b := newBuf(t, 2, 32)
	for seq := uint16(1); seq <= 4; seq++ {
		b.Push(pkt(seq, 0))
	}
	b.Flush()
	res := b.Push(pkt(4, 0))
	if res.Reason != ReasonLate {
		t.Fatalf("reason = %s", res.Reason)
	}
}

func TestBufferHeartbeatOutOfOrderStats(t *testing.T) {
	b := newBuf(t, 8, 32)
	for _, seq := range []uint16{1, 3, 2, 4} {
		b.Push(pkt(seq, 0))
	}
	if b.Stats().OutOfOrder() != 1 {
		t.Fatalf("out of order = %d", b.Stats().OutOfOrder())
	}
}

func TestBufferWindowOneStillOrders(t *testing.T) {
	b := newBuf(t, 1, 32)
	if got := b.Push(pkt(1, 0)).Committed; len(got) != 0 {
		t.Fatalf("committed early: %v", seqs(got))
	}
	got := b.Push(pkt(2, 0)).Committed
	if len(got) != 1 || got[0].ExtSeq != 1 {
		t.Fatalf("committed = %v", seqs(got))
	}
}

func TestBufferNilPacketRecorded(t *testing.T) {
	b := newBuf(t, 2, 4)
	res := b.Push(nil)
	if res.Reason != ReasonConfig {
		t.Fatalf("reason = %s", res.Reason)
	}
	if b.Faults().CountKind(fault.KindConfig) != 1 {
		t.Fatalf("faults = %s", b.Faults().Summary())
	}
}

func TestDiscardGapReleasesContiguousRun(t *testing.T) {
	b := newBuf(t, 1, 32)
	b.Push(pkt(1, 0))
	b.Push(pkt(2, 0))
	b.Push(pkt(4, 0))
	b.Push(pkt(5, 0))
	// Sequence 3 is missing and is the current floor.
	if b.Floor() != 3 {
		t.Fatalf("floor = %d want 3", b.Floor())
	}
	released, err := b.DiscardGap(4)
	if err != nil {
		t.Fatal(err)
	}
	if len(released) != 2 {
		t.Fatalf("released = %v", seqs(released))
	}
	if released[0].ExtSeq != 4 || released[1].ExtSeq != 5 {
		t.Fatalf("released = %v", seqs(released))
	}
	if b.Floor() != 6 {
		t.Fatalf("floor = %d", b.Floor())
	}
}
