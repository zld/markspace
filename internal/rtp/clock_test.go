package rtp

import (
	"math"
	"testing"

	"mediatool/internal/timeline"
)

func TestUnwrapMonotonic(t *testing.T) {
	u := &Unwrapper{}
	cases := []uint32{1000, 2000, 3000, 4000}
	var prev uint64
	for i, ts := range cases {
		got, err := u.Unwrap(ts)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if i > 0 && got < prev {
			t.Fatalf("unwrapped series went backwards: %d < %d", got, prev)
		}
		prev = got
	}
	if prev != 4000 || u.WrapCount() != 0 {
		t.Fatalf("unexpected state: last=%d wraps=%d", prev, u.WrapCount())
	}
}

func TestUnwrapWraparound(t *testing.T) {
	u := &Unwrapper{}
	first, _ := u.Unwrap(math.MaxUint32 - 100) // 4294967195
	got, err := u.Unwrap(50)
	if err != nil {
		t.Fatalf("legal wraparound must not error: %v", err)
	}
	want := first + 151 // 4294967195 -> 4294967246
	if got != want {
		t.Fatalf("wrapped value = %d, want %d", got, want)
	}
	if u.WrapCount() != 1 || u.Wraparounds != 1 {
		t.Fatalf("wrap accounting wrong: count=%d", u.WrapCount())
	}
	// Continuing past the wrap stays monotonic.
	got2, err := u.Unwrap(60)
	if err != nil || got2 != want+10 {
		t.Fatalf("post-wrap progression wrong: %d err=%v", got2, err)
	}
}

func TestUnwrapRepeatedTimestamp(t *testing.T) {
	u := &Unwrapper{}
	a, _ := u.Unwrap(12345)
	b, err := u.Unwrap(12345)
	if err != nil || a != b {
		t.Fatalf("repeat must be stable: %d vs %d err=%v", a, b, err)
	}
	if u.WrapCount() != 0 {
		t.Fatal("repeat is not a wraparound")
	}
}

func TestUnwrapBackwardRestart(t *testing.T) {
	u := &Unwrapper{}
	_, _ = u.Unwrap(3_000_000_000)
	// Backwards distance ~2.999e9 > 2^31: cannot be a legal wraparound.
	got, err := u.Unwrap(1_000_000_000)
	me := timeline.AsMediaError(err)
	if me == nil || me.Class != timeline.ClassTimestampBackward {
		t.Fatalf("want backward-restart error, got %v", err)
	}
	if got != 1_000_000_000 {
		t.Fatalf("restart must rebase clock to 1e9, got %d", got)
	}
	// After rebase, normal progression resumes without another error.
	next, err := u.Unwrap(1_000_001_000)
	if err != nil || next != 1_000_001_000 {
		t.Fatalf("post-restart progression wrong: %d err=%v", next, err)
	}
}

func TestUnwrapNearHalfBoundary(t *testing.T) {
	u := &Unwrapper{}
	_, _ = u.Unwrap(100000)
	// Exactly 2^31 forward distance is still a legal wrap.
	got, err := u.Unwrap(100000 + (1 << 31))
	if err != nil {
		t.Fatalf("half-boundary wrap must be legal: %v", err)
	}
	if got != 100000+(1<<31) {
		t.Fatalf("half-boundary value wrong: %d", got)
	}
}

func TestDriftEstimatorNoDrift(t *testing.T) {
	e := NewDriftEstimator(90000, 8)
	// Perfect 90 kHz clock: 90 units per millisecond.
	for ms := int64(0); ms <= 700; ms += 100 {
		e.Add(Observation{WallMS: ms, MediaTime: uint64(ms * 90)})
	}
	r := e.Estimate()
	if r.Confidence != timeline.ConfidenceHigh {
		t.Fatalf("perfect clock must be high confidence, got %s", r.Confidence)
	}
	if math.Abs(r.PPM) > 1.0 {
		t.Fatalf("ppm = %f, want ~0", r.PPM)
	}
	if r.Correction != 0 || r.Samples != 8 {
		t.Fatalf("correction/samples wrong: %+v", r)
	}
}

func TestDriftEstimatorFastClock(t *testing.T) {
	e := NewDriftEstimator(90000, 8)
	// Device runs 1000 ppm fast: 90.09 units per ms.
	for ms := int64(0); ms <= 700; ms += 100 {
		e.Add(Observation{WallMS: ms, MediaTime: uint64(math.Round(float64(ms) * 90.09))})
	}
	r := e.Estimate()
	if math.Abs(r.PPM-1000) > 50 {
		t.Fatalf("ppm = %f, want ~1000", r.PPM)
	}
	if r.Confidence != timeline.ConfidenceHigh {
		t.Fatalf("clean fast clock must be high confidence, got %s", r.Confidence)
	}
	// A fast media clock is ahead, so correction must pull it back (<=0).
	if r.Correction > 0 {
		t.Fatalf("fast clock correction must be non-positive, got %d", r.Correction)
	}
}

func TestDriftEstimatorNoisyLowConfidence(t *testing.T) {
	e := NewDriftEstimator(90000, 8)
	noise := []int64{0, 300, -400, 250, -600, 350, -500, 450}
	for i, n := range noise {
		ms := int64(i) * 100
		e.Add(Observation{WallMS: ms, MediaTime: uint64(int64(ms*90) + n)})
	}
	r := e.Estimate()
	if r.Confidence == timeline.ConfidenceHigh {
		t.Fatalf("noisy clock must not be high confidence: %+v", r)
	}
}

func TestDriftEstimatorInsufficientSamples(t *testing.T) {
	e := NewDriftEstimator(90000, 8)
	if r := e.Estimate(); r.Confidence != timeline.ConfidenceNone {
		t.Fatalf("empty estimator must be none, got %s", r.Confidence)
	}
	e.Add(Observation{WallMS: 0, MediaTime: 0})
	if r := e.Estimate(); r.Confidence != timeline.ConfidenceNone {
		t.Fatalf("one sample must be none, got %s", r.Confidence)
	}
}

func TestDriftEstimatorWindowBounded(t *testing.T) {
	e := NewDriftEstimator(90000, 4)
	for i := 0; i < 100; i++ {
		e.Add(Observation{WallMS: int64(i), MediaTime: uint64(i * 90)})
	}
	if len(e.obs) != 4 {
		t.Fatalf("window must cap memory at 4, got %d", len(e.obs))
	}
}
