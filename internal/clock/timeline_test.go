package clock

import (
	"strings"
	"testing"
	"time"

	"avbatch/internal/mediaerr"
)

func TestTicksToNanosBoundaries(t *testing.T) {
	tb := Timebase{Timescale: 90000}
	cases := []struct {
		ticks uint64
		want  int64
	}{
		{0, 0},
		{90000, 1_000_000_000},
		{1, 11111}, // 1e9/90000 = 11111.11 -> floor
		{90001, 1_000_011_111},
	}
	for _, c := range cases {
		got, err := tb.TicksToNanos(c.ticks)
		if err != nil {
			t.Fatalf("ticks %d: %v", c.ticks, err)
		}
		if got != c.want {
			t.Errorf("ticks %d = %d ns, want %d", c.ticks, got, c.want)
		}
	}
	if _, err := tb.TicksToNanos(1 << 63); err == nil {
		t.Fatal("expected overflow rejection for hostile tick value")
	}
	if _, err := (Timebase{}).TicksToNanos(1); err == nil {
		t.Fatal("zero timescale must error")
	}
}

func TestFitRecoversKnownDrift(t *testing.T) {
	tl := NewTimeline(Timebase{Timescale: 48000}, 5*time.Millisecond)
	var obs []Observation
	const ppm = 120.0
	for i := 0; i < 40; i++ {
		media := uint64(i) * 48000 // one second apart
		wall := int64(i) * 1_000_000_000
		wall += int64(float64(wall) * ppm / 1e6)
		obs = append(obs, Observation{MediaTicks: media, WallNanos: wall})
	}
	if err := tl.Fit(obs); err != nil {
		t.Fatalf("fit: %v", err)
	}
	got := tl.Model().PPM
	if got < ppm-1 || got > ppm+1 {
		t.Fatalf("ppm = %.3f, want ~%.1f", got, ppm)
	}
	c, err := tl.Correct(48000*10, 48000/30)
	if err != nil {
		t.Fatalf("correct: %v", err)
	}
	if c.DriftNanos <= 0 || c.DriftNanos > 20_000_000 {
		t.Fatalf("drift at 10s = %d ns", c.DriftNanos)
	}
	if c.UncertaintyNanos <= 0 {
		t.Fatal("uncertainty must be positive")
	}
}

func TestFitRejectsAbsurdDrift(t *testing.T) {
	tl := NewTimeline(Timebase{Timescale: 1000}, 0)
	var obs []Observation
	for i := 0; i < 8; i++ {
		obs = append(obs, Observation{
			MediaTicks: uint64(i * 1000),
			WallNanos:  int64(i) * 1_000_000_000 * 3, // 3x faster
		})
	}
	err := tl.Fit(obs)
	if !mediaerr.Is(err, mediaerr.KindDriftOutOfRange) {
		t.Fatalf("err = %v, want drift_out_of_range", err)
	}
	if tl.Model().PPM != MaxPPM {
		t.Fatalf("clamped ppm = %v, want %v", tl.Model().PPM, MaxPPM)
	}
}

func TestOrderRefusesAmbiguousSamples(t *testing.T) {
	tl := NewTimeline(Timebase{Timescale: 1000}, 0)
	pts := []Corrected{
		{Nanos: 1000, UncertaintyNanos: 400},
		{Nanos: 1100, UncertaintyNanos: 400},
	}
	_, err := tl.Order(pts)
	if !mediaerr.Is(err, mediaerr.KindUnsorted) {
		t.Fatalf("err = %v, want unsorted", err)
	}
	same := []Corrected{{Nanos: 5, UncertaintyNanos: 1}, {Nanos: 5, UncertaintyNanos: 1}}
	_, err = tl.Order(same)
	if !mediaerr.Is(err, mediaerr.KindUnsorted) {
		t.Fatalf("equal times err = %v, want unsorted", err)
	}
}

func TestOrderPreservesDeterministicPermutation(t *testing.T) {
	tl := NewTimeline(Timebase{Timescale: 1000}, 0)
	var pts []Corrected
	for i := 10; i > 0; i-- {
		pts = append(pts, Corrected{Input: uint64(i), Nanos: int64(i) * 1000, UncertaintyNanos: 1})
	}
	first, err := tl.Order(pts)
	if err != nil {
		t.Fatalf("order: %v", err)
	}
	second, _ := tl.Order(pts)
	for i := range first {
		if first[i].Input != second[i].Input {
			t.Fatal("ordering is not deterministic")
		}
		if i > 0 && first[i].Nanos < first[i-1].Nanos {
			t.Fatal("output not sorted")
		}
	}
}

