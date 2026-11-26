package clock

import "testing"

func TestSampleNanosRoundTrip(t *testing.T) {
	tb, err := NewTimebase(48000)
	if err != nil {
		t.Fatal(err)
	}
	tb.SetEpoch(0)
	// one sample at 48 kHz = 20833.33... ns; SampleToNanos rounds to nearest,
	// NanosToSample floors — the two integer grids cannot be exact inverses.
	cases := []struct {
		sample, nanos int64
	}{
		{0, 0}, {1, 20833}, {2, 41667}, {24000, 500_000_000},
		{48000, 1_000_000_000}, {48001, 1_000_020_833},
	}
	for _, c := range cases {
		if got := tb.SampleToNanos(c.sample); got != c.nanos {
			t.Errorf("SampleToNanos(%d) = %d, want %d", c.sample, got, c.nanos)
		}
	}
	// floor semantics: a nanosecond strictly inside sample k maps back to k-1
	floorCases := []struct {
		nanos, sample int64
	}{
		{0, 0}, {20833, 0}, {20834, 1}, {41666, 1}, {41667, 2},
		{500_000_000, 24000}, {1_000_000_000, 48000},
	}
	for _, c := range floorCases {
		if got := tb.NanosToSample(c.nanos); got != c.sample {
			t.Errorf("NanosToSample(%d) = %d, want %d", c.nanos, got, c.sample)
		}
	}
}

func TestEpochOffset(t *testing.T) {
	tb, _ := NewTimebase(48000)
	tb.SetEpoch(1_000_000)
	if got := tb.SampleToNanos(48); got != 1_000_000+1_000_000 {
		t.Fatalf("epoch offset wrong: %d", got)
	}
	if got := tb.NanosToSample(1_000_000); got != 0 {
		t.Fatalf("epoch sample wrong: %d", got)
	}
}

func TestMonotonicGuard(t *testing.T) {
	tb, _ := NewTimebase(90000)
	tb.SetEpoch(0)
	if _, err := tb.Advance(1000); err != nil {
		t.Fatal(err)
	}
	if _, err := tb.Advance(999); err != nil { // equal-ish, tiny step back
		t.Fatalf("small jitter should pass: %v", err)
	}
	_, err := tb.Advance(1000 - MaxSaneBackwardNanos - 1)
	if e := AsError(err); e == nil || e.Code != CodeTimeBackwards {
		t.Fatalf("want CodeTimeBackwards, got %v", err)
	}
}

func TestRTPTicksConversions(t *testing.T) {
	// 90000 ticks == 1 s
	if got := RTPTicksToNanos(90000); got != NanosPerSecond {
		t.Fatalf("RTPTicksToNanos = %d", got)
	}
	if got := NanosToRTPTicks(NanosPerSecond); got != 90000 {
		t.Fatalf("NanosToRTPTicks = %d", got)
	}
	if got := PCRToNanos(PCRRate); got != NanosPerSecond {
		t.Fatalf("PCRToNanos = %d", got)
	}
}

func TestNewTimebaseRejectsZero(t *testing.T) {
	if _, err := NewTimebase(0); err == nil {
		t.Fatal("expected error for zero rate")
	}
}
