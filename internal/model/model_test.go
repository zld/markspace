package model

import "testing"

func TestRationalRoundTrip(t *testing.T) {
	cases := []struct {
		name  string
		base  Rational
		ticks int64
		want  int64
		exact bool // nanoseconds are an exact multiple, so ticks round-trip
	}{
		{"90kHz tick floor", Rational{Num: 1, Den: 90000}, 1, 11111, false},
		{"90kHz 90k ticks", Rational{Num: 1, Den: 90000}, 90000, 1_000_000_000, true},
		{"1kHz", Rational{Num: 1, Den: 1000}, 1500, 1_500_000_000, true},
		{"48kHz", Rational{Num: 1, Den: 48000}, 48000, 1_000_000_000, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ns, err := tc.base.ToNanos(tc.ticks)
			if err != nil {
				t.Fatalf("ToNanos: %v", err)
			}
			if ns != tc.want {
				t.Fatalf("ToNanos = %d, want %d", ns, tc.want)
			}
			if !tc.exact {
				return
			}
			back, err := tc.base.ToTicks(ns)
			if err != nil {
				t.Fatalf("ToTicks: %v", err)
			}
			if back != tc.ticks {
				t.Fatalf("round trip ticks = %d, want %d", back, tc.ticks)
			}
		})
	}
}

func TestRationalOverflow(t *testing.T) {
	base := Rational{Num: 1, Den: 90000}
	if _, err := base.ToNanos(1 << 62); err != ErrOverflow {
		t.Fatalf("expected ErrOverflow, got %v", err)
	}
	if _, err := base.ToTicks(1 << 62); err != ErrOverflow {
		t.Fatalf("expected ErrOverflow on ToTicks, got %v", err)
	}
	if err := (Rational{Num: 1, Den: 0}).Validate(); err == nil {
		t.Fatal("expected validation error on zero denominator")
	}
	if _, err := base.ToNanos(-1); err != ErrOverflow {
		t.Fatalf("negative ticks must be rejected, got %v", err)
	}
}

func TestCompareNanos(t *testing.T) {
	b90k := Rational{Num: 1, Den: 90000}
	b1k := Rational{Num: 1, Den: 1000}
	cmp, err := CompareNanos(90000, b90k, 1000, b1k)
	if err != nil || cmp != 0 {
		t.Fatalf("equal times: cmp=%d err=%v", cmp, err)
	}
	cmp, _ = CompareNanos(45000, b90k, 1000, b1k)
	if cmp != -1 {
		t.Fatalf("expected -1, got %d", cmp)
	}
	// Big values must not wrap: each side represents (1<<40) seconds.
	cmp, err = CompareNanos(90000*(1<<40), b90k, 1000*(1<<40), b1k)
	if err != nil || cmp != 0 {
		t.Fatalf("large equal times: cmp=%d err=%v", cmp, err)
	}
}

func TestSecondsHelpers(t *testing.T) {
	ns, err := SecondsToNanos(1.5)
	if err != nil || ns != 1_500_000_000 {
		t.Fatalf("SecondsToNanos = %d, %v", ns, err)
	}
	if _, err := SecondsToNanos(-1); err != ErrOverflow {
		t.Fatalf("negative seconds: %v", err)
	}
	if got := NanosToSeconds(2_500_000_000); got != 2.5 {
		t.Fatalf("NanosToSeconds = %v", got)
	}
}

func TestStableKey(t *testing.T) {
	a := Anomaly{ID: "x", Type: AnomalyGap, TrackID: 2, StartNanos: 9, SourceOffset: 4}
	if a.StableKey() != "2|9|4|GAP|x" {
		t.Fatalf("unexpected key %q", a.StableKey())
	}
}
