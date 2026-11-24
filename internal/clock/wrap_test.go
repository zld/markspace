package clock

import "testing"

func TestUnwrapExactlyHalfModulusResolvesToEpoch(t *testing.T) {
	got, err := Unwrap(0, int64(Mod32)/2, Mod32)
	if err != nil {
		t.Fatal(err)
	}
	if got != 0 {
		t.Fatalf("half-modulus tie should resolve to epoch, got %d", got)
	}
}

func TestUnwrapJustUnderHalfModulusRoundTrips(t *testing.T) {
	ref := int64(Mod32)/2 - 1
	got, err := Unwrap(0, ref, Mod32)
	if err != nil {
		t.Fatal(err)
	}
	if got != 0 {
		t.Fatalf("got %d", got)
	}
}

func TestUnwrapZeroModulusRejected(t *testing.T) {
	if _, err := Unwrap(1, 1, 0); AsKind(err) != KindInvalidMetadata {
		t.Fatalf("kind = %q", AsKind(err))
	}
}

func TestFromNanosRoundTripsSeconds(t *testing.T) {
	tb, _ := NewTimebase(25)
	got, err := tb.FromNanos(3 * NanosPerSecond)
	if err != nil {
		t.Fatal(err)
	}
	if got != 75 {
		t.Fatalf("got %d want 75", got)
	}
}

func TestFromNanosQuotientOverflow(t *testing.T) {
	// A large timescale can push ns->units past int64 for extreme values.
	tb, _ := NewTimebase(4_000_000_000)
	if _, err := tb.FromNanos(1 << 62); AsKind(err) != KindOverflow {
		t.Fatalf("kind = %q", AsKind(err))
	}
}

func TestUnwrapSeriesMatchesIndividualExpansion(t *testing.T) {
	vals := []uint32{100, 5, 4294967290, 4294967295, 3}
	series, err := UnwrapSeries(vals, Mod32)
	if err != nil {
		t.Fatal(err)
	}
	ref := int64(vals[0])
	for i := 1; i < len(vals); i++ {
		want, _ := Unwrap(vals[i], ref, Mod32)
		if series[i] != want {
			t.Fatalf("index %d series %d want %d", i, series[i], want)
		}
		ref = series[i]
	}
}
