package clock

import (
	"testing"

	"mediad/internal/model"
)

func TestRateEqualAndNormal(t *testing.T) {
	cases := []struct {
		a, b Rate
		want bool
	}{
		{Rate{30000, 1001}, Rate{60000, 2002}, true},
		{Rate{90000, 1}, Rate{90000, 1}, true},
		{Rate{44100, 1}, Rate{48000, 1}, false},
		{Rate{0, 1}, Rate{0, 1}, false},
	}
	for _, c := range cases {
		if got := c.a.Equal(c.b); got != c.want {
			t.Errorf("%v.Equal(%v) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
	if n := (Rate{60000, 2002}).Normal(); n != (Rate{30000, 1001}) {
		t.Errorf("Normal = %v, want {30000 1001}", n)
	}
}

func TestSharedConversionRounding(t *testing.T) {
	// 44.1 kHz tick 1 -> nanoseconds: 1e9/44100 = 22675.736... -> 22676.
	s := Stamp{Value: 1, Rate: Rate{44100, 1}}
	got, err := s.Shared()
	if err != nil || got != 22676 {
		t.Fatalf("44.1k tick 1 = %d, err=%v, want 22676", got, err)
	}
	// 48 kHz tick 1 -> 20833.333... -> 20833 (fraction below half).
	if got, _ = (Stamp{1, Rate{48000, 1}}).Shared(); got != 20833 {
		t.Fatalf("48k tick 1 = %d, want 20833", got)
	}
	// Round trip through FromShared must land on the original tick.
	back, err := FromShared(22676, Rate{44100, 1})
	if err != nil || back != 1 {
		t.Fatalf("FromShared round trip = %d, err=%v, want 1", back, err)
	}
	// Negative half rounds away from zero: -22675.7... -> -22676.
	if got, _ = (Stamp{-1, Rate{44100, 1}}).Shared(); got != -22676 {
		t.Fatalf("negative round = %d, want -22676", got)
	}
}

func TestOverflowDetected(t *testing.T) {
	bad := Stamp{Value: 1<<62, Rate: Rate{1, 1}} // ns = 4.6e27, overflows int64
	if _, err := bad.Shared(); !model.HasCode(err, model.ErrTimestampOverflow) {
		t.Fatalf("expected TS_OVERFLOW, got %v", err)
	}
	if _, err := (Stamp{1, Rate{0, 1}}).Shared(); !model.HasCode(err, model.ErrTimestampOverflow) {
		t.Fatalf("invalid rate must error, got %v", err)
	}
}

func TestMonotonicAndBases(t *testing.T) {
	if err := CheckMonotonic([]int64{1, 2, 2, 3}); err != nil {
		t.Errorf("equal timestamps should be allowed: %v", err)
	}
	err := CheckMonotonic([]int64{1, 2, 0})
	if !model.HasCode(err, model.ErrNonMonotonicDTS) {
		t.Fatalf("want NON_MONOTONIC_DTS, got %v", err)
	}
	err = CheckTrackBases([]Rate{{48000, 1}, {48000, 1}})
	if err != nil {
		t.Errorf("matching bases failed: %v", err)
	}
	err = CheckTrackBases([]Rate{{48000, 1}, {44100, 1}})
	if !model.HasCode(err, model.ErrTimebaseMismatch) {
		t.Fatalf("want TIMEBASE_MISMATCH, got %v", err)
	}
}
