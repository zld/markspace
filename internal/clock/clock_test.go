package clock

import (
	"testing"

	"mediad/internal/mederr"
)

func TestScaleRoundTrip(t *testing.T) {
	s := Scale{Num: 44100, Den: 1}
	for _, ticks := range []int64{0, 1, 44100, 176400, 1 << 40} {
		nanos := s.ToNanos(ticks)
		back := s.FromNanos(nanos)
		if diff := back - ticks; diff < -1 || diff > 1 {
			t.Fatalf("round trip %d -> %d -> %d drifted", ticks, nanos, back)
		}
	}
	if got := (Scale{Num: 90000, Den: 1}).ToNanos(90000); got != 1_000_000_000 {
		t.Fatalf("90000 ticks at 90kHz = %d ns, want 1s", got)
	}
}

func TestUnwrapForwardWrap(t *testing.T) {
	var u Unwrapper
	seq := []uint32{4294967000, 4294967295, 0, 1, 1000}
	want := []int64{4294967000, 4294967295, 4294967296, 4294967297, 4294968296}
	for i, raw := range seq {
		got, err := u.Unwrap(raw)
		if err != nil {
			t.Fatalf("step %d: unexpected error %v", i, err)
		}
		if got != want[i] {
			t.Fatalf("step %d: got %d want %d", i, got, want[i])
		}
	}
}

func TestUnwrapMultipleWrapsMonotonic(t *testing.T) {
	var u Unwrapper
	prev := int64(-1)
	raw := uint32(4294967000)
	for i := 0; i < 100000; i++ {
		got, err := u.Unwrap(raw)
		if err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
		if got <= prev {
			t.Fatalf("iter %d: not monotonic: %d <= %d", i, got, prev)
		}
		prev = got
		raw += 100000 // large strides cross the boundary many times
	}
}

func TestUnwrapNonMonotonic(t *testing.T) {
	var u Unwrapper
	if _, err := u.Unwrap(1000); err != nil {
		t.Fatal(err)
	}
	if _, err := u.Unwrap(2000); err != nil {
		t.Fatal(err)
	}
	// small backward step: not a wrap, must be reported
	if _, err := u.Unwrap(1500); err == nil {
		t.Fatal("expected non-monotonic error")
	} else if me, ok := err.(*mederr.Error); !ok || me.Category != mederr.CatTimeline {
		t.Fatalf("wrong error type: %v", err)
	}
	// state untouched: stream continues from last good value
	if got, err := u.Unwrap(3000); err != nil || got != 3000 {
		t.Fatalf("recovery failed: %d %v", got, err)
	}
	// duplicate
	if _, err := u.Unwrap(3000); err == nil {
		t.Fatal("expected duplicate error")
	}
}

func TestDriftExactLinear(t *testing.T) {
	d, err := NewDriftCorrector(16)
	if err != nil {
		t.Fatal(err)
	}
	// remote clock runs 120 ppm fast: remote = local*(1+120e-6)
	const ppm = 120.0
	for i := int64(0); i < 100; i++ {
		local := i * 20_000_000 // 20 ms in nanos
		remote := int64(float64(local) * (1 + ppm/1e6) / 1000) // remote ticks (1kHz)
		d.Add(remote, local)
	}
	slope, ok := d.Slope()
	if !ok {
		t.Fatal("no fit")
	}
	// expect ~1000/(1+120ppm) nanos per tick
	want := 1000.0 / (1 + ppm/1e6)
	if diff := slope - want; diff < -0.5 || diff > 0.5 {
		t.Fatalf("slope %f, want ~%f", slope, want)
	}
	// correction of a later remote stamp lands within 1 ms of truth
	target := int64(5_000_000_000)
	remote := int64(float64(target) * (1 + ppm/1e6) / 1000)
	got := d.Correct(remote)
	if diff := got - target; diff < -1_000_000 || diff > 1_000_000 {
		t.Fatalf("corrected %d, want ~5s (diff %d ns)", got, diff)
	}
}

func TestDriftWindowSlides(t *testing.T) {
	d, _ := NewDriftCorrector(4)
	for i := int64(0); i < 10; i++ {
		d.Add(i*100, i*1000)
	}
	if len(d.remote) != 4 {
		t.Fatalf("window not enforced: %d", len(d.remote))
	}
	slope, _ := d.Slope()
	if slope != 10 {
		t.Fatalf("slope %f want 10", slope)
	}
}
