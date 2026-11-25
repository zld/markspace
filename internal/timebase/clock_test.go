package timebase

import (
	"errors"
	"testing"
)

func TestConvertRoundingWithinOneTick(t *testing.T) {
	tb := MXF(30000, 1001) // 29.97 fps
	for ticks := int64(0); ticks < 1000; ticks++ {
		c, err := Convert(tb, ticks)
		if err != nil {
			t.Fatalf("Convert(%d): %v", ticks, err)
		}
		if absFloat(c.ErrorTicks) > 0.5000001 {
			t.Fatalf("tick rounding error too large at %d: %f", ticks, c.ErrorTicks)
		}
		if absFloat(c.ErrorTicks) > 1 {
			t.Fatalf("error exceeds one tick: %f", c.ErrorTicks)
		}
	}
}

func TestConvertRangeAndNanoseconds(t *testing.T) {
	r, err := ConvertRange(MP4(48000), 0, 48000)
	if err != nil {
		t.Fatal(err)
	}
	if r.StartTicks != 0 || r.EndTicks != 90000 {
		t.Fatalf("unexpected unified range: %+v", r)
	}
	if r.StartNS != 0 || r.EndNS != 1_000_000_000 {
		t.Fatalf("unexpected ns range: %+v", r)
	}
	if r.Timescale != UnifiedTimescale {
		t.Fatalf("timescale = %d", r.Timescale)
	}
}

func TestIllegalTimestampNotZeroed(t *testing.T) {
	_, err := Convert(MP4(48000), -7)
	if !errors.Is(err, ErrInvalidTimestamp) {
		t.Fatalf("expected ErrInvalidTimestamp, got %v", err)
	}
	_, err = ConvertRange(MP4(48000), 10, 9)
	if !errors.Is(err, ErrInvalidTimestamp) {
		t.Fatalf("expected invalid range error, got %v", err)
	}
	_, err = Convert(Timebase{}, 1)
	if !errors.Is(err, ErrInvalidTimestamp) {
		t.Fatalf("expected zero timebase error, got %v", err)
	}
}
