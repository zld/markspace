package rtp

import (
	"encoding/binary"
	"testing"

	"github.com/pion/rtp"

	"rtx/internal/model"
)

func encodePacket(seq uint16, ts uint32) []byte {
	p := rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: seq, Timestamp: ts, SSRC: 7}}
	raw, err := p.Marshal()
	if err != nil {
		panic(err)
	}
	out := binary.BigEndian.AppendUint16(nil, uint16(len(raw)))
	return append(out, raw...)
}

func trace(pkts ...[]byte) []byte {
	var out []byte
	for _, p := range pkts {
		out = append(out, p...)
	}
	return out
}

func TestDecodeDuplicateAndReorder(t *testing.T) {
	buf := trace(
		encodePacket(1, 100),
		encodePacket(3, 300),
		encodePacket(2, 200), // reordered, inside window
		encodePacket(2, 200), // duplicate
		encodePacket(4, 400),
	)
	res, err := Decode(buf, 8, model.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if res.Stats.Total != 5 || res.Stats.Duplicates != 1 || res.Stats.Reordered != 1 {
		t.Fatalf("stats %+v", res.Stats)
	}
	if len(res.Packets) != 4 {
		t.Fatalf("want 4 packets, got %d", len(res.Packets))
	}
	for i, want := range []uint16{1, 2, 3, 4} {
		if res.Packets[i].Seq != want {
			t.Fatalf("order[%d]=%d want %d", i, res.Packets[i].Seq, want)
		}
	}
	var dup bool
	for _, a := range res.Anomalies {
		if a.Reason == model.ReasonDuplicate && a.RawValue == 2 {
			dup = true
		}
	}
	if !dup {
		t.Fatal("duplicate anomaly missing")
	}
}

func TestDecodeLatePacketExcluded(t *testing.T) {
	var recs [][]byte
	for s := uint16(1); s <= 6; s++ {
		recs = append(recs, encodePacket(s, uint32(s)*100))
	}
	recs = append(recs, encodePacket(1, 100)) // duplicate of 1
	recs = append(recs, encodePacket(0, 0))   // far behind: late
	buf := trace(recs...)
	res, err := Decode(buf, 2, model.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if res.Stats.Late != 1 {
		t.Fatalf("want 1 late packet, stats %+v", res.Stats)
	}
	for _, p := range res.Packets {
		if p.Seq == 0 {
			t.Fatal("late packet entered transform set")
		}
	}
}

func TestDecodeTruncatedRecord(t *testing.T) {
	buf := trace(encodePacket(1, 100), encodePacket(2, 200))
	buf = buf[:len(buf)-3] // cut the second record
	res, err := Decode(buf, 8, model.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Packets) != 1 {
		t.Fatalf("want 1 surviving packet, got %d", len(res.Packets))
	}
	if len(res.Anomalies) != 1 || res.Anomalies[0].Reason != model.ReasonTruncatedBlock {
		t.Fatalf("want truncation anomaly, got %+v", res.Anomalies)
	}
	if res.Anomalies[0].Offset <= 0 {
		t.Fatalf("truncation offset must be raw byte offset, got %d", res.Anomalies[0].Offset)
	}
}

func TestUnwrapAcrossWrapBoundary(t *testing.T) {
	pkts := []Packet{
		{Seq: 1, Timestamp: 0xFFFFFFF0},
		{Seq: 2, Timestamp: 0x00000010}, // wrapped: +0x20
		{Seq: 3, Timestamp: 0x00000030},
	}
	res := Unwrap(pkts)
	if len(res.Anomalies) != 0 || len(res.Isolated) != 0 {
		t.Fatalf("unexpected anomalies %+v", res.Anomalies)
	}
	want := []uint64{0xFFFFFFF0, 0x100000010, 0x100000030}
	for i, p := range res.Points {
		if p.Unwrapped != want[i] {
			t.Fatalf("point %d unwrapped=%x want %x", i, p.Unwrapped, want[i])
		}
		if p.Raw != pkts[i].Timestamp {
			t.Fatalf("point %d raw value rewritten", i)
		}
	}
}

func TestUnwrapBackwardsIsolates(t *testing.T) {
	pkts := []Packet{
		{Seq: 1, Timestamp: 1000},
		{Seq: 2, Timestamp: 500}, // backwards
		{Seq: 3, Timestamp: 1100},
		{Seq: 4, Timestamp: 1200},
	}
	res := Unwrap(pkts)
	var backwards bool
	for _, a := range res.Anomalies {
		if a.Reason == model.ReasonBackwards && a.RawValue == 500 {
			backwards = true
		}
	}
	if !backwards {
		t.Fatalf("backwards anomaly missing: %+v", res.Anomalies)
	}
	if len(res.Isolated) == 0 {
		t.Fatal("affected range not isolated")
	}
	// The bad packet must not contribute a timing point.
	for _, p := range res.Points {
		if p.Raw == 500 {
			t.Fatal("backwards packet leaked into timing points")
		}
	}
}

func TestUnwrapSeqGapIsolates(t *testing.T) {
	pkts := []Packet{
		{Seq: 1, Timestamp: 100},
		{Seq: 5, Timestamp: 500}, // gap
		{Seq: 6, Timestamp: 600},
	}
	res := Unwrap(pkts)
	var gap bool
	for _, a := range res.Anomalies {
		if a.Reason == model.ReasonSeqGap {
			gap = true
		}
	}
	if !gap || len(res.Isolated) == 0 {
		t.Fatalf("gap not isolated: %+v %+v", res.Anomalies, res.Isolated)
	}
}
