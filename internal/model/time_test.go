package model

import (
	"errors"
	"math"
	"testing"
)

func TestToNanoAndBack(t *testing.T) {
	cases := []struct {
		name       string
		ticks      int64
		rate       uint32
		wantNano   Nano
		wantReTick int64
	}{
		{"48k one frame 960 ticks", 960, 48000, 20 * Millisecond, 960},
		{"90k video tick", 3003, 90000, Nano(33366666), 3003},
		{"44.1k 1024 ticks", 1024, 44100, Nano(23219954), 1024},
		{"zero", 0, 48000, 0, 0},
		{"negative", -960, 48000, -20 * Millisecond, -960},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ToNano(tc.ticks, tc.rate)
			if err != nil {
				t.Fatalf("ToNano: %v", err)
			}
			if got != tc.wantNano {
				t.Fatalf("ToNano = %d, want %d", got, tc.wantNano)
			}
			back, err := RescaleNano(got, tc.rate)
			if err != nil {
				t.Fatalf("RescaleNano: %v", err)
			}
			if back != tc.wantReTick {
				t.Fatalf("RescaleNano = %d, want %d", back, tc.wantReTick)
			}
		})
	}
}

func TestZeroRateIsLocatedError(t *testing.T) {
	_, err := ToNano(1, 0)
	var me *Error
	if !errors.As(err, &me) || me.Code != ErrTimeOverflow {
		t.Fatalf("want ErrTimeOverflow, got %v", err)
	}
	if me.Track != "time" {
		t.Fatalf("want source track=time, got %q", me.Track)
	}
	if _, err := NewRational(1, 0); err == nil {
		t.Fatal("NewRational zero denominator must fail")
	}
}

func TestScaleOverflow(t *testing.T) {
	if _, err := Scale(math.MaxInt64, 1_000_000_000, 1); err == nil {
		t.Fatal("overflow Scale must return an error")
	}
	if _, err := Scale(1, math.MaxInt64, math.MaxInt64); err != nil {
		t.Fatalf("large but exact ratio failed: %v", err)
	}
	v, err := Scale(7, 3, 2) // 21/2 truncated toward zero
	if err != nil || v != 10 {
		t.Fatalf("Scale(7,3,2) = %d, %v", v, err)
	}
	v, err = Scale(-7, 3, 2)
	if err != nil || v != -10 {
		t.Fatalf("Scale(-7,3,2) = %d, %v", v, err)
	}
}

func TestAddNanoOverflow(t *testing.T) {
	if _, err := AddNano(math.MaxInt64, 1); err == nil {
		t.Fatal("positive overflow must be reported")
	}
	if _, err := AddNano(math.MinInt64, -1); err == nil {
		t.Fatal("negative overflow must be reported")
	}
	r, err := AddNano(20*Millisecond, -5*Millisecond)
	if err != nil || r != 15*Millisecond {
		t.Fatalf("AddNano = %d, %v", r, err)
	}
}

func TestRationalNormalizesSign(t *testing.T) {
	r, err := NewRational(3, -4)
	if err != nil {
		t.Fatal(err)
	}
	if r.Num != -3 || r.Den != 4 {
		t.Fatalf("got %+v", r)
	}
}
