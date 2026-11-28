package rtp

import (
	"testing"

	"github.com/pion/rtp"

	merr "mediacheck/internal/errors"
)

func datagram(t *testing.T, ssrc uint32, seq uint16, ts uint32, payload []byte) []byte {
	t.Helper()
	pkt := &rtp.Packet{
		Header: rtp.Header{
			Version: 2, PayloadType: 96,
			SequenceNumber: seq, Timestamp: ts, SSRC: ssrc,
		},
		Payload: payload,
	}
	raw, err := pkt.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func payloadOf(seq int) []byte {
	return []byte{byte(seq >> 8), byte(seq), 0xab, 0xcd}
}

func TestReorderInAndOutOfOrder(t *testing.T) {
	b := NewReorderBuffer(8)
	const ssrc = 0x11111111
	// Arrival order: 0,2,1,3. Draining after each push models a streaming
	// receiver; the final order must be 0,1,2,3.
	var emitted []OrderedPacket
	for _, seq := range []uint16{0, 2, 1, 3} {
		if err := b.Push(datagram(t, ssrc, seq, uint32(seq)*160, payloadOf(int(seq)))); err != nil {
			t.Fatal(err)
		}
		emitted = append(emitted, b.Drain()...)
	}
	out := emitted
	if len(out) != 4 {
		t.Fatalf("emitted %d; want 4", len(out))
	}
	for i, op := range out {
		if op.ExtSequence != uint64(i) || op.Sequence != uint16(i) {
			t.Fatalf("position %d got ext=%d seq=%d", i, op.ExtSequence, op.Sequence)
		}
		if op.SSRC != ssrc || op.Timestamp != uint32(i)*160 {
			t.Fatalf("ssrc/timestamp not preserved at %d", i)
		}
	}
}

func TestDuplicateDoesNotAdvanceClock(t *testing.T) {
	b := NewReorderBuffer(8)
	const ssrc = 0x22222222
	raw := datagram(t, ssrc, 5, 800, payloadOf(5))
	if err := b.Push(raw); err != nil {
		t.Fatal(err)
	}
	if err := b.Push(raw); err != nil {
		t.Fatalf("identical duplicate must be benign, got %v", err)
	}
	out := b.Drain()
	if len(out) != 1 || out[0].ExtSequence != 5 {
		t.Fatalf("duplicate moved the clock: %+v", out)
	}
}

func TestSameSequenceDifferentContentConflicts(t *testing.T) {
	b := NewReorderBuffer(8)
	const ssrc = 0x33333333
	if err := b.Push(datagram(t, ssrc, 7, 100, payloadOf(7))); err != nil {
		t.Fatal(err)
	}
	err := b.Push(datagram(t, ssrc, 7, 100, []byte("totally different")))
	e, ok := merr.As(err)
	if !ok || e.Code != merr.CodeRTPConflict {
		t.Fatalf("got %v; want RTP_CONTENT_CONFLICT", err)
	}
	if e.RawTimestamp != 100 || e.RawTimescale == 0 {
		t.Fatal("conflict must carry the original timestamp")
	}
}

func TestSequenceRolloverExtendsMonotonically(t *testing.T) {
	b := NewReorderBuffer(16)
	const ssrc = 0x44444444
	seqs := []uint16{0xfffe, 0xffff, 0, 1} // wrap boundary, shuffled lightly
	for _, seq := range seqs {
		if err := b.Push(datagram(t, ssrc, seq, uint32(seq), payloadOf(int(seq)))); err != nil {
			t.Fatal(err)
		}
	}
	out := b.Drain()
	want := []uint64{0xfffe, 0xffff, 0x10000, 0x10001}
	if len(out) != len(want) {
		t.Fatalf("emitted %d; want %d", len(out), len(want))
	}
	for i, op := range out {
		if op.ExtSequence != want[i] {
			t.Fatalf("position %d ext=%d; want %d", i, op.ExtSequence, want[i])
		}
	}
}

func TestGapBeyondWindowIsLocatableAndRecoverable(t *testing.T) {
	b := NewReorderBuffer(4)
	const ssrc = 0x55555555
	// Sequences 0,1 then jump to 10..14: 2..9 must become a located gap and
	// the stream resumes from 10.
	for _, seq := range []uint16{0, 1, 10, 11, 12, 13, 14} {
		if err := b.Push(datagram(t, ssrc, seq, uint32(seq)*160, payloadOf(int(seq)))); err != nil {
			t.Fatal(err)
		}
	}
	out := b.Drain()
	gaps := b.Gaps(ssrc)
	if len(gaps) != 1 {
		t.Fatalf("gaps = %+v; want exactly 1", gaps)
	}
	if gaps[0].FromExt != 2 || gaps[0].ToExt != 9 || gaps[0].Count != 8 {
		t.Fatalf("gap range wrong: %+v", gaps[0])
	}
	// All present packets still emitted; post-gap ones flagged recovered.
	if len(out) != 7 {
		t.Fatalf("emitted %d; want 7", len(out))
	}
	var recovered int
	for _, op := range out {
		if op.Recovered {
			recovered++
		}
	}
	if recovered != 5 {
		t.Fatalf("recovered packets = %d; want 5", recovered)
	}
	if b.RecoveredCount(ssrc) != 5 {
		t.Fatal("RecoveredCount mismatch")
	}
}

func TestSSRCsAreIndependentStreams(t *testing.T) {
	b := NewReorderBuffer(8)
	if err := b.Push(datagram(t, 1, 0, 0, payloadOf(0))); err != nil {
		t.Fatal(err)
	}
	if err := b.Push(datagram(t, 2, 0, 0, payloadOf(0))); err != nil {
		t.Fatal(err)
	}
	out := b.Drain()
	ssrcs := map[uint32]bool{}
	for _, op := range out {
		ssrcs[op.SSRC] = true
	}
	if len(ssrcs) != 2 {
		t.Fatalf("SSRC streams merged: %+v", ssrcs)
	}
}
