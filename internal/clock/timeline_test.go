package clock

import (
	"errors"
	"testing"
)

func TestConvertExactAndRounded(t *testing.T) {
	got, err := Convert(9000, 90000, 1000)
	if err != nil || got != 100 {
		t.Fatalf("exact convert = %d, %v", got, err)
	}
	got, err = Convert(1, 30, 1000) // 33.333... ms, nearest
	if err != nil || got != 33 {
		t.Fatalf("rounded convert = %d, %v", got, err)
	}
}

func TestConvertRejectsBadScaleAndOverflow(t *testing.T) {
	if _, err := Convert(1, 0, 1000); !errors.Is(err, ErrInvalidScale) {
		t.Fatalf("err = %v, want invalid scale", err)
	}
	if _, err := Convert(1<<62, 1, 1_000_000_000); !errors.Is(err, ErrOverflow) {
		t.Fatalf("err = %v, want overflow", err)
	}
}

func TestRangeValidationDoesNotClamp(t *testing.T) {
	if _, err := NewRange(-1, 2, 1000); !errors.Is(err, ErrInvalidRange) {
		t.Fatalf("negative start err = %v", err)
	}
	if _, err := NewRange(5, 4, 1000); !errors.Is(err, ErrInvalidRange) {
		t.Fatalf("reversed range err = %v", err)
	}
}

func TestOverlapAtScale(t *testing.T) {
	a := Range{Start: 0, End: 300, Timescale: 1000}
	b := Range{Start: 250, End: 300, Timescale: 1000}
	got, ok, err := OverlapAtScale(a, b, 1000)
	if err != nil || !ok || got.Start != 250 || got.End != 300 {
		t.Fatalf("overlap = %+v %v %v", got, ok, err)
	}
	c := Range{Start: 300, End: 400, Timescale: 1000}
	if _, ok, err := OverlapAtScale(a, c, 1000); err != nil || ok {
		t.Fatalf("touching ranges ok=%v err=%v, want no overlap", ok, err)
	}
}
