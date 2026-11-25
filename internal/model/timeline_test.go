package model

import (
	"errors"
	"testing"

	"github.com/zld/mediacore/internal/trace"
)

func TestTicksToNSAndHalfOpenIntervals(t *testing.T) {
	// 90 kHz: one tick is 11,111.111... ns.
	ns, err := TicksToNS(9000, 90000)
	if err != nil {
		t.Fatal(err)
	}
	if ns != 100_000_000 {
		t.Fatalf("9000/90000s = %dns", ns)
	}
	if _, err := TicksToNS(1, 0); err == nil {
		t.Fatal("zero timescale must fail")
	}

	i := Interval{Start: 10, End: 20}
	if !i.Contains(10) || i.Contains(20) {
		t.Fatal("interval must be [start,end)")
	}
	got, ok := i.Intersect(Interval{Start: 15, End: 25})
	if !ok || got != (Interval{Start: 15, End: 20}) {
		t.Fatalf("intersection = %+v,%v", got, ok)
	}
}

func TestNormalizeConvertsAndLocatesTimestampRegression(t *testing.T) {
	tr := &Track{ID: 7, TimeScale: 1000, Samples: []*Sample{
		{Index: 0, StartTick: 100, DurationTick: 100, BoxOffset: 100},
		{Index: 1, StartTick: 50, DurationTick: 100, BoxOffset: 200},
	}}
	_, err := Normalize("digest", []*Track{tr}, nil)
	var te *trace.Error
	if !errors.As(err, &te) || te.Code != trace.CodeTimestampRegression {
		t.Fatalf("expected regression, got %v", err)
	}
	if te.TrackID != 7 || !te.HasTrack {
		t.Fatalf("track location missing: %+v", te)
	}
	if te.ByteOffset != 200 || !te.HasByteOffset {
		t.Fatalf("byte location missing: %+v", te)
	}
	if te.TimeNS != 50_000_000 {
		t.Fatalf("time = %d", te.TimeNS)
	}
}

func TestNormalizeRejectsEmptyTrack(t *testing.T) {
	_, err := Normalize("d", []*Track{{ID: 3, TimeScale: 48000}}, nil)
	if !trace.HasCode(err, trace.CodeEmptyTrack) {
		t.Fatalf("expected empty track, got %v", err)
	}
	te, _ := trace.As(err)
	if te.TrackID != 3 {
		t.Fatalf("track = %d", te.TrackID)
	}
}

func TestNormalizeAppliesTrackOffset(t *testing.T) {
	tr := &Track{ID: 1, TimeScale: 10, OffsetNS: 5_000_000_000, Samples: []*Sample{
		{StartTick: 2, DurationTick: 1},
	}}
	tl, err := Normalize("d", []*Track{tr}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if tl.Tracks[0].Start != 5_200_000_000 || tl.Tracks[0].End != 5_300_000_000 {
		t.Fatalf("normalized range = %+v", tl.Tracks[0])
	}
}
