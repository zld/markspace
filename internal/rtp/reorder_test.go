package rtp

import (
	"testing"

	"github.com/pion/rtp"

	"mediaverify/internal/errs"
)

func mkPacket(ssrc uint32, seq uint16, ts uint32, payload byte) *rtp.Packet {
	return &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    96,
			SSRC:           ssrc,
			SequenceNumber: seq,
			Timestamp:      ts,
			Marker:         seq%10 == 9,
		},
		Payload: []byte{payload, payload, payload},
	}
}

func TestExtendSequenceAndWrap(t *testing.T) {
	e := NewExtender()
	first, err := e.Extend(mkPacket(1, 65535, 1000, 1))
	if err != nil {
		t.Fatal(err)
	}
	if first.ExtSeq != 65535 {
		t.Fatalf("first ext seq = %d", first.ExtSeq)
	}
	second, err := e.Extend(mkPacket(1, 0, 1060, 2))
	if err != nil {
		t.Fatal(err)
	}
	if second.ExtSeq != 1<<16 {
		t.Fatalf("after wrap ext seq = %d, want %d", second.ExtSeq, 1<<16)
	}
	// Late packet from before the wrap keeps the old cycle.
	late, err := e.Extend(mkPacket(1, 65534, 940, 3))
	if err != nil {
		t.Fatal(err)
	}
	if late.ExtSeq != 65534 {
		t.Fatalf("late reordered ext seq = %d, want 65534", late.ExtSeq)
	}
}

func TestDedupReorderAndGaps(t *testing.T) {
	e := NewExtender()
	buf := NewReorderBuffer(4)
	ssrc := uint32(42)
	offer := func(seq uint16) OfferResult {
		p, err := e.Extend(mkPacket(ssrc, seq, uint32(seq)*160, byte(seq)))
		if err != nil {
			t.Fatal(err)
		}
		r, err := buf.Offer(p)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}

	r := offer(0)
	if len(r.Emitted) != 1 || r.Emitted[0].ExtSeq != 0 {
		t.Fatalf("seq 0 should emit immediately, got %+v", r.Emitted)
	}
	// Out of order: 2 then 1, buffered until 1 arrives.
	if r := offer(2); len(r.Emitted) != 0 {
		t.Fatalf("seq 2 must wait, emitted %d", len(r.Emitted))
	}
	r = offer(1)
	if len(r.Emitted) != 2 || r.Emitted[0].ExtSeq != 1 || r.Emitted[1].ExtSeq != 2 {
		t.Fatalf("seq 1 should release 1 and 2, got %+v", r.Emitted)
	}
	// Exact duplicate: counted, never re-enters the stream.
	dup := offer(1)
	if !dup.Duplicate || len(dup.Emitted) != 0 {
		t.Fatalf("duplicate handling wrong: %+v", dup)
	}
	// Burst loss: jump far enough to push missing 3.. out of the window.
	offer(10)
	offer(11)
	offer(12)
	offer(13)
	offer(14) // highest=14, window 4 -> limit 10; 3..9 become a gap
	st := buf.SnapshotStats()[ssrc]
	if st.Lost != 7 {
		t.Fatalf("want 7 lost (3..9), got %d", st.Lost)
	}
	if st.Gaps != 1 {
		t.Fatalf("want 1 gap run, got %d", st.Gaps)
	}
	// Late packet for a sealed sequence is rejected as sealed duplicate.
	late := offer(3)
	if !late.Sealed {
		t.Fatalf("late packet for sealed seq must be flagged sealed")
	}
	st2 := buf.SnapshotStats()[ssrc]
	if st2.Duplicate < 2 {
		t.Fatalf("want at least 2 duplicates counted, got %d", st2.Duplicate)
	}
	if st2.Reordered == 0 {
		t.Fatal("reordered counter never advanced")
	}
}

func TestSSRCIsolation(t *testing.T) {
	e := NewExtender()
	buf := NewReorderBuffer(8)
	for _, ssrc := range []uint32{1, 2} {
		p, _ := e.Extend(mkPacket(ssrc, 0, 0, 1))
		if _, err := buf.Offer(p); err != nil {
			t.Fatal(err)
		}
	}
	if len(buf.SnapshotStats()) != 2 {
		t.Fatalf("want 2 independent stream states, got %d", len(buf.SnapshotStats()))
	}
}

func TestCacheLimit(t *testing.T) {
	e := NewExtender()
	buf := NewReorderBuffer(1000)
	buf.maxBytes = 2 // smaller than one 3-byte payload
	if _, err := buf.Offer(mustExtend(t, e, 9, 0)); err != nil {
		t.Fatalf("first packet emits immediately: %v", err)
	}
	// seq 2 stays buffered waiting for seq 1, so it hits the budget.
	_, err := buf.Offer(mustExtend(t, e, 9, 2))
	if err == nil || errs.Class(err) != errs.KindLimitExceeded {
		t.Fatalf("want limit_exceeded, got %v", err)
	}
}

func mustExtend(t *testing.T, e *Extender, ssrc uint32, seq uint16) Packet {
	t.Helper()
	p, err := e.Extend(mkPacket(ssrc, seq, uint32(seq)*160, byte(seq)))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTimestampBackwardJump(t *testing.T) {
	e := NewExtender()
	if _, err := e.Extend(mkPacket(1, 0, 10000, 1)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Extend(mkPacket(1, 1, 9000, 2)); err != nil {
		t.Fatal(err)
	}
	if e.BackwardJumps != 1 {
		t.Fatalf("want 1 backward jump, got %d", e.BackwardJumps)
	}
	// A full 32-bit wrap is normal, not a jump.
	e2 := NewExtender()
	_, _ = e2.Extend(mkPacket(1, 0, 4_294_967_000, 1))
	_, _ = e2.Extend(mkPacket(1, 1, 500, 2))
	if e2.BackwardJumps != 0 {
		t.Fatalf("32-bit wrap misclassified: %d jumps", e2.BackwardJumps)
	}
}

func TestParseBadPacket(t *testing.T) {
	e := NewExtender()
	if _, err := e.Parse([]byte{0x00}); err == nil || errs.Class(err) != errs.KindInvalidInput {
		t.Fatalf("want invalid_input for corrupt packet, got %v", err)
	}
}

func TestVerifyChecksumClass(t *testing.T) {
	e := NewExtender()
	good, err := e.Extend(mkPacket(1, 0, 0, 7))
	if err != nil {
		t.Fatal(err)
	}
	// mkPacket payload has no CRC, so the final bytes are content; this is
	// itself a checksum failure under the verified layout.
	if err := VerifyChecksum(good); errs.Class(err) != errs.KindChecksum {
		t.Fatalf("want KindChecksum, got %v", err)
	}
	short := good
	short.Payload = []byte{1}
	if err := VerifyChecksum(short); errs.Class(err) != errs.KindChecksum {
		t.Fatalf("short payload want KindChecksum, got %v", err)
	}
}
