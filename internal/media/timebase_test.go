package media

import (
	"math"
	"testing"
)

func TestTimeBaseRoundTrip(t *testing.T) {
	tb, err := NewTimeBase(90000)
	if err != nil {
		t.Fatalf("NewTimeBase: %v", err)
	}
	for _, ticks := range []int64{0, 1, 90000, 90001, 123456789} {
		ns := tb.ToNanos(ticks)
		back := tb.FromNanos(ns)
		if back != ticks {
			t.Errorf("round trip ticks=%d: ns=%d back=%d", ticks, ns, back)
		}
	}
	if got := tb.ToNanos(90000); got != 1_000_000_000 {
		t.Errorf("90000 ticks = %d ns, want 1e9", got)
	}
}

func TestNewTimeBaseRejectsZero(t *testing.T) {
	if _, err := NewTimeBase(0); err != ErrInvalidTimeBase {
		t.Fatalf("zero tick rate: got %v, want ErrInvalidTimeBase", err)
	}
}

func TestWrapper32BitForwardWrap(t *testing.T) {
	w := NewWrapper(32)
	limit := int64(1) << 32
	cases := []struct {
		raw  uint32
		want int64
	}{
		{0xFFFFFFF0, 0xFFFFFFF0},
		{0x00000010, limit + 0x10}, // wrapped forward across 2^32
		{0x00000020, limit + 0x20},
		{0x00000010, limit + 0x10}, // small backward step preserved
	}
	for _, c := range cases {
		if got := w.Expand(c.raw); got != c.want {
			t.Errorf("Expand(%#x) = %d, want %d", c.raw, got, c.want)
		}
	}
}

func TestWrapperBackwardWrap(t *testing.T) {
	w := NewWrapper(32)
	limit := int64(1) << 32
	w.Expand(0x20)
	// Jump backwards across the modulus: smaller arc is the wrap arc.
	got := w.Expand(0xFFFFFFF0)
	want := int64(0x20) - 0x30
	if got != want {
		t.Fatalf("backward wrap = %d, want %d", got, want)
	}
	_ = limit
}

func TestWrapperDeterministic(t *testing.T) {
	raw := []uint32{100, 200, 300, 240, 250, 500, 1000}
	w1, w2 := NewWrapper(32), NewWrapper(32)
	for _, r := range raw {
		if w1.Expand(r) != w2.Expand(r) {
			t.Fatal("two wrappers diverged on identical input")
		}
	}
}

func TestEstimateDrift(t *testing.T) {
	// Remote clock runs 100 ppm fast with a fixed 5 ms offset.
	local := []int64{0, 1e9, 2e9, 3e9, 4e9}
	remote := make([]int64, len(local))
	for i, x := range local {
		remote[i] = int64(1.0001*float64(x)) + 5_000_000
	}
	d := EstimateDrift(local, remote)
	if math.Abs(d.PPM-100) > 1 {
		t.Errorf("PPM = %f, want ~100", d.PPM)
	}
	if math.Abs(d.InterceptNanos-5_000_000) > 1000 {
		t.Errorf("intercept = %f, want 5e6", d.InterceptNanos)
	}
	if d.R2 < 0.9999 {
		t.Errorf("R2 = %f, want >= 0.9999", d.R2)
	}
	// Correction maps a remote reading close to its local origin.
	corr := d.CorrectNanos(remote[4])
	if math.Abs(float64(corr-local[4])) > 2e6 {
		t.Errorf("corrected = %d, want ~%d", corr, local[4])
	}
}

func TestEstimateDriftDegenerate(t *testing.T) {
	d := EstimateDrift([]int64{1, 1}, []int64{2, 3})
	if !math.IsNaN(d.Slope) {
		t.Errorf("degenerate x: slope = %v, want NaN", d.Slope)
	}
	if got := (Drift{}).CorrectNanos(42); got != 42 {
		t.Errorf("NaN-model correction changed value to %d", got)
	}
}
