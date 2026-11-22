package clock

import (
	"errors"
	"testing"
)

func TestRationalNormalize(t *testing.T) {
	r, err := New(4, 8)
	if err != nil {
		t.Fatal(err)
	}
	if r.Num != 1 || r.Den != 2 {
		t.Fatalf("got %v, want 1/2", r)
	}
	if _, err := New(1, 0); !errors.Is(err, ErrInvalidDenominator) {
		t.Fatalf("want ErrInvalidDenominator, got %v", err)
	}
}

func TestRationalAddSubExact(t *testing.T) {
	a := Must(1, 90000)  // video timescale
	b := Must(1, 48000)  // audio timescale
	sum, err := a.Add(b)
	if err != nil {
		t.Fatal(err)
	}
	// 1/90000 + 1/48000 = 23/720000
	if sum.Num != 23 || sum.Den != 720000 {
		t.Fatalf("got %v", sum)
	}
	back, err := sum.Sub(b)
	if err != nil {
		t.Fatal(err)
	}
	if back.Cmp(a) != 0 {
		t.Fatalf("round trip failed: %v vs %v", back, a)
	}
}

func TestRationalOverflow(t *testing.T) {
	big := Must(1<<62, 1)
	if _, err := big.Add(big); !errors.Is(err, ErrOverflow) {
		t.Fatalf("want overflow, got %v", err)
	}
	if _, err := big.Mul(Must(1 << 40, 1)); !errors.Is(err, ErrOverflow) {
		t.Fatalf("want mul overflow, got %v", err)
	}
}

func TestTicksRoundTrip(t *testing.T) {
	r := Must(1001, 30000)
	ticks, err := r.Ticks(90000)
	if err != nil {
		t.Fatal(err)
	}
	if ticks != 3003 {
		t.Fatalf("got %d ticks, want 3003", ticks)
	}
}

func TestRegressionDetection(t *testing.T) {
	tl := TrackTimeline{
		TrackID: 7,
		Samples: []Rational{Must(0, 1), Must(1, 1), Must(1, 2), Must(2, 1)},
	}
	ev := DetectRegressions(tl)
	if len(ev) != 1 || ev[0].Index != 2 || ev[0].TrackID != 7 {
		t.Fatalf("got %+v", ev)
	}
	if ev[0].Previous.Cmp(Must(1, 1)) != 0 || ev[0].Current.Cmp(Must(1, 2)) != 0 {
		t.Fatalf("wrong event payload: %+v", ev[0])
	}
}

func TestNonZeroStartAndOffsets(t *testing.T) {
	tracks := []TrackTimeline{
		{TrackID: 1, Kind: "video", Start: Must(0, 1)},
		{TrackID: 2, Kind: "audio", Start: Must(1024, 48000)}, // non-zero start
	}
	corr, err := EstimateOffsets(tracks)
	if err != nil {
		t.Fatal(err)
	}
	if corr[0].NonZeroStart {
		t.Fatal("track 1 should have zero start")
	}
	if !corr[1].NonZeroStart {
		t.Fatal("track 2 non-zero start not flagged")
	}
	if corr[1].Offset.Cmp(Must(1024, 48000)) != 0 {
		t.Fatalf("offset = %v", corr[1].Offset)
	}
}

func TestAVSkewWithinAndBeyondTolerance(t *testing.T) {
	cfg := SyncConfig{MaxSkew: Must(20, 1000)} // 20 ms
	mk := func(audioStart Rational) []TrackTimeline {
		return []TrackTimeline{
			{TrackID: 1, Kind: "video", Start: Must(0, 1),
				Samples: []Rational{Must(0, 1), Must(1, 30)}},
			{TrackID: 2, Kind: "audio", Start: audioStart,
				Samples: []Rational{audioStart, Must(1, 20)}},
		}
	}
	// 10 ms skew: passes.
	skew, err := CheckAVSkew(mk(Must(10, 1000)), mustOffsets(t, mk(Must(10, 1000))), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if skew.Cmp(Must(10, 1000)) != 0 {
		t.Fatalf("skew = %v", skew)
	}
	// 40 ms skew: fails with SkewError, original timestamps intact.
	bad := mk(Must(40, 1000))
	if _, err := CheckAVSkew(bad, mustOffsets(t, bad), cfg); err == nil {
		t.Fatal("want skew error")
	} else {
		var se *SkewError
		if !errors.As(err, &se) {
			t.Fatalf("want SkewError, got %T", err)
		}
	}
	if bad[1].Start.Cmp(Must(40, 1000)) != 0 {
		t.Fatal("original timestamp mutated")
	}
}

func TestDriftRateExact(t *testing.T) {
	a := TrackTimeline{Samples: []Rational{Must(0, 1), Must(1, 1), Must(2, 1)}}
	b := TrackTimeline{Samples: []Rational{Must(0, 1), Must(1001, 1000), Must(2002, 1000)}}
	d, err := DriftRate(a, b)
	if err != nil {
		t.Fatal(err)
	}
	// spans: 2 vs 2.002 → diff -0.002 over 2 intervals → -1/1000
	if d.Cmp(Must(-1, 1000)) != 0 {
		t.Fatalf("drift = %v", d)
	}
}

func mustOffsets(t *testing.T, tracks []TrackTimeline) []Correction {
	t.Helper()
	c, err := EstimateOffsets(tracks)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
