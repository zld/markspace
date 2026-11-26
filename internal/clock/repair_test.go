package clock

import (
	"math"
	"testing"

	"github.com/pion/rtp"
)

func mustRTP(t *testing.T, ts uint32, seq uint16) []byte {
	t.Helper()
	b, err := (&rtp.Packet{
		Header:  rtp.Header{Version: 2, PayloadType: 96, SequenceNumber: seq, Timestamp: ts, SSRC: 7},
		Payload: []byte{1, 2, 3, 4},
	}).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseAndUnwrapOne32BitWrap(t *testing.T) {
	raw := []uint32{
		math.MaxUint32 - 9000,
		math.MaxUint32 - 6000,
		math.MaxUint32 - 3000,
		math.MaxUint32,
		2999,
		5999,
	}
	points := make([]RTPPoint, 0, len(raw))
	for i, ts := range raw {
		payload := mustRTP(t, ts, uint16(i))
		p, err := ParseRTP(i+1, payload, int64(100+i*16))
		if err != nil {
			t.Fatal(err)
		}
		points = append(points, p)
	}
	got, wraps := UnwrapRTP(points)
	if len(wraps) != 1 {
		t.Fatalf("wraps=%#v want one", wraps)
	}
	if wraps[0].FromSample != 4 || wraps[0].ToSample != 5 {
		t.Fatalf("wrap samples=%d->%d", wraps[0].FromSample, wraps[0].ToSample)
	}
	if got[4].Timestamp != math.MaxUint32+3000 {
		t.Fatalf("unwrapped=%d want %d", got[4].Timestamp, int64(math.MaxUint32)+3000)
	}
	if got[4].RawTimestamp != 2999 {
		t.Fatal("raw timestamp was not retained")
	}
}

func TestContinuityRepairWithinOneSecond(t *testing.T) {
	d := ResolveBoundary(Boundary{
		Track: "jump", Timescale: 90000, Sample: 4,
		PrevTick: 6000, PreviousDuration: 3000, CurrentTick: 7000,
		LastTick: 13000, LastDuration: 3000,
		FirstAffectedSample: 4, LastAffectedSample: 6,
	})
	if d.Isolation != nil || d.Repair == nil {
		t.Fatalf("decision=%#v", d)
	}
	r := d.Repair
	if r.OriginalOffsetTicks != -2000 || r.RepairOffsetTicks != 2000 || r.ResidualTicks != 0 {
		t.Fatalf("repair offsets orig=%d shift=%d residual=%d", r.OriginalOffsetTicks, r.RepairOffsetTicks, r.ResidualTicks)
	}
	if r.IntervalStartTick != 7000 || r.IntervalEndTick != 16000 {
		t.Fatalf("interval=%d..%d", r.IntervalStartTick, r.IntervalEndTick)
	}
}

func TestLargeJumpRequiresConsistentAnchors(t *testing.T) {
	b := Boundary{
		Track: "bad", Timescale: 90000, Sample: 4,
		PrevTick: 0, PreviousDuration: 3000, CurrentTick: 540000,
		LastTick: 549000, LastDuration: 3000,
		FirstAffectedSample: 4, LastAffectedSample: 6,
	}
	if d := ResolveBoundary(b); d.Isolation == nil {
		t.Fatalf("missing anchors should isolate: %#v", d.Repair)
	}

	left, right := int64(100), int64(105)
	b.LeftAnchorShift = &left
	b.RightAnchorShift = &right
	if d := ResolveBoundary(b); d.Isolation == nil {
		t.Fatalf("disagreeing anchors should isolate: %#v", d.Repair)
	}

	right = 101
	d := ResolveBoundary(b)
	if d.Repair == nil || d.Repair.ResidualTicks != 1 {
		t.Fatalf("consistent anchors should repair with residual 1: %#v", d)
	}
}
