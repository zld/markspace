package model

import (
	"math"
	"testing"
)

func TestTimescaleToNsExact(t *testing.T) {
	ts := Timescale{TicksPerSecond: 48000}
	ns, err := ts.ToNs(48000)
	if ns != NsTime(NsPerSecond) || err != 0 {
		t.Fatalf("got %d err=%v", ns, err)
	}
}

func TestTimescaleToNsRounding(t *testing.T) {
	ts := Timescale{TicksPerSecond: 44100}
	// 1 tick = 1e9/44100 ns ≈ 22675.7 ns; nearest is 22676, and the
	// signed error reports that the grid is 0.28 ns late.
	ns, errNs := ts.ToNs(1)
	if ns != 22676 {
		t.Fatalf("ns=%d", ns)
	}
	if errNs <= 0 || errNs >= 1 {
		t.Fatalf("errNs=%v out of range", errNs)
	}
	// At the half-way point rounding moves away from zero.
	half := Timescale{TicksPerSecond: 2_000_000_000}
	if got, _ := half.ToNs(1); got != 1 {
		t.Fatalf("half-round=%d", got)
	}
}

func TestTimescaleToNsSaturates(t *testing.T) {
	ts := Timescale{TicksPerSecond: 1}
	if got, _ := ts.ToNs(^uint64(0)); got != NsTime(math.MaxInt64) {
		t.Fatalf("overflow not saturated: %d", got)
	}
}

func TestCheckMonotonic(t *testing.T) {
	ok := []Sample{
		{TrackID: 1, Start: 0}, {TrackID: 1, Start: 10}, {TrackID: 2, Start: 5},
	}
	if i := CheckMonotonic(ok); i != -1 {
		t.Fatalf("expected -1, got %d", i)
	}
	bad := []Sample{
		{TrackID: 1, Start: 10}, {TrackID: 1, Start: 5},
	}
	if i := CheckMonotonic(bad); i != 1 {
		t.Fatalf("expected 1, got %d", i)
	}
}

func TestErrorModel(t *testing.T) {
	var es Errors
	es.Add(NewError(LevelRecovered, "ingest", "a.mp4", 128, 1, "tfdt rollback"))
	es.Add(NewError(LevelIsolated, "ingest", "a.mp4", 256, 2, "truncated box"))
	if es.MaxLevel() != LevelIsolated {
		t.Fatalf("max=%v", es.MaxLevel())
	}
	if es.CountAt(LevelRecovered) != 2 {
		t.Fatalf("count=%d", es.CountAt(LevelRecovered))
	}
	e := es.All()[0]
	if e.ByteOffset != 128 || e.TrackID != 1 {
		t.Fatalf("error fields lost: %+v", e)
	}
	if got := e.Error(); got == "" {
		t.Fatal("empty error string")
	}
}

func TestInterval(t *testing.T) {
	iv := Interval{Start: 100, End: 250}
	if !iv.Contains(100) || iv.Contains(250) || iv.Len() != 150 {
		t.Fatal("interval semantics broken")
	}
}
