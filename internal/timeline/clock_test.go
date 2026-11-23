package timeline

import (
	"testing"

	"github.com/pion/rtp"
	"mediapack/internal/model"
)

func pkt(ts uint32, seq uint16) rtp.Packet {
	return rtp.Packet{Header: rtp.Header{
		Version: 2, PayloadType: 96, SequenceNumber: seq, Timestamp: ts,
		SSRC: 1, Marker: true,
	}, Payload: []byte{0xaa}}
}

func table(id uint32, rate uint32, ts ...uint32) *ClockTable {
	ct := &ClockTable{TrackID: id, ClockRate: rate, BaseTS: ts[0]}
	for i, t := range ts {
		ct.Packets = append(ct.Packets, pkt(t, uint16(i)))
		ct.Offsets = append(ct.Offsets, int64(i*13))
	}
	return ct
}

func TestExpandWraparound(t *testing.T) {
	// 90000 Hz clock: advance 45000 then wrap near the 32-bit ceiling.
	high := uint32(0xFFFFFFFF - 1000) // variable: uint32 add wraps at runtime
	wrapped := high + 45000
	ct := table(1, 90000, high-45000, high, wrapped)
	var es model.Errors
	tl, err := ct.Expand(&es)
	if err != nil {
		t.Fatal(err)
	}
	if tl.WrapCount != 1 {
		t.Fatalf("wrap count = %d, want 1", tl.WrapCount)
	}
	if tl.Events[2].Kind != EvtWrap {
		t.Fatalf("third event must be wrap, got %s", tl.Events[2].Kind)
	}
	// Unwrapped must stay monotonic and reflect a 45000-tick advance.
	if tl.Events[2].UnwrappedTS-tl.Events[1].UnwrappedTS != 45000 {
		t.Fatalf("unwrapped delta wrong: %d", tl.Events[2].UnwrappedTS-tl.Events[1].UnwrappedTS)
	}
	if tl.Events[1].MasterNS != 500_000_000 { // 45000/90000 s
		t.Fatalf("master ns = %d, want 500ms", tl.Events[1].MasterNS)
	}
}

func TestExpandDuplicateAndRegression(t *testing.T) {
	ct := table(2, 1000, 100, 110, 110, 105)
	var es model.Errors
	tl, _ := ct.Expand(&es)
	if tl.DupCount != 1 || tl.Events[2].Kind != EvtDuplicate {
		t.Fatalf("duplicate classification failed: %+v", tl.Events)
	}
	if tl.Events[3].Kind != EvtRegression {
		t.Fatalf("regression classification failed: %s", tl.Events[3].Kind)
	}
	if es.Len() == 0 {
		t.Fatal("regression must produce a structured error")
	}
	pe := es.Sorted()[0]
	if pe.Code != model.CodeTimestampBack || pe.TrackID != 2 || pe.ByteRange.Offset != 39 {
		t.Fatalf("regression error wrong: %+v", pe)
	}
}

func TestEstimateDriftKnownRate(t *testing.T) {
	// Clock runs 1% fast: nominal 1 s at 1000 Hz should be 1000 ticks but we
	// observe 1010.
	ct := table(3, 1000, 0, 1010)
	var es model.Errors
	tl, _ := ct.Expand(&es)
	d, err := EstimateDrift(tl, 1_000_000_000) // nominal exactly 1 s
	if err != nil {
		t.Fatal(err)
	}
	if d.PPM != 10000 { // 1% = 10000 ppm
		t.Fatalf("ppm = %d, want 10000 (ratio=%v)", d.PPM, d.RateRatio)
	}
}

func TestVFRTimelineExact(t *testing.T) {
	// VFR: irregular gaps 100ms, 250ms, 50ms at 1000 Hz.
	ct := table(4, 1000, 0, 100, 350, 400)
	var es model.Errors
	tl, err := ct.Expand(&es)
	if err != nil {
		t.Fatal(err)
	}
	want := []int64{0, 100_000_000, 350_000_000, 400_000_000}
	for i, ev := range tl.Events {
		if ev.MasterNS != want[i] {
			t.Fatalf("event %d master ns = %d, want %d", i, ev.MasterNS, want[i])
		}
	}
}

func TestAlignCommonPoints(t *testing.T) {
	a := table(1, 1000, 0, 100, 200, 300)
	b := table(2, 1000, 0, 100, 200) // ends earlier; shared points 0,100,200
	var es model.Errors
	mt, err := Align([]*ClockTable{a, b}, nil, &es)
	if err != nil {
		t.Fatal(err)
	}
	if mt.MasterTrack != 1 {
		t.Fatalf("master track = %d, want longer track 1", mt.MasterTrack)
	}
	want := []int64{0, 100_000_000, 200_000_000}
	if len(mt.CommonPoints) != len(want) {
		t.Fatalf("common points = %v, want %v", mt.CommonPoints, want)
	}
	for i := range want {
		if mt.CommonPoints[i] != want[i] {
			t.Fatalf("common point %d = %d, want %d", i, mt.CommonPoints[i], want[i])
		}
	}
	if ns, ok := mt.BoundaryAt(150_000_000, true); !ok || ns != 200_000_000 {
		t.Fatalf("boundary after target = %d (%v)", ns, ok)
	}
}

func TestParseRTPStream(t *testing.T) {
	p1 := pkt(7, 0)
	p2 := pkt(9, 1)
	stream, err := EncodeRTPStream([]rtp.Packet{p1, p2})
	if err != nil {
		t.Fatal(err)
	}
	pkts, offs, err := ParseRTPPacketStream(stream)
	if err != nil {
		t.Fatal(err)
	}
	if len(pkts) != 2 || offs[1] != int64(4+13) {
		t.Fatalf("parse offsets wrong: %v", offs)
	}
	if pkts[1].Timestamp != 9 {
		t.Fatalf("ts = %d", pkts[1].Timestamp)
	}
}
