package render

import (
	"testing"

	"mediapack/timeline"
)

func grid(trackID string, start, fps int64, n int, size int, skip map[int]bool) TrackSource {
	ts := TrackSource{TrackID: trackID, Video: true, Required: true}
	for i := 0; i < n; i++ {
		if skip[i] {
			continue
		}
		t := timeline.FrameTime(start, fps, int64(i))
		ts.Frames = append(ts.Frames, SourceFrame{Time: t, Data: []byte{byte(i), byte(i >> 8)}, Size: size})
	}
	return ts
}

func TestRenderDeterministicAndMonotonic(t *testing.T) {
	c := &Compositor{Width: 320, Height: 180, FPS: 30, Seed: 17}
	src := []TrackSource{grid("video-main", 0, 30, 120, 3300, nil)}
	r1, err := c.Render(src, nil, 0, 4*timeline.NanosPerSecond)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := c.Render(src, nil, 0, 4*timeline.NanosPerSecond)
	if err != nil {
		t.Fatal(err)
	}
	if r1.Stats.Frames != 120 || r1.Stats.ValidFrames != 120 || r1.Stats.GapFrames != 0 {
		t.Fatalf("stats=%+v", r1.Stats)
	}
	for i := range r1.Frames {
		if r1.Frames[i].Hash != r2.Frames[i].Hash {
			t.Fatalf("frame %d hash differs across runs", i)
		}
		if i > 0 && r1.Frames[i].Time <= r1.Frames[i-1].Time {
			t.Fatalf("frame %d time not monotonic", i)
		}
	}
	if r1.Stats.SampleBytes != 120*3300 {
		t.Fatalf("sample bytes=%d", r1.Stats.SampleBytes)
	}
}

func TestRenderGapPropagation(t *testing.T) {
	c := &Compositor{Width: 64, Height: 64, FPS: 30, Seed: 1}
	skip := map[int]bool{50: true, 51: true, 52: true, 53: true}
	src := []TrackSource{grid("remote-rtp", 0, 30, 120, 100, skip)}
	r, err := c.Render(src, nil, 0, 4*timeline.NanosPerSecond)
	if err != nil {
		t.Fatal(err)
	}
	if r.Stats.ValidFrames != 116 || r.Stats.GapFrames != 4 {
		t.Fatalf("stats=%+v", r.Stats)
	}
	for _, i := range []int{50, 51, 52, 53} {
		if r.Frames[i].Valid {
			t.Fatalf("frame %d should be a hole", i)
		}
		if r.Frames[i].Quality != QualityGap {
			t.Fatalf("frame %d quality=%v", i, r.Frames[i].Quality)
		}
	}
	// quality mean: (116*0.95 + 4*0.65)/120 = 0.94
	want := (116*QualityValid + 4*QualityGap) / 120
	if d := r.Stats.QualityMean - want; d > 1e-9 || d < -1e-9 {
		t.Fatalf("quality mean=%v want %v", r.Stats.QualityMean, want)
	}
}

func TestRenderTransitionBlend(t *testing.T) {
	c := &Compositor{Width: 32, Height: 32, FPS: 30, Seed: 5}
	a := grid("a", 0, 30, 10, 10, nil)
	b := grid("b", 0, 30, 10, 10, nil)
	b.Required = false
	dur := timeline.FrameInterval(30) * 4
	tr := timeline.Transition{ID: "x", FromTrack: "a", ToTrack: "b", Start: timeline.FrameTime(0, 30, 2), Duration: dur, Kind: "crossfade"}
	withTr, err := c.Render([]TrackSource{a, b}, []timeline.Transition{tr}, 0, timeline.FrameTime(0, 30, 10))
	if err != nil {
		t.Fatal(err)
	}
	without, err := c.Render([]TrackSource{a, b}, nil, 0, timeline.FrameTime(0, 30, 10))
	if err != nil {
		t.Fatal(err)
	}
	// frames inside the transition window must differ from the unblended output
	changed := 0
	for i := 2; i < 6; i++ {
		if withTr.Frames[i].Hash != without.Frames[i].Hash {
			changed++
		}
	}
	if changed == 0 {
		t.Fatal("transition had no effect on blended frames")
	}
	// frames outside the window must be identical
	if withTr.Frames[8].Hash != without.Frames[8].Hash {
		t.Fatal("frame outside transition window changed")
	}
}

func TestRenderHalfOpenWindow(t *testing.T) {
	// a clip-style half-open source window: frames [2, 6) only
	c := &Compositor{Width: 16, Height: 16, FPS: 30, Seed: 3}
	full := grid("v", 0, 30, 10, 10, nil)
	var windowed []SourceFrame
	in := timeline.FrameTime(0, 30, 2)
	out := timeline.FrameTime(0, 30, 6)
	for _, f := range full.Frames {
		if f.Time >= in && f.Time < out { // half-open
			windowed = append(windowed, f)
		}
	}
	if len(windowed) != 4 {
		t.Fatalf("half-open window got %d frames, want 4", len(windowed))
	}
	full.Frames = windowed
	r, err := c.Render([]TrackSource{full}, nil, in, out)
	if err != nil {
		t.Fatal(err)
	}
	if r.Stats.ValidFrames != 4 || r.Stats.GapFrames != 0 {
		t.Fatalf("stats=%+v", r.Stats)
	}
}
