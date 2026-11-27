package rtp

import (
	"testing"

	"github.com/pion/rtp"

	"mediasync/internal/model"
)

func marshalPacket(t *testing.T, seq uint16, payload []byte) []byte {
	t.Helper()
	pkt := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    111,
			SequenceNumber: seq,
			SSRC:           4242,
			Timestamp:      uint32(seq) * 480,
		},
		Payload: payload,
	}
	b, err := pkt.Marshal()
	if err != nil {
		t.Fatalf("marshal seq=%d: %v", seq, err)
	}
	return b
}

func raw(t *testing.T, off int, seq uint16, n int) model.RawPacket {
	return model.RawPacket{Offset: off, RTP: marshalPacket(t, seq, make([]byte, n))}
}

func TestReconstructOrderedClean(t *testing.T) {
	pkts := []model.RawPacket{raw(t, 0, 10, 5), raw(t, 1, 11, 6), raw(t, 2, 12, 7)}
	res, err := Reconstruct("t", pkts)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Gaps) != 0 || len(res.Failures) != 0 {
		t.Fatalf("clean stream wants no gaps/failures, got %+v %+v", res.Gaps, res.Failures)
	}
	for i, r := range res.Records {
		if r.ExtSeq != uint64(10+i) || r.Duplicate {
			t.Fatalf("record %d = %+v", i, r)
		}
	}
	if res.FirstExtSeq != 10 || res.LastExtSeq != 12 {
		t.Fatalf("range = %d..%d", res.FirstExtSeq, res.LastExtSeq)
	}
}

func TestReconstructWrap(t *testing.T) {
	pkts := []model.RawPacket{
		raw(t, 0, 0xFFFE, 1),
		raw(t, 1, 0xFFFF, 1),
		raw(t, 2, 0x0000, 1),
		raw(t, 3, 0x0001, 1),
	}
	res, err := Reconstruct("t", pkts)
	if err != nil {
		t.Fatal(err)
	}
	want := []uint64{0xFFFE, 0xFFFF, 0x10000, 0x10001}
	for i, r := range res.Records {
		if r.ExtSeq != want[i] {
			t.Fatalf("rec %d ext=%d want %d", i, r.ExtSeq, want[i])
		}
	}
	if len(res.Gaps) != 0 {
		t.Fatalf("wrap must not create gaps: %+v", res.Gaps)
	}
}

func TestReconstructOutOfOrderAndDuplicate(t *testing.T) {
	// Arrival order 20, 22, 21, 20(dup).
	pkts := []model.RawPacket{
		raw(t, 0, 20, 1),
		raw(t, 1, 22, 1),
		raw(t, 2, 21, 1),
		raw(t, 3, 20, 1),
	}
	res, err := Reconstruct("t", pkts)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Records) != 4 {
		t.Fatalf("records=%d", len(res.Records))
	}
	// Sorted by (ext seq, offset): 20@0, 20@3(dup), 21, 22.
	if res.Records[0].ExtSeq != 20 || res.Records[0].Offset != 0 || res.Records[0].Duplicate {
		t.Fatalf("first = %+v", res.Records[0])
	}
	if res.Records[1].ExtSeq != 20 || !res.Records[1].Duplicate || res.Records[1].Offset != 3 {
		t.Fatalf("duplicate = %+v", res.Records[1])
	}
	if res.Records[2].ExtSeq != 21 || res.Records[3].ExtSeq != 22 {
		t.Fatalf("ordering = %+v", res.Records)
	}
	if len(res.Gaps) != 0 {
		t.Fatalf("unexpected gaps: %+v", res.Gaps)
	}
	if len(res.Failures) != 1 || res.Failures[0].PacketOffset != 3 {
		t.Fatalf("duplicate failure = %+v", res.Failures)
	}
}

func TestReconstructGaps(t *testing.T) {
	// 5, 8, 9 => gaps 6..7.
	pkts := []model.RawPacket{raw(t, 0, 5, 1), raw(t, 1, 8, 1), raw(t, 2, 9, 1)}
	res, err := Reconstruct("t", pkts)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Gaps) != 1 || res.Gaps[0].From != 6 || res.Gaps[0].To != 7 {
		t.Fatalf("gaps = %+v", res.Gaps)
	}
}

func TestReconstructTruncatedPacketLocated(t *testing.T) {
	pkts := []model.RawPacket{
		raw(t, 0, 30, 2),
		{Offset: 1, RTP: []byte{0x80, 0x60, 0x00, 0x1F}}, // header only, no payload bytes
		{Offset: 2, RTP: []byte{0x01}},                   // too short to even read seq
		raw(t, 3, 32, 2),
	}
	res, err := Reconstruct("t", pkts)
	if err != nil {
		t.Fatal(err)
	}
	var offsets []int
	for _, f := range res.Failures {
		offsets = append(offsets, f.PacketOffset)
	}
	if len(offsets) != 2 || offsets[0] != 1 || offsets[1] != 2 {
		t.Fatalf("failure offsets = %v", offsets)
	}
	// The truncated-but-sequenced packet keeps its slot; the 4-byte one is seq 31.
	seqs := map[uint64]bool{}
	for _, r := range res.Records {
		seqs[r.ExtSeq] = true
	}
	if !seqs[31] {
		t.Fatalf("truncated seq-31 packet should occupy a slot: %+v", res.Records)
	}
}

func TestReconstructLimits(t *testing.T) {
	oversized := model.RawPacket{Offset: 0, RTP: make([]byte, model.MaxPacketBytes+1)}
	res, err := Reconstruct("t", []model.RawPacket{oversized})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Failures) != 1 || res.Failures[0].Code != model.ErrPacketTooLarge {
		t.Fatalf("oversize failure = %+v", res.Failures)
	}

	many := make([]model.RawPacket, model.MaxPacketsPerTrack+1)
	for i := range many {
		many[i] = raw(t, i, uint16(i), 1)
	}
	if _, err := Reconstruct("t", many); err == nil {
		t.Fatal("expected too-many-packets error")
	}
}
