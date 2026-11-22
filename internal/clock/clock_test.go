package clock

import (
	"errors"
	"math"
	"testing"
)

func TestScaleAndOrigin(t *testing.T) {
	tl := NewTimeline()
	if err := tl.Add(TrackClock{Track: 1, TicksPerSecond: 1000, OriginTicks: 500}); err != nil {
		t.Fatal(err)
	}
	ns, err := tl.Reduce(1, 0, 1500) // 1000 ticks above origin => 1 s
	if err != nil || ns != 1_000_000_000 {
		t.Fatalf("ns=%d err=%v", ns, err)
	}
	ns2, err := tl.Reduce(1, 1, 2500)
	if err != nil || ns2 != 2_000_000_000 {
		t.Fatalf("ns2=%d err=%v", ns2, err)
	}
}

func TestBadScaleAndDuplicate(t *testing.T) {
	tl := NewTimeline()
	if err := tl.Add(TrackClock{Track: 1}); err == nil {
		t.Fatal("zero scale must fail")
	}
	if err := tl.Add(TrackClock{Track: 2, TicksPerSecond: 1}); err != nil {
		t.Fatal(err)
	}
	if err := tl.Add(TrackClock{Track: 2, TicksPerSecond: 1}); err == nil {
		t.Fatal("duplicate registration must fail")
	}
}

func TestWrapAround(t *testing.T) {
	tl := NewTimeline()
	// 1000 ticks/s, modulus 10000 => wraps every 10 s.
	if err := tl.Add(TrackClock{Track: 7, TicksPerSecond: 1000, WrapAt: 10000}); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		raw int64
		ns  int64
	}{
		{9900, 9_900_000_000},
		{100, 10_100_000_000}, // wrapped once: jump 9800 > modulus/2
		{200, 10_200_000_000},
		{9800, 19_800_000_000},
		{100, 20_100_000_000}, // wrapped twice: jump 9700 > modulus/2
	}
	for i, c := range cases {
		got, err := tl.Reduce(7, uint64(i), c.raw)
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		if got != c.ns {
			t.Fatalf("case %d raw=%d: got %d want %d", i, c.raw, got, c.ns)
		}
	}
}

func TestAmbiguousWrapRejected(t *testing.T) {
	tl := NewTimeline()
	if err := tl.Add(TrackClock{Track: 1, TicksPerSecond: 1000, WrapAt: 1000}); err != nil {
		t.Fatal(err)
	}
	if _, err := tl.Reduce(1, 0, 900); err != nil {
		t.Fatal(err)
	}
	// Backwards jump of only 100 ticks (< modulus/2) cannot be trusted.
	_, err := tl.Reduce(1, 1, 800)
	var ce *Error
	if !errors.As(err, &ce) || ce.Kind != KindWrap {
		t.Fatalf("want KindWrap, got %v", err)
	}
}

func TestMonotonicityViolationNotRepaired(t *testing.T) {
	tl := NewTimeline()
	if err := tl.Add(TrackClock{Track: 3, TicksPerSecond: 1000}); err != nil {
		t.Fatal(err)
	}
	if _, err := tl.Reduce(3, 0, 100); err != nil {
		t.Fatal(err)
	}
	_, err := tl.Reduce(3, 1, 50) // backwards, no wrap configured
	var ce *Error
	if !errors.As(err, &ce) || ce.Kind != KindMonotonicity {
		t.Fatalf("want KindMonotonicity, got %v", err)
	}
	if ce.Track != 3 || ce.Seq != 1 {
		t.Fatalf("diagnostics lost: %+v", ce)
	}
	// State must not have advanced: repeating the earlier good sample works.
	ns, err := tl.Reduce(3, 2, 200)
	if err != nil || ns != 200_000_000 {
		t.Fatalf("state mutated by failed reduce: ns=%d err=%v", ns, err)
	}
}

func TestDriftCorrection(t *testing.T) {
	// +1000 ppm fast device: 1.001 measured seconds map to ~1 true second.
	m := NewDriftModel(1000)
	got := m.Correct(1_001_000_000)
	want := int64(math.Round(1_001_000_000.0 / 1.001))
	if got != want {
		t.Fatalf("correction: got %d want %d", got, want)
	}
	if math.Abs(float64(got)-1_000_000_000) > 1 {
		t.Fatalf("drift-corrected base off by %d ns", got-1_000_000_000)
	}
}

func TestDriftMutationDetected(t *testing.T) {
	m := NewDriftModelWithThreshold(0, 500)
	// Steady ~100 ppm residual intervals.
	for i := 0; i < 6; i++ {
		if err := m.Observe(1, uint64(i), 1_000_100, 1_000_000); err != nil {
			t.Fatalf("steady interval %d: %v", i, err)
		}
	}
	// Sudden 20 000 ppm jump must be flagged, not averaged away.
	err := m.Observe(1, 7, 1_020_000, 1_000_000)
	var ce *Error
	if !errors.As(err, &ce) || ce.Kind != KindDrift {
		t.Fatalf("want KindDrift, got %v", err)
	}
	if m.Mutations() != 1 {
		t.Fatalf("mutations = %d", m.Mutations())
	}
}

func TestVariableFrameRateStaysMonotonic(t *testing.T) {
	tl := NewTimeline()
	if err := tl.Add(TrackClock{Track: 1, TicksPerSecond: 90000}); err != nil {
		t.Fatal(err)
	}
	// Irregular Opus-like frame spacing; every step must still advance.
	raw := []int64{0, 6000, 9000, 12000, 21000, 30000}
	var prev int64
	for i, r := range raw {
		ns, err := tl.Reduce(1, uint64(i), r)
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		if i > 0 && ns <= prev {
			t.Fatalf("VFR frame %d not monotonic: %d <= %d", i, ns, prev)
		}
		prev = ns
	}
}

func TestDeterministicReduction(t *testing.T) {
	reduce := func() []int64 {
		tl := NewTimeline()
		_ = tl.Add(TrackClock{Track: 1, TicksPerSecond: 48000, WrapAt: 1 << 16, DriftPPM: 180})
		out := make([]int64, 0, 8)
		for i := int64(0); i < 8; i++ {
			ns, err := tl.Reduce(1, uint64(i), (i*4807)%(1<<16))
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, ns)
		}
		return out
	}
	a, b := reduce(), reduce()
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("nondeterministic at %d: %d vs %d", i, a[i], b[i])
		}
	}
}
