package clock

import (
	"errors"
	"math"
	"testing"
)

func TestRationalScale(t *testing.T) {
	c, err := NewMediaClock(48000)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.ToNanos(48); got != 1_000_000 { // 48 ticks @48kHz = 1ms
		t.Fatalf("ToNanos(48)=%d want 1000000", got)
	}
	if got := c.FromNanos(1_000_000); got != 48 {
		t.Fatalf("FromNanos(1ms)=%d want 48", got)
	}
	// 44100 -> 48000 sample counts must stay exact at a block boundary:
	// 441 source ticks equal 480 destination ticks over 10 ms.
	r := Rational{Num: 44100, Den: 48000}
	if got := r.ScaleExact(441, Rational{Num: 1, Den: 1}); got != 480 {
		t.Fatalf("scale=%d want 480", got)
	}
}

func TestUnwrapperWrap(t *testing.T) {
	u := NewUnwrapper(0)
	cases := []struct {
		in   uint32
		want uint64
	}{
		{0xFFFFFFF0, 0xFFFFFFF0},
		{0xFFFFFFF8, 0xFFFFFFF8},
		{0x00000008, 0x100000008}, // wraps upward
		{0x00000010, 0x100000010},
	}
	for _, tc := range cases {
		if got := u.Extend(tc.in); got != tc.want {
			t.Fatalf("Extend(%#x)=%#x want %#x", tc.in, got, tc.want)
		}
	}
	if u.WrapCount() != 1 {
		t.Fatalf("wraps=%d want 1", u.WrapCount())
	}
	// Snapshot/restore round trip.
	s := u.Snapshot()
	u2 := NewUnwrapper(999)
	u2.Restore(s)
	if got := u2.Extend(0x00000018); got != 0x100000018 {
		t.Fatalf("after restore=%#x", got)
	}
}

func TestDriftEstimator(t *testing.T) {
	// Node clock runs 1000 ppm fast: node = 1.001 * reference.
	d := NewDriftEstimator(8)
	for i := 0; i < 8; i++ {
		ref := uint64(i+1) * 1_000_000
		d.Observe(ref, uint64(float64(ref)*1.001))
	}
	ppm := d.PPM()
	if math.Abs(ppm-1000) > 1 {
		t.Fatalf("ppm=%f want ~1000", ppm)
	}
	s := d.Snapshot()
	d2 := NewDriftEstimator(8)
	d2.Restore(s)
	if d2.PPM() != ppm {
		t.Fatal("restored estimator diverged")
	}
}

func TestErrorModel(t *testing.T) {
	e := NewError(ErrCRC, "bad payload").WithPosition(1234, 2, 7)
	if !e.Recoverable() || e.FileOffset != 1234 || e.TrackID != 2 || e.SampleIndex != 7 {
		t.Fatalf("position error: %+v", e)
	}
	var ce *Error
	if !errors.As(e, &ce) || ce.Code != ErrCRC {
		t.Fatal("errors.As failed")
	}
	if NewError(ErrUnrecoverable, "x").Recoverable() {
		t.Fatal("unrecoverable misclassified")
	}
}

func TestPacketizeReassemble(t *testing.T) {
	p := NewPacketizer(0xDEAD, 48000, 16)
	payload := make([]byte, 40)
	for i := range payload {
		payload[i] = byte(i)
	}
	pkts, err := p.Packetize(payload, 0xFFFFFFFE)
	if err != nil || len(pkts) != 3 || !pkts[2].Marker {
		t.Fatalf("packetize: n=%d err=%v", len(pkts), err)
	}
	ts, got, err := Reassemble(pkts)
	if err != nil || ts != 0xFFFFFFFE || string(got) != string(payload) {
		t.Fatalf("reassemble: ts=%#x err=%v", ts, err)
	}
}
