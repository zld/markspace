package timeline

import (
	"errors"
	"math"
	"testing"
)

func TestClockExactConversionNoDrift(t *testing.T) {
	// 44.1 kHz audio is the classic non-dividing rate: conversion must stay
	// exact under rational integer scaling even over a long 12 s timeline.
	c, err := NewClock(44100)
	if err != nil {
		t.Fatal(err)
	}
	var nativeTotal uint64 = 44100 * 12 // 12 s of samples
	var accCommon uint64
	samples := make([]uint64, 0, 12)
	for s := 0; s < 12; s++ {
		samples = append(samples, 44100)
	}
	for _, n := range samples {
		got, rem := c.ToCommonRound(n)
		if rem != 0 && rem >= uint64(c.Timescale) {
			t.Fatalf("remainder %d must be below one native tick", rem)
		}
		accCommon += got
	}
	whole := c.ToCommon(nativeTotal)
	if accCommon != whole {
		t.Fatalf("piecewise accumulation %d drifted from whole conversion %d", accCommon, whole)
	}
	if DurationMs(whole) != 12000 {
		t.Fatalf("duration = %d ms, want 12000", DurationMs(whole))
	}
	// Round trip back to native ticks loses at most one source tick.
	back := c.FromCommon(whole)
	if d := nativeTotal - back; d > 1 {
		t.Fatalf("round trip lost %d native ticks", d)
	}
}

func TestNewClockRejectsBadTimescale(t *testing.T) {
	for _, ts := range []uint32{0, 100_000_001} {
		if _, err := NewClock(ts); !errors.Is(err, ErrBadTimescale) {
			t.Fatalf("timescale %d: got %v, want ErrBadTimescale", ts, err)
		}
	}
}

func TestValidateGraphBackwardTimestamp(t *testing.T) {
	g := &Graph{Tracks: []*Track{{
		ID: 7, Kind: KindAudio, Timescale: 48000, Layout: LayoutStereo, SampleRate: 48000,
		Samples: []TrackSample{
			{DTS: 0, CTS: 0, Dur: 1024},
			{DTS: 900, CTS: 900, Dur: 1024},
			{DTS: 800, CTS: 800, Dur: 1024}, // backward
		},
	}}}
	err := ValidateGraph(g)
	var le *LocError
	if !errors.As(err, &le) {
		t.Fatalf("want LocError, got %T %v", err, err)
	}
	if le.Code != CodeTimestampBack || le.Stage != StageTimeline || le.TrackID != 7 || le.SampleIdx != 2 {
		t.Fatalf("locator wrong: %+v", le)
	}
}

func TestValidateGraphUnsupportedLayout(t *testing.T) {
	g := &Graph{Tracks: []*Track{{
		ID: 3, Kind: KindAudio, Timescale: 48000, Layout: LayoutUnsupported, SampleRate: 48000,
		Samples: []TrackSample{{DTS: 0, CTS: 0, Dur: 1024}},
	}}}
	err := ValidateGraph(g)
	var le *LocError
	if !errors.As(err, &le) || le.Code != CodeBadLayout || le.TrackID != 3 {
		t.Fatalf("got %v, want LAYOUT_UNSUPPORTED on track 3", err)
	}
}

func TestValidateGraphEmptyAndEndConservation(t *testing.T) {
	if err := ValidateGraph(&Graph{}); err == nil {
		t.Fatal("empty graph must fail")
	}
	g := &Graph{Tracks: []*Track{
		{ID: 1, Kind: KindVideo, Timescale: 90000, Samples: []TrackSample{
			{DTS: 0, CTS: 0, Dur: 90000},
			{DTS: 90000, CTS: 90000, Dur: 90000},
		}},
		{ID: 2, Kind: KindAudio, Timescale: 48000, Layout: LayoutMono, SampleRate: 48000,
			Samples: []TrackSample{{DTS: 0, CTS: 0, Dur: 90000}}}, // ends at 90000 vs 180000
	}}
	err := ValidateGraph(g)
	var le *LocError
	if !errors.As(err, &le) || le.Code != CodeDurationConserv || le.TrackID != 2 {
		t.Fatalf("got %v, want DURATION_NOT_CONSERVED track 2", err)
	}
}

func TestValidateGraphAcceptsAlignedTracks(t *testing.T) {
	g := &Graph{Tracks: []*Track{
		{ID: 1, Kind: KindVideo, Timescale: 90000, Color: &ColorDesc{Source: SourceContainer},
			Samples: []TrackSample{{DTS: 0, CTS: 0, Dur: 90000}}},
		{ID: 2, Kind: KindAudio, Timescale: 48000, Layout: LayoutStereo, SampleRate: 48000,
			Samples: []TrackSample{{DTS: 0, CTS: 0, Dur: 90000}}},
	}}
	if err := ValidateGraph(g); err != nil {
		t.Fatalf("aligned graph rejected: %v", err)
	}
}

func TestAssertMonotonic(t *testing.T) {
	tr := &Track{ID: 1, Samples: []TrackSample{{DTS: 1}, {DTS: 1}, {DTS: 2}}}
	if err := AssertMonotonic(tr); err != nil {
		t.Fatalf("equal DTS must be allowed: %v", err)
	}
	tr.Samples = append(tr.Samples, TrackSample{DTS: 1})
	var le *LocError
	if err := AssertMonotonic(tr); !errors.As(err, &le) || le.Code != CodeTimestampBack {
		t.Fatalf("got %v, want TIMESTAMP_BACKWARD", err)
	}
}

func TestGuardOverflow(t *testing.T) {
	if _, ok := GuardOverflow(math.MaxUint64, 2); ok {
		t.Fatal("overflow not detected")
	}
	if v, ok := GuardOverflow(123, 456); !ok || v != 56088 {
		t.Fatal("checked multiply wrong")
	}
}

func TestLayoutAndColorSource(t *testing.T) {
	if LayoutFiveOne.Channels() != 6 || LayoutStereo.String() != "stereo" {
		t.Fatal("layout metadata wrong")
	}
	d1 := ColorDesc{Primaries: 9, Transfer: 16, Matrix: 9, Source: SourceContainer}
	d2 := d1
	d2.Source = SourceBitstream
	if !d1.Equal(d2) {
		t.Fatal("Equal must compare values, not provenance")
	}
	d2.Matrix = 1
	if d1.Equal(d2) {
		t.Fatal("different matrix must compare unequal")
	}
}
