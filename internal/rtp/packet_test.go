package rtp

import (
	"testing"

	"github.com/pion/rtp"

	"mediad/internal/fault"
)

func marshal(t *testing.T, seq uint16, ts uint32, payload []byte) []byte {
	t.Helper()
	p := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    96,
			SequenceNumber: seq,
			Timestamp:      ts,
			SSRC:           4242,
		},
		Payload: payload,
	}
	wire, err := p.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return wire
}

func TestParseValidAndMalformed(t *testing.T) {
	wire := marshal(t, 1, 1000, []byte{1, 2, 3, 4})
	p, err := Parse(wire, 77)
	if err != nil {
		t.Fatal(err)
	}
	if p.SequenceNumber != 1 || p.Timestamp != 1000 || p.StreamOffset != 77 {
		t.Fatalf("parsed fields wrong: %+v", p)
	}
	if string(p.Payload) != string([]byte{1, 2, 3, 4}) {
		t.Fatal("payload mismatch")
	}
	if _, err := Parse(wire[:4], 88); err == nil {
		t.Fatal("truncated RTP packet must fail")
	} else if fe, ok := fault.As(err); !ok || fe.Category != fault.CatRTPPacket || fe.Offset != 88 {
		t.Fatalf("wrong fault for truncated packet: %v", err)
	}
	bad := append([]byte(nil), wire...)
	bad[0] &^= 0xC0 // force RTP version 0
	if _, err := Parse(bad, 0); err == nil {
		t.Fatal("non-v2 packet must be rejected")
	}
}

func TestInOrderAndDuplicate(t *testing.T) {
	rb := NewReorderBuffer(48000, 1, 8)
	for i := uint16(0); i < 10; i++ {
		if err := rb.Push(mustParse(t, i, uint32(i*100), []byte{byte(i)})); err != nil {
			t.Fatal(err)
		}
	}
	// Retransmit packet 5: counted as duplicate but not re-delivered.
	if err := rb.Push(mustParse(t, 5, 500, []byte{5})); err != nil {
		t.Fatal(err)
	}
	out, gaps := rb.Drain()
	if len(gaps) != 0 || len(out) != 10 {
		t.Fatalf("got %d packets, %d gaps", len(out), len(gaps))
	}
	if rb.DuplicateCount() != 1 {
		t.Fatalf("duplicate count = %d want 1", rb.DuplicateCount())
	}
	for i, p := range out {
		if p.SequenceNumber != uint16(i) {
			t.Fatalf("order broken at %d: seq %d", i, p.SequenceNumber)
		}
	}
}

func TestReorderingWithinWindow(t *testing.T) {
	rb := NewReorderBuffer(48000, 1, 8)
	order := []uint16{0, 1, 3, 2, 4, 7, 5, 6, 8}
	for _, seq := range order {
		if err := rb.Push(mustParse(t, seq, uint32(seq), nil)); err != nil {
			t.Fatal(err)
		}
	}
	out, gaps := rb.Drain()
	if len(gaps) != 0 || len(out) != 9 {
		t.Fatalf("got %d packets, %d gaps", len(out), len(gaps))
	}
	for i, p := range out {
		if p.SequenceNumber != uint16(i) {
			t.Fatalf("reorder failed at %d: %d", i, p.SequenceNumber)
		}
	}
}

func TestGapDetectedAfterWindow(t *testing.T) {
	rb := NewReorderBuffer(48000, 1, 4)
	// Sequence 5 is missing; push 0..4 then jump far enough to age the hole.
	for seq := uint16(0); seq < 5; seq++ {
		if err := rb.Push(mustParse(t, seq, uint32(seq), nil)); err != nil {
			t.Fatal(err)
		}
	}
	if _, gaps := rb.Drain(); len(gaps) != 0 {
		t.Fatalf("unexpected early gaps: %+v", gaps)
	}
	// Push past the reorder window (maxReorder=4): once seq 10 is queued the
	// hole at seq 5 can no longer be filled in time.
	for seq := uint16(6); seq <= 10; seq++ {
		if err := rb.Push(mustParse(t, seq, uint32(seq), nil)); err != nil {
			t.Fatal(err)
		}
	}
	out, gaps := rb.Drain()
	if len(gaps) != 1 || gaps[0].AbsSeqStart != 5 || gaps[0].AbsSeqEnd != 6 {
		t.Fatalf("gap detection wrong: %+v", gaps)
	}
	if rb.GapCount() != 1 {
		t.Fatalf("gap count = %d", rb.GapCount())
	}
	if len(out) != 5 || out[0].SequenceNumber != 6 || out[4].SequenceNumber != 10 {
		t.Fatalf("drained %d packets: %+v", len(out), out)
	}
}

func TestSequenceAndTimestampWraparound(t *testing.T) {
	rb := NewReorderBuffer(48000, 1, 8)
	seqs := []uint16{0xFFFE, 0xFFFF, 0x0000, 0x0001, 0x0002}
	tss := []uint32{0xFFFFFFF8, 0x00000000, 0x00000008, 0x00000010, 0x00000018}
	for i := range seqs {
		if err := rb.Push(mustParse(t, seqs[i], tss[i], nil)); err != nil {
			t.Fatal(err)
		}
	}
	out, gaps := rb.Flush()
	if len(gaps) != 0 || len(out) != 5 {
		t.Fatalf("wrap handling produced %d packets, %d gaps", len(out), len(gaps))
	}
	// Absolute frame cursor must advance monotonically through the TS wrap.
	// The first observed timestamp is 0xFFFFFFF8; five packets advance 32
	// units total, landing at 0x100000018.
	if got := rb.Timeline().AbsTimestamp(); got != 0x100000018 {
		t.Fatalf("absolute timestamp = %#x", got)
	}
	if got := rb.Timeline().AbsFrame(); got != 0x100000018 {
		t.Fatalf("absolute frame = %#x", got)
	}
}

func TestFlushReportsTrailingHole(t *testing.T) {
	rb := NewReorderBuffer(48000, 1, 32)
	if err := rb.Push(mustParse(t, 0, 0, nil)); err != nil {
		t.Fatal(err)
	}
	if early, gaps := rb.Drain(); len(early) != 1 || len(gaps) != 0 {
		t.Fatalf("early drain wrong: %d %+v", len(early), gaps)
	}
	if err := rb.Push(mustParse(t, 2, 20, nil)); err != nil {
		t.Fatal(err)
	}
	out, gaps := rb.Flush()
	if len(out) != 1 || out[0].SequenceNumber != 2 {
		t.Fatalf("flush packets wrong: %d", len(out))
	}
	if len(gaps) != 1 || gaps[0].AbsSeqStart != 1 || gaps[0].AbsSeqEnd != 2 {
		t.Fatalf("flush result wrong: packets=%d gaps=%+v", len(out), gaps)
	}
}

func mustParse(t *testing.T, seq uint16, ts uint32, payload []byte) *Packet {
	t.Helper()
	p, err := Parse(marshal(t, seq, ts, payload), int64(seq)*100)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
