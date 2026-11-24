package timeline

import (
	"testing"

	"archivecheck/internal/manifest"
	"archivecheck/internal/media"
	"archivecheck/internal/merr"
)

func twoSecPlaylist() *manifest.Playlist {
	return &manifest.Playlist{
		Format:             manifest.FormatHLS,
		TargetDurationTick: 2,
		Tracks: []*manifest.Track{{
			ID: 1, Name: "v0", Kind: media.TrackVideo,
			Width: 160, Height: 90, TargetBitrateBps: 800_000,
			Refs: []manifest.SegmentRef{
				{Seq: 1, URI: "v0/seg-1.m4s", DurationNum: 2, DurationDen: 1},
				{Seq: 2, URI: "v0/seg-2.m4s", DurationNum: 2, DurationDen: 1},
			},
		}},
	}
}

func TestClockExactConversion(t *testing.T) {
	// 30 fps: one frame = 1/30 s = 3000 ticks exactly.
	clk, err := NewClock(1, 30, 1)
	if err != nil {
		t.Fatal(err)
	}
	ticks, errNanos := clk.ToTicks(1)
	if ticks != 3000 || errNanos != 0 {
		t.Fatalf("one 30fps frame = %d ticks, err=%d ns", ticks, errNanos)
	}
	// Two 2s segments span 4s = 360000 ticks on the 90kHz clock.
	if got, _ := clk.ToTicks(120); got != 360000 {
		t.Fatalf("120 frames = %d ticks, want 360000", got)
	}
}

func TestClockRoundingErrorBounded(t *testing.T) {
	// 48kHz audio units: one unit = 90000/48000 = 1.875 ticks, rounds to
	// 2 ticks; exact rounding error is 0.125 tick ~= 1388 ns.
	clk, _ := NewClock(1, 48000, 1)
	ticks, errNanos := clk.ToTicks(1)
	if ticks != 2 || errNanos != 1388 {
		t.Fatalf("ticks=%d err=%d, want 2 ticks and ~1388 ns", ticks, errNanos)
	}
	// Error must never exceed half a tick (~5555 ns).
	if errNanos > 1_000_000_000/media.ClockRate/2+1 {
		t.Fatalf("rounding error too large: %d ns", errNanos)
	}
}

func TestComposeBoundariesMatchManifest(t *testing.T) {
	pl := twoSecPlaylist()
	clk, _ := NewClock(1, 30, 1)
	frames := []media.Frame{}
	for seq := uint32(1); seq <= 2; seq++ {
		for i := uint32(0); i < 60; i++ {
			frames = append(frames, media.Frame{
				SegmentSeq: seq, Index: i,
				PTS: int64(i), Duration: 1, // native frame units
				Width: 160, Height: 90, Luma: 100,
			})
		}
	}
	in := ComposeInput{
		Spec:  media.TrackSpec{ID: 1, Name: "v0", Width: 160, Height: 90},
		Clock: clk, Frames: frames,
		SegmentLost:  map[uint32]int{},
		SegmentBytes: map[uint32]int{1: 1000, 2: 1000},
		SegmentTrunc: map[uint32]bool{},
	}
	tl, err := Compose(pl, []ComposeInput{in})
	if err != nil {
		t.Fatalf("compose failed: %v", err)
	}
	if len(tl.Segments) != 2 {
		t.Fatalf("segments = %d", len(tl.Segments))
	}
	s1, s2 := tl.Segments[0], tl.Segments[1]
	if s1.StartTicks != 0 || s1.DurationTicks != 180000 {
		t.Fatalf("seg1 start=%d dur=%d", s1.StartTicks, s1.DurationTicks)
	}
	if s2.StartTicks != 180000 || s2.DurationTicks != 180000 {
		t.Fatalf("seg2 start=%d dur=%d", s2.StartTicks, s2.DurationTicks)
	}
	// First frame of seg2 lands exactly on the boundary; zero drift.
	if s2.Frames[0].PTS != 180000 {
		t.Fatalf("boundary frame PTS = %d, want 180000", s2.Frames[0].PTS)
	}
	if tl.Align.ManifestMicros > 1000 {
		t.Fatalf("alignment error %d us exceeds 1ms", tl.Align.ManifestMicros)
	}
}

func TestComposeRejectsOvershoot(t *testing.T) {
	pl := twoSecPlaylist()
	clk, _ := NewClock(1, 30, 1)
	frames := []media.Frame{{
		SegmentSeq: 1, Index: 0, PTS: 59, Duration: 2,
		Width: 160, Height: 90,
	}}
	in := ComposeInput{
		Spec:  media.TrackSpec{ID: 1, Width: 160, Height: 90},
		Clock: clk, Frames: frames,
		SegmentBytes: map[uint32]int{1: 10},
	}
	_, err := Compose(pl, []ComposeInput{in})
	if err == nil {
		t.Fatal("frame crossing segment boundary must be rejected")
	}
	me, ok := merr.As(err)
	if !ok || me.Kind != merr.KindTimeline {
		t.Fatalf("wrong error: %v", err)
	}
}

func TestComposeMissingTrackInput(t *testing.T) {
	pl := twoSecPlaylist()
	_, err := Compose(pl, nil)
	if err == nil {
		t.Fatal("missing track input must be a timeline error")
	}
}

func TestRepeatedComposeIsDeterministic(t *testing.T) {
	pl := twoSecPlaylist()
	clk, _ := NewClock(1, 30, 1)
	build := func() *Timeline {
		var frames []media.Frame
		for seq := uint32(1); seq <= 2; seq++ {
			for i := uint32(0); i < 60; i++ {
				frames = append(frames, media.Frame{
					SegmentSeq: seq, Index: i, PTS: int64(i), Duration: 1,
					Width: 160, Height: 90, Luma: uint8(i * 4),
				})
			}
		}
		in := ComposeInput{
			Spec:  media.TrackSpec{ID: 1, Width: 160, Height: 90},
			Clock: clk, Frames: frames,
			SegmentBytes: map[uint32]int{1: 1, 2: 1},
		}
		tl, err := Compose(pl, []ComposeInput{in})
		if err != nil {
			t.Fatal(err)
		}
		return tl
	}
	a, b := build(), build()
	if len(a.Segments) != len(b.Segments) {
		t.Fatal("segment count differs across runs")
	}
	for i := range a.Segments {
		sa, sb := a.Segments[i], b.Segments[i]
		if sa.StartTicks != sb.StartTicks || sa.DurationTicks != sb.DurationTicks {
			t.Fatal("timestamps differ across repeated runs")
		}
		for j := range sa.Frames {
			if sa.Frames[j].PTS != sb.Frames[j].PTS {
				t.Fatal("frame PTS differs across repeated runs")
			}
		}
	}
}
