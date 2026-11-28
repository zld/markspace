package timeline_test

import (
	"testing"

	"mediapack/internal/fixture"
	"mediapack/internal/ingest"
	"mediapack/internal/timeline"
)

func spec() fixture.Spec {
	return fixture.Spec{
		Video: fixture.VideoSpec{
			TrackID: 1, Timescale: 24000, Width: 16, Height: 16,
			Delta: 1001, Frames: 6,
		},
		Audio: []fixture.AudioSpec{
			{TrackID: 2, Timescale: 48000, Channels: 2, SampleRate: 48000,
				SampleSize: 16, Delta: 1024, Samples: 4},
			{TrackID: 3, Timescale: 44100, Channels: 6, SampleRate: 44100,
				SampleSize: 16, Delta: 735, Samples: 4},
		},
	}
}

func parse(t *testing.T, s fixture.Spec) *ingest.Container {
	t.Helper()
	dir := t.TempDir()
	p := dir + "/s.mp4"
	if err := writeFile(p, fixture.BuildMP4(s)); err != nil {
		t.Fatal(err)
	}
	c, err := ingest.ParseMP4(p)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func writeFile(p string, b []byte) error {
	return osWriteFile(p, b)
}

func TestComposeTicksAndStableOrder(t *testing.T) {
	c := parse(t, spec())
	tl, err := timeline.Compose(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(tl.Segments) != 3 {
		t.Fatalf("segments = %d, want 3", len(tl.Segments))
	}
	v := tl.VideoSegments()
	if len(v) != 1 || v[0].Kind != "video" || v[0].SampleCount != 6 {
		t.Fatalf("video segment wrong: %+v", v)
	}
	// Points must be ordered by tick, then track, then sample.
	for i := 1; i < len(tl.Points); i++ {
		a, b := tl.Points[i-1], tl.Points[i]
		if a.Tick > b.Tick ||
			(a.Tick == b.Tick && a.TrackIndex > b.TrackIndex) ||
			(a.Tick == b.Tick && a.TrackIndex == b.TrackIndex &&
				a.SampleIndex > b.SampleIndex) {
			t.Fatalf("unstable point order at %d: %+v > %+v", i, a, b)
		}
	}
	// First point of every track is at tick zero.
	for _, seg := range tl.Segments {
		if seg.StartTick != 0 {
			t.Fatalf("segment %d start = %d", seg.GlobalIndex, seg.StartTick)
		}
	}
}

func TestComposeAlignmentErrorUnderOneTick(t *testing.T) {
	c := parse(t, spec())
	tl, err := timeline.Compose(c)
	if err != nil {
		t.Fatal(err)
	}
	if tl.MaxAlignment.MaxErrorTicks > 1 {
		t.Fatalf("max alignment error %d ticks, must be <= 1",
			tl.MaxAlignment.MaxErrorTicks)
	}
	// 44100 Hz track: 735 source units -> 1500 ticks exactly (735*90000/44100).
	audio := tl.AudioSegments()
	var rate44100 *timeline.TrackSegment
	for i := range audio {
		if audio[i].Timescale == 44100 {
			rate44100 = &audio[i]
		}
	}
	if rate44100 == nil {
		t.Fatal("44100 Hz track missing")
	}
	if rate44100.EndTick != 6000 {
		t.Fatalf("44100 end tick = %d, want 6000", rate44100.EndTick)
	}
}

func TestComposeMultipleContainersConcatenated(t *testing.T) {
	c1 := parse(t, spec())
	c2 := parse(t, spec())
	tl, err := timeline.Compose(c1, c2)
	if err != nil {
		t.Fatal(err)
	}
	if len(tl.Segments) != 6 {
		t.Fatalf("segments = %d, want 6", len(tl.Segments))
	}
	seen := map[int]bool{}
	for _, s := range tl.Segments {
		if seen[s.GlobalIndex] {
			t.Fatal("global index reused")
		}
		seen[s.GlobalIndex] = true
	}
}
