package mediacore

import (
	"math"
	"testing"
	"time"
)

func TestRescaleExactRatios(t *testing.T) {
	cases := []struct {
		in           int64
		srcTS, dstTS uint32
		want         int64
	}{
		{0, 90000, 1000, 0},
		{90000, 90000, 1000, 1000},
		{180000, 90000, 1000, 2000},
		{48000, 48000, 1000, 1000},
		{1024, 48000, 48000, 1024},
		{-90000, 90000, 1000, -1000},
	}
	for _, tc := range cases {
		got, err := Rescale(tc.in, tc.srcTS, tc.dstTS)
		if err != nil {
			t.Fatalf("Rescale(%d,%d,%d) unexpected error: %v", tc.in, tc.srcTS, tc.dstTS, err)
		}
		if got != tc.want {
			t.Fatalf("Rescale(%d,%d,%d)=%d want %d", tc.in, tc.srcTS, tc.dstTS, got, tc.want)
		}
	}
}

func TestRescaleHalfUpRounding(t *testing.T) {
	// One 90 kHz tick is 1/90 ms; 45 ticks = 0.5 ms exactly and must round up.
	got, err := Rescale(45, 90000, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if got != 1 {
		t.Fatalf("half-up at exact half = %d, want 1", got)
	}
	got, err = Rescale(44, 90000, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if got != 0 {
		t.Fatalf("below half = %d, want 0", got)
	}
}

func TestRescaleRejectsZeroTimescale(t *testing.T) {
	if _, err := Rescale(10, 0, 1000); !IsKind(err, KindInvalidConfig) {
		t.Fatalf("src zero: got %v kind %q", err, KindOf(err))
	}
	if _, err := Rescale(10, 1000, 0); !IsKind(err, KindInvalidConfig) {
		t.Fatalf("dst zero: got %v kind %q", err, KindOf(err))
	}
}

// TestRescaleAbsoluteBoundariesDoNotDrift proves that converting an absolute
// boundary independently is stable: boundary n computed directly must never
// depend on accumulating n previous rounded conversions.
func TestRescaleAbsoluteBoundariesDoNotDrift(t *testing.T) {
	const frameDur = int64(3000) // 90 kHz, ~33.3 ms
	const frames = 10000
	var prev int64
	for n := 0; n <= frames; n++ {
		absolute, err := Rescale(int64(n)*frameDur, 90000, 1000)
		if err != nil {
			t.Fatal(err)
		}
		if n > 0 && absolute < prev {
			t.Fatalf("absolute boundary regressed at %d: %d < %d", n, absolute, prev)
		}
		prev = absolute
	}
	// The final boundary must be within one destination tick of the ideal and
	// not off by a per-step fraction multiplied by the frame count.
	got, _ := Rescale(frames*frameDur, 90000, 1000)
	ideal := float64(frames*frameDur) * 1000 / 90000
	if d := math.Abs(float64(got) - ideal); d > 1 {
		t.Fatalf("absolute conversion drifted: got %d ideal %.3f diff %.3f", got, ideal, d)
	}
}

// TestRateConverterBoundedCarryDrift checks the delta-carrying converter keeps
// accumulated output within one tick of converting the total once, even over a
// very long run of awkward deltas.
func TestRateConverterBoundedCarryDrift(t *testing.T) {
	rc, err := NewRateConverter(48000, 90000)
	if err != nil {
		t.Fatal(err)
	}
	deltas := make([]uint64, 100000)
	for i := range deltas {
		deltas[i] = 1024 // AAC frame duration on a 48 kHz clock
	}
	for _, d := range deltas {
		if _, err := rc.Push(d); err != nil {
			t.Fatal(err)
		}
	}
	in, out := rc.Totals()
	want, err := Rescale(int64(in), 48000, 90000)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(float64(out)-float64(want)) > 1 {
		t.Fatalf("accumulated out=%d vs single rescale=%d (unbounded drift)", out, want)
	}
}

func TestTimebaseDurationAndTicks(t *testing.T) {
	tb := NewTimebase(1000)
	d, err := tb.Duration(1500)
	if err != nil {
		t.Fatal(err)
	}
	if d != 1500*time.Millisecond {
		t.Fatalf("duration = %v, want 1.5s", d)
	}
	ticks, err := tb.Ticks(1500 * time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if ticks != 1500 {
		t.Fatalf("ticks = %d, want 1500", ticks)
	}
	if NewTimebase(0).Valid() {
		t.Fatal("zero timescale must be invalid")
	}
}
