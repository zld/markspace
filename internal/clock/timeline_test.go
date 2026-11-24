package clock

import (
	"errors"
	"testing"

	"github.com/pion/rtp"
)

func TestConvertTimescales(t *testing.T) {
	cases := []struct {
		name      string
		ticks     int64
		from, to  uint32
		want      int64
		wantErrIs error
	}{
		{"90k to unified one second", 90000, 90000, UnifiedHz, 1_000_000, nil},
		{"48k to 90k", 48000, 48000, 90000, 90000, nil},
		{"24fps frame duration rounds", 3750, 90000, UnifiedHz, 41667, nil}, // 41.6667ms rounds up
		{"zero source", 10, 0, UnifiedHz, 0, ErrZeroTimescale},
		{"zero dest", 10, 90000, 0, 0, ErrZeroTimescale},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Convert(tc.ticks, tc.from, tc.to)
			if tc.wantErrIs != nil {
				if !errors.Is(err, tc.wantErrIs) {
					t.Fatalf("want err %v, got %v", tc.wantErrIs, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if got != tc.want {
				t.Fatalf("Convert(%d,%d,%d)=%d want %d", tc.ticks, tc.from, tc.to, got, tc.want)
			}
		})
	}
}

func TestConvertOverflow(t *testing.T) {
	// Huge tick * large destination scale cannot fit int64.
	_, err := Convert(1<<62, 1, 1_000_000_000)
	if !errors.Is(err, ErrOverflow) {
		t.Fatalf("expected overflow, got %v", err)
	}
}

func TestToUnifiedRejectsZero(t *testing.T) {
	if _, err := ToUnified(123, 0); !errors.Is(err, ErrZeroTimescale) {
		t.Fatalf("expected zero timescale error, got %v", err)
	}
}

func TestDriftEstimatorKnownRate(t *testing.T) {
	d := NewDriftEstimator(100_000)
	// media runs 500 ppm fast: media = ref * 1.0005
	for i := int64(0); i < 20; i++ {
		ref := i * 90000
		media := ref + (ref*500)/1_000_000
		d.Observe(ref, media)
	}
	ppm, ok := d.EstimatePPM()
	if !ok {
		t.Fatal("expected estimate")
	}
	if ppm < 495 || ppm > 505 {
		t.Fatalf("ppm=%d want ~500", ppm)
	}
}

func TestDriftEstimatorBoundedClamp(t *testing.T) {
	d := NewDriftEstimator(1000) // tight bound 0.1%
	for i := int64(1); i <= 10; i++ {
		d.Observe(i*1000, i*2000) // 100% fast drift
	}
	ppm, ok := d.EstimatePPM()
	if !ok {
		t.Fatal("expected estimate")
	}
	if ppm != 1000 {
		t.Fatalf("expected clamped 1000, got %d", ppm)
	}
}

func TestDriftEstimatorNeedsEvidence(t *testing.T) {
	d := NewDriftEstimator(0)
	if _, ok := d.EstimatePPM(); ok {
		t.Fatal("empty estimator must not report a rate")
	}
	d.Observe(1, 2)
	if _, ok := d.EstimatePPM(); ok {
		t.Fatal("single observation must not report a rate")
	}
}

func TestUnwrapBoundaryEvidence(t *testing.T) {
	const M = int64(1 << 32)
	u := NewUnwrapper(M, 200_000)
	seq := []int64{M - 120_000, 60_090, 150_090}
	want := []int64{M - 120_000, M + 60_090, M + 150_090}
	for i, raw := range seq {
		got, ev, ok := u.Unwrap(raw)
		if !ok {
			t.Fatalf("step %d rejected", i)
		}
		if got != want[i] {
			t.Fatalf("step %d got %d want %d", i, got, want[i])
		}
		if i == 1 && ev == nil {
			t.Fatal("expected wrap event at boundary")
		}
	}
	if len(u.Events) != 1 {
		t.Fatalf("events=%d want 1", len(u.Events))
	}
}

func TestUnwrapRejectsAmbiguousBackwardJump(t *testing.T) {
	const M = int64(1 << 32)
	u := NewUnwrapper(M, 1000) // tight boundary window
	_, _, _ = u.Unwrap(1_000_000)
	v, ev, ok := u.Unwrap(500_000) // backward but nowhere near modulus edge
	if ok {
		t.Fatal("ambiguous backward jump must not be unwrapped")
	}
	if ev != nil {
		t.Fatal("no wrap event expected")
	}
	if v != 500_000 {
		t.Fatalf("raw value should be returned, got %d", v)
	}
	if u.RejectedBackward != 1 {
		t.Fatalf("rejected=%d want 1", u.RejectedBackward)
	}
}

func TestUnwrapRTPPacketsViaPion(t *testing.T) {
	// Build real pion/rtp packets whose 32-bit timestamps cross the edge.
	raws := []uint32{0xFFFFFF00, 0x00000040, 0x00000080}
	var wire [][]byte
	for i, ts := range raws {
		p := &rtp.Packet{
			Header:  rtp.Header{Version: 2, PayloadType: 96, SequenceNumber: uint16(i + 1), Timestamp: ts, SSRC: 1},
			Payload: []byte{0x00},
		}
		b, err := p.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		wire = append(wire, b)
	}
	unwrapped, events, rejected, err := ParseRTPAndUnwrap(wire, 0x200)
	if err != nil {
		t.Fatal(err)
	}
	if rejected != 0 || len(events) != 1 {
		t.Fatalf("rejected=%d events=%d", rejected, len(events))
	}
	const M = int64(1 << 32)
	if unwrapped[1] != M+0x40 {
		t.Fatalf("unwrapped[1]=%d want %d", unwrapped[1], M+0x40)
	}
}
