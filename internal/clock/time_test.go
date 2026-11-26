package clock

import (
	"errors"
	"math"
	"testing"

	"example.com/mediavault/internal/fault"
)

func TestRateReducesAndConvertsExactly(t *testing.T) {
	r := NewRate(48000, 2)
	if r.Num != 24000 || r.Den != 1 {
		t.Fatalf("rate not reduced: %+v", r)
	}
	ns, err := r.DurationOfSamples(24000)
	if err != nil {
		t.Fatalf("duration: %v", err)
	}
	if ns != 1_000_000_000 {
		t.Fatalf("one second of 24 kHz should be 1e9 ns, got %d", ns)
	}
	got, err := r.SamplesAt(500_000_000)
	if err != nil {
		t.Fatalf("samples: %v", err)
	}
	if got != 12000 {
		t.Fatalf("half second at 24 kHz should be 12000 samples, got %d", got)
	}
}

func TestRateRejectsBadInput(t *testing.T) {
	for _, r := range []Rate{{}, {Num: -1, Den: 1}, {Num: 1, Den: 0}} {
		if r.Valid() {
			t.Fatalf("rate %+v should be invalid", r)
		}
		if _, err := r.DurationOfSamples(1); err == nil {
			t.Fatalf("invalid rate %+v must fail duration", r)
		}
	}
}

func TestOverflowIsStableFault(t *testing.T) {
	r := NewRate(192000, 1)
	_, err := r.DurationOfSamples(math.MaxInt64 / 2)
	if err == nil {
		t.Fatal("expected overflow")
	}
	if fault.CodeOf(err) != fault.CodeRTPOverflow {
		t.Fatalf("code = %q", fault.CodeOf(err))
	}
	if fault.StageOf(err) != fault.StageScanned {
		t.Fatalf("stage = %q", fault.StageOf(err))
	}
	_, err = r.SamplesAt(math.MaxInt64)
	if fault.CodeOf(err) != fault.CodeRTPOverflow {
		t.Fatalf("SamplesAt overflow code = %q", fault.CodeOf(err))
	}
}

func TestPPMDelta(t *testing.T) {
	ppm, err := PPMDelta(100_100, 100_000)
	if err != nil {
		t.Fatalf("ppm: %v", err)
	}
	if ppm != 1000 {
		t.Fatalf("100 extra samples in 100k should be 1000 ppm, got %d", ppm)
	}
	neg, err := PPMDelta(99_900, 100_000)
	if err != nil || neg != -1000 {
		t.Fatalf("negative drift: %d %v", neg, err)
	}
	if _, err := PPMDelta(1, 0); fault.CodeOf(err) != fault.CodeRTPOverflow {
		t.Fatalf("zero expected must overflow, got %v", err)
	}
}

func TestUnwrapDeterminableWrap(t *testing.T) {
	u := NewUnwrapper()
	cases := []struct {
		raw     uint32
		want    uint64
		wrapped bool
	}{
		{0xFFFFFF00, 0xFFFFFF00, false},
		{0xFFFFFFF0, 0xFFFFFFF0, false},
		{0x00000010, 0x100000010, true},
		{0x00001000, 0x100001000, false},
	}
	for _, c := range cases {
		got, wrapped, err := u.Unwrap(c.raw)
		if err != nil {
			t.Fatalf("raw %#x: %v", c.raw, err)
		}
		if got != c.want || wrapped != c.wrapped {
			t.Fatalf("raw %#x: got %#x wrap=%v, want %#x wrap=%v", c.raw, got, wrapped, c.want, c.wrapped)
		}
	}
	if u.Wraps() != 1 {
		t.Fatalf("wraps = %d", u.Wraps())
	}
}

func TestUnwrapRejectsAmbiguousBackward(t *testing.T) {
	u := NewUnwrapper()
	if _, _, err := u.Unwrap(1_000_000); err != nil {
		t.Fatal(err)
	}
	_, _, err := u.Unwrap(900_000)
	if err == nil {
		t.Fatal("a small backward step is not a wrap and must be rejected")
	}
	if fault.CodeOf(err) != fault.CodeRTPBackward {
		t.Fatalf("code = %q", fault.CodeOf(err))
	}
	var fe *fault.Error
	if !errors.As(err, &fe) {
		t.Fatal("error should be a *fault.Error")
	}
	if fe.Stage != fault.StageNormalized {
		t.Fatalf("stage = %q", fe.Stage)
	}
}

func TestUnwrapExpandsRepeatedWraps(t *testing.T) {
	u := NewUnwrapper()
	if _, _, err := u.Unwrap(0xFFFFFF00); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := u.Unwrap(0x00000100); got != 0x100000100 {
		t.Fatalf("first wrap: %#x", got)
	}
	if _, _, err := u.Unwrap(0xFFFFFE00); err != nil {
		t.Fatalf("second pre-wrap: %v", err)
	}
	if got, _, _ := u.Unwrap(0x00000200); got != 0x200000200 {
		t.Fatalf("second wrap: %#x", got)
	}
	if u.Wraps() != 2 {
		t.Fatalf("wraps = %d", u.Wraps())
	}
}