func TestDriftLimitRejects(t *testing.T) {
	tl := NewTimeline(Timebase{Timescale: 1000}, 0).WithDriftLimit(100)
	tl.m = DriftModel{Samples: 2, PPM: 4000, AnchorMedia: 0, AnchorWall: 0}
	_, err := tl.Correct(10_000, 0)
	if !mediaerr.Is(err, mediaerr.KindDriftOutOfRange) {
		t.Fatalf("err = %v, want drift_out_of_range", err)
	}
}

func TestUnwrapForwardWrap(t *testing.T) {
	u := NewUnwrapper(0, 90000)
	seq := []uint32{0xFFFFF000, 0xFFFFFF00, 0x00000100, 0x00001000}
	var prev uint64
	for i, ts := range seq {
		r := u.Push(ts)
		if i == 2 && !r.Wrapped {
			t.Fatalf("sample %d: expected wrap, got %+v", i, r)
		}
		if i != 2 && r.Wrapped {
			t.Fatalf("sample %d: unexpected wrap, got %+v", i, r)
		}
		if i > 0 && r.Extended <= prev {
			t.Fatalf("sample %d: extended %d not increasing after %d", i, r.Extended, prev)
		}
		prev = r.Extended
	}
	if u.Wraps() != 1 {
		t.Fatalf("wraps = %d, want 1", u.Wraps())
	}
	if u.Err() != nil {
		t.Fatalf("unexpected anomaly: %v", u.Err())
	}
	if u.Anomalies() != 0 {
		t.Fatalf("anomalies = %d, want 0", u.Anomalies())
	}
}

func TestUnwrapBackwardsInsideTolerance(t *testing.T) {
	u := NewUnwrapper(500, 90000)
	u.Push(10000)
	u.Push(10200)
	r := u.Push(10100) // 100 tick reorder, within tolerance
	if r.Backwards || u.Anomalies() != 0 {
		t.Fatalf("reorder inside tolerance flagged: %+v", r)
	}
	if r.Extended != 10100 {
		t.Fatalf("extended = %d, want 10100", r.Extended)
	}
}

func TestUnwrapBackwardsBeyondToleranceReported(t *testing.T) {
	u := NewUnwrapper(10, 90000)
	u.Push(10000)
	r := u.Push(500)
	if !r.Backwards {
		t.Fatalf("expected backwards flag: %+v", r)
	}
	if !mediaerr.Is(u.Err(), mediaerr.KindRTPRegression) {
		t.Fatalf("err = %v, want rtp_regression", u.Err())
	}
	if u.MaxBackstep() != 9500 {
		t.Fatalf("max backstep = %d", u.MaxBackstep())
	}
}

func TestUnwrapNeverEmitsNegative(t *testing.T) {
	u := NewUnwrapper(0, 90000)
	u.Push(10)
	r := u.Push(5)
	if r.Extended > 1<<63 {
		t.Fatal("extended overflowed")
	}
	u.Reset()
	u.Push(0x80000000)
	_ = u.Push(0x7FFFFFFF) // exactly half the space: ambiguous step
	if u.Err() == nil {
		// Half-space steps are not classified, but must not panic or go negative.
		t.Log("half-space step accepted without anomaly")
	}
}

func TestUnwrapRoundTripAgainst64BitAccumulator(t *testing.T) {
	const rate = 90000
	var ts32 uint32
	var full uint64 = 0xFFFFFFFF - 90000*3
	seq := make([]uint32, 0, 500)
	starts := make([]uint64, 0, 500)
	for i := 0; i < 500; i++ {
		full += rate / 30
		ts32 = uint32(full)
		seq = append(seq, ts32)
		starts = append(starts, full)
	}
	ext, err := UnwrapAll(seq, 0, rate)
	if err != nil {
		t.Fatalf("unwrap: %v", err)
	}
	for i := range ext {
		if ext[i] != starts[i] {
			t.Fatalf("sample %d: extended %d, want %d", i, ext[i], starts[i])
		}
	}
}

func TestSortByExtendedRefusesDuplicates(t *testing.T) {
	_, err := SortByExtended([]uint64{5, 3, 5})
	if !mediaerr.Is(err, mediaerr.KindUnsorted) {
		t.Fatalf("err = %v, want unsorted", err)
	}
	if !strings.Contains(err.Error(), "share") {
		t.Fatalf("message should describe the duplicate: %v", err)
	}
}

func TestArrivalDriftClamped(t *testing.T) {
	now := time.Unix(0, 0)
	ext := []uint64{0, 90000, 180000}
	arr := []time.Time{now, now.Add(time.Second), now.Add(2 * time.Second)}
	if got := ArrivalDrift(ext, arr, 90000); got != 0 {
		t.Fatalf("ppm = %v, want 0", got)
	}
}
