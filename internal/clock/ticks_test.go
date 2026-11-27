package clock

import (
	"errors"
	"math"
	"testing"
)

func TestConvertRoundTripAndRounding(t *testing.T) {
	cases := []struct {
		value, srcHz, want int64
	}{
		{0, 48000, 0},
		{48000, 48000, MasterHz},   // one second of audio
		{3000, 30000, 9000},        // 100 ms of 30 kHz
		{1, 48000, 2},              // nearest rounding (1.875 -> 2)
		{1, 44100, 2},              // 2.04 -> 2
		{-48000, 48000, -MasterHz}, // negative delta
		{90000, 90000, MasterHz},   // RTP native scale is identity
	}
	for _, c := range cases {
		got, err := Convert(c.value, c.srcHz)
		if err != nil {
			t.Fatalf("Convert(%d,%d): %v", c.value, c.srcHz, err)
		}
		if int64(got) != c.want {
			t.Errorf("Convert(%d,%d) = %d, want %d", c.value, c.srcHz, got, c.want)
		}
	}
	// Boundary error of the conversion never exceeds one source tick in
	// master units; assert the rounding error stays under MasterHz/srcHz.
	v, err := Convert(1, 48000)
	if err != nil || math.Abs(float64(int64(v)-MasterHz/48000.0)) > 1.0 {
		t.Fatalf("boundary error exceeds 1 tick: %v %v", v, err)
	}
}

func TestConvertRejectsBadTimeBase(t *testing.T) {
	for _, bad := range []int64{0, -1} {
		if _, err := Convert(10, bad); !errors.Is(err, ErrInvalidTimeBase) {
			t.Errorf("Convert(10,%d) err = %v, want ErrInvalidTimeBase", bad, err)
		}
	}
}

func TestUnwrapTimestamp(t *testing.T) {
	const max = uint64(1<<32) - 1
	values := []uint64{max - 2, max - 1, 0, 1, 2, 10}
	got := UnwrapMany(values, RTPTimestampMod)
	want := []int64{int64(max) - 2, int64(max) - 1, int64(max) + 1,
		int64(max) + 2, int64(max) + 3, int64(max) + 11}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("index %d: got %d want %d (full=%v)", i, got[i], want[i], got)
		}
	}
}

func TestUnwrapSequence(t *testing.T) {
	values := []uint64{65534, 65535, 0, 1}
	got := UnwrapMany(values, RTPSequenceMod)
	want := []int64{65534, 65535, 65536, 65537}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("index %d: got %d want %d", i, got[i], want[i])
		}
	}
}

func TestUnwrapBackwardJumpStaysNearest(t *testing.T) {
	// A genuine large backwards jump is resolved to the nearest congruent
	// representative, which is deterministic even if it goes backwards.
	got := Unwrap(1_000_000, 999_000, RTPTimestampMod)
	if got != 999_000 {
		t.Fatalf("got %d, want 999000", got)
	}
}
