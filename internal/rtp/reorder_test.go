package rtp

import (
	"testing"

	"github.com/pion/rtp"

	"mediacheck/internal/fault"
)

func mkPacket(seq uint16, ts uint32) *rtp.Packet {
	return &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			SequenceNumber: seq,
			Timestamp:      ts,
			SSRC:           42,
			PayloadType:    96,
		},
		Payload: []byte{0x65, 0xAA},
	}
}

func seqs(out []OrderedPacket) []uint64 {
	got := make([]uint64, len(out))
	for i, p := range out {
		got[i] = p.ExpandedSeq
	}
	return got
}

func TestInOrderAndDuplicate(t *testing.T) {
	b := NewReorderBuffer(4)
	for i := uint16(0); i < 6; i++ {
		r := b.Push(mkPacket(i, uint32(i)*3600))
		if len(r.Gaps) != 0 {
			t.Fatalf("unexpected gaps: %+v", r.Gaps)
		}
		if len(r.Delivered) != 1 || r.Delivered[0].ExpandedSeq != uint64(i) {
			t.Fatalf("push %d delivered %v", i, seqs(r.Delivered))
		}
	}
	// Repeated delivery of an old seq is a duplicate, not a new packet.
	r := b.Push(mkPacket(2, 7200))
	if !r.Duplicate || len(r.Delivered) != 0 {
		t.Fatalf("duplicate handling failed: %+v", r)
	}
	f := b.Flush()
	if len(f.Gaps) != 0 || len(f.Delivered) != 0 {
		t.Fatalf("clean flush should be empty: %+v", f)
	}
	st := b.Stats()
	if st.Input != 7 || st.Delivered != 6 || st.Duplicates != 1 || st.MissingPackets != 0 {
		t.Fatalf("unexpected stats: %+v", st)
	}
}

func TestReorderThenRecover(t *testing.T) {
	b := NewReorderBuffer(8)
	order := []uint16{0, 1, 3, 2, 4}
	var delivered []uint64
	for _, s := range order {
		r := b.Push(mkPacket(s, uint32(s)*3600))
		for _, p := range r.Delivered {
			delivered = append(delivered, p.ExpandedSeq)
		}
	}
	f := b.Flush()
	for _, p := range f.Delivered {
		delivered = append(delivered, p.ExpandedSeq)
	}
	want := []uint64{0, 1, 2, 3, 4}
	if len(delivered) != len(want) {
		t.Fatalf("delivered %v, want %v", delivered, want)
	}
	for i := range want {
		if delivered[i] != want[i] {
			t.Fatalf("delivered %v, want %v", delivered, want)
		}
	}
	if b.Stats().MissingPackets != 0 {
		t.Fatal("recovered reorder must not count as loss")
	}
}

func TestMissingPacketOnFlush(t *testing.T) {
	b := NewReorderBuffer(8)
	b.Push(mkPacket(0, 0))
	b.Push(mkPacket(1, 3600))
	b.Push(mkPacket(4, 14400)) // 2 and 3 missing
	f := b.Flush()
	if len(f.Gaps) != 1 || f.Gaps[0].OverWindow ||
		f.Gaps[0].Seq != (fault.SeqRange{First: 2, Last: 3}) {
		t.Fatalf("want one ordinary merged gap 2..3, got %+v", f.Gaps)
	}
	// The contiguous prefix drains during Push; only seq 4 remains
	// buffered behind the holes and is delivered by Flush.
	if got := seqs(f.Delivered); len(got) != 1 || got[0] != 4 {
		t.Fatalf("buffered tail packet must be delivered: %v", got)
	}
	if st := b.Stats(); st.MissingPackets != 2 {
		t.Fatalf("missing count = %d, want 2", st.MissingPackets)
	}
}

func TestOverWindowGap(t *testing.T) {
	// Window 1: the buffer waits for exactly one outstanding seq. A
	// jump to seq 3 (two seqs missing) ages seq 1 and seq 2 out at once.
	b := NewReorderBuffer(1)
	b.Push(mkPacket(0, 0))
	r := b.Push(mkPacket(3, 10800))
	if len(r.Gaps) != 1 || !r.Gaps[0].OverWindow ||
		r.Gaps[0].Seq != (fault.SeqRange{First: 1, Last: 2}) {
		t.Fatalf("want over-window gap 1..2, got %+v", r.Gaps)
	}
	if got := seqs(r.Delivered); len(got) != 1 || got[0] != 3 {
		t.Fatalf("in-window packet should deliver: %v", got)
	}
	// Late arrivals for seqs already reported are rejected.
	for _, lateSeq := range []uint16{1, 2} {
		late := b.Push(mkPacket(lateSeq, 3600))
		if !late.Duplicate {
			t.Fatalf("aged-out seq %d must be rejected", lateSeq)
		}
	}
	f := b.Flush()
	if len(f.Gaps) != 0 {
		t.Fatalf("stream end must not double count over-window gaps: %+v", f.Gaps)
	}
	if st := b.Stats(); st.OverWindowGaps != 2 || st.MissingPackets != 2 {
		t.Fatalf("over-window stats wrong: %+v", st)
	}
}

func TestOverWindowThenInWindowRecovery(t *testing.T) {
	// Window 2: seq 3 forces only seq 1 out; seq 2 still fits and a
	// late arrival recovers it without any additional gap.
	b := NewReorderBuffer(2)
	b.Push(mkPacket(0, 0))
	r := b.Push(mkPacket(3, 10800))
	if len(r.Gaps) != 1 || !r.Gaps[0].OverWindow ||
		r.Gaps[0].Seq != (fault.SeqRange{First: 1, Last: 1}) {
		t.Fatalf("want single over-window gap at 1, got %+v", r.Gaps)
	}
	r = b.Push(mkPacket(2, 7200))
	// Filling the hole unblocks the already-buffered seq 3 as well.
	if got := seqs(r.Delivered); len(got) != 2 || got[0] != 2 || got[1] != 3 {
		t.Fatalf("late in-window packet should drain chain: %v", got)
	}
	if len(r.Gaps) != 0 {
		t.Fatalf("recovery must not emit gaps: %+v", r.Gaps)
	}
	if st := b.Stats(); st.MissingPackets != 1 || st.OverWindowGaps != 1 {
		t.Fatalf("stats wrong after recovery: %+v", st)
	}
}

func TestSequenceWraparound(t *testing.T) {
	b := NewReorderBuffer(4)
	for i := uint32(0xFFFC); i < 0x10003; i++ {
		r := b.Push(mkPacket(uint16(i), i*3600))
		if len(r.Gaps) != 0 {
			t.Fatalf("unexpected gap at wrap: %+v", r.Gaps)
		}
		if len(r.Delivered) != 1 || r.Delivered[0].ExpandedSeq != uint64(i) {
			t.Fatalf("expanded seq at %#x = %v", i, seqs(r.Delivered))
		}
	}
	f := b.Flush()
	if len(f.Gaps) != 0 || len(f.Delivered) != 0 {
		t.Fatalf("wrap stream must close cleanly: %+v", f)
	}
}

func TestDecodeDatagramTruncated(t *testing.T) {
	if _, err := DecodeDatagram([]byte{0x80, 0x60}); err == nil {
		t.Fatal("short datagram must fail parsing")
	}
	raw, err := EncodeDatagram(mkPacket(7, 123))
	if err != nil {
		t.Fatal(err)
	}
	p, err := DecodeDatagram(raw)
	if err != nil {
		t.Fatal(err)
	}
	if p.SequenceNumber != 7 || p.Timestamp != 123 {
		t.Fatalf("round trip lost fields: %+v", p)
	}
}
