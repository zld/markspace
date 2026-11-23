package hls

import (
	"errors"
	"testing"

	"mediacheck/internal/clock"
	"mediacheck/internal/fixture"
	"mediacheck/internal/model"
	"mediacheck/internal/safeio"
)

func TestBuildAndParsePlaylist(t *testing.T) {
	prog := fixture.Generate(17, nil)
	if err := fixture.Materialize(prog); err != nil {
		t.Fatal(err)
	}
	text := BuildPlaylist(prog.Video[0], nil)
	pl, err := Parse(text, safeio.DefaultLimits())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(pl.Segments) != 8 {
		t.Fatalf("segments: %d", len(pl.Segments))
	}
	if len(pl.Discontinuities) != 1 {
		t.Fatalf("discontinuities: %d", len(pl.Discontinuities))
	}
	// Segment 4 follows the reset and must carry the flag.
	if !pl.Segments[4].AfterReset || pl.Segments[4].DiscontinuitySeq != 1 {
		t.Fatal("discontinuity sequence not anchored")
	}
	// 0.4 s segments on the 90 kHz master clock.
	want := int64(0.4 * float64(clock.MasterTicksPerSecond))
	for i, s := range pl.Segments {
		if s.EndTick-s.StartTick != want {
			t.Fatalf("segment %d duration ticks %d want %d", i, s.EndTick-s.StartTick, want)
		}
	}
	if pl.TargetDurationTick != clock.MasterTicksPerSecond {
		t.Fatalf("target duration ticks %d", pl.TargetDurationTick)
	}
}

func TestPlaylistMonotonicAcrossReset(t *testing.T) {
	prog := fixture.Generate(17, nil)
	if err := fixture.Materialize(prog); err != nil {
		t.Fatal(err)
	}
	pl, err := Parse(BuildPlaylist(prog.Video[0], nil), safeio.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(pl.Segments); i++ {
		if pl.Segments[i].StartTick < pl.Segments[i-1].StartTick {
			t.Fatalf("backwards start at %d", i)
		}
	}
}

func TestPlaylistBadDuration(t *testing.T) {
	bad := "#EXTM3U\n#EXTINF:0.33333333333,\na.m4s\n#EXT-X-ENDLIST\n"
	if _, err := Parse(bad, safeio.DefaultLimits()); !errors.Is(err, model.ErrLossyTime) {
		t.Fatalf("want lossy time, got %v", err)
	}
}

func TestPlaylistURIWithoutEXTINF(t *testing.T) {
	bad := "#EXTM3U\na.m4s\n#EXT-X-ENDLIST\n"
	if _, err := Parse(bad, safeio.DefaultLimits()); err == nil {
		t.Fatal("expected error for URI without EXTINF")
	}
}

func TestPlaylistDuplicateAndLate(t *testing.T) {
	prog := fixture.Generate(17, []model.FaultKind{
		model.FaultLate, model.FaultDuplicate,
	})
	if err := fixture.Materialize(prog); err != nil {
		t.Fatal(err)
	}
	pl, err := Parse(BuildPlaylist(prog.Video[0], prog.Faults), safeio.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	// 8 base + one duplicate + one late announcement.
	if len(pl.Segments) != 10 {
		t.Fatalf("entries: %d want 10", len(pl.Segments))
	}
}

func TestExactDecimal(t *testing.T) {
	cases := map[[2]int64]string{
		{10000, 25000}: "0.4",
		{19200, 48000}: "0.4",
		{1000, 1000}:   "1.0",
	}
	for in, want := range cases {
		if got := exactDecimal(in[0], in[1]); got != want {
			t.Errorf("exactDecimal(%d,%d)=%q want %q", in[0], in[1], got, want)
		}
	}
}
