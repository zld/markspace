package media

import "testing"

func TestBuildTrackLineVFRPreserved(t *testing.T) {
	tb, _ := NewTimeBase(1000) // 1 tick = 1 ms
	// Deliberately non-constant durations: 10ms, 30ms, 25ms, 40ms.
	durations := []uint32{10, 30, 25, 40}
	raw := make([]RawSample, len(durations))
	var tick int64
	for i, d := range durations {
		raw[i] = RawSample{MediaTick: tick, DurationTick: d, IsSync: i%2 == 0, Source: SourceDecoded}
		tick += int64(d)
	}
	line, err := BuildTrackLine(1, KindVideo, tb, raw, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	gaps := line.Gaps()
	wantGaps := []int64{10_000_000, 30_000_000, 25_000_000}
	for i, g := range gaps {
		if g.GapNanos != wantGaps[i] {
			t.Errorf("gap %d = %d, want %d (VFR interval must be preserved)", i, g.GapNanos, wantGaps[i])
		}
		if !g.Deterministic {
			t.Errorf("gap %d must be deterministic for decoded points", i)
		}
	}
	if !line.Monotonic {
		t.Error("line must be monotonic")
	}
}

func TestBuildTrackLineWrappedIsMonotonic(t *testing.T) {
	tb, _ := NewTimeBase(90000)
	w := NewWrapper(32)
	raws := []uint32{0xFFFFFFF0, 0x00000010, 0x00000050}
	raw := make([]RawSample, len(raws))
	for i, r := range raws {
		raw[i] = RawSample{MediaTick: w.Expand(r), DurationTick: 0x40, Source: SourceDecoded}
	}
	line, err := BuildTrackLine(2, KindVideo, tb, raw, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if !line.CheckMonotonic() {
		t.Fatal("wrapped stream must expand to a monotonic timeline")
	}
	if line.Points[1].StartNanos <= line.Points[0].StartNanos {
		t.Fatal("wrap boundary must not produce a backwards timestamp")
	}
}

func TestInferredPointsFlagged(t *testing.T) {
	tb, _ := NewTimeBase(1000)
	raw := []RawSample{
		{MediaTick: 0, DurationTick: 10, Source: SourceDecoded},
		{MediaTick: 10, DurationTick: 10, Source: SourceInferred},
	}
	line, err := BuildTrackLine(3, KindVideo, tb, raw, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if !line.Undecidable {
		t.Error("truncated index must keep Undecidable=true")
	}
	if line.Gaps()[0].Deterministic {
		t.Error("gap touching an inferred point must be non-deterministic")
	}
}

func TestApplyEditsWithEmptyEdit(t *testing.T) {
	tb, _ := NewTimeBase(1000)
	raw := []RawSample{
		{MediaTick: 0, DurationTick: 10, Source: SourceDecoded},
		{MediaTick: 10, DurationTick: 10, Source: SourceDecoded},
		{MediaTick: 20, DurationTick: 10, Source: SourceDecoded},
	}
	edits := []Edit{
		{MovieStartNanos: 0, MovieDurationNanos: 5_000_000, MediaStartNanos: -1}, // empty 5ms
		{MovieStartNanos: 5_000_000, MovieDurationNanos: 30_000_000, MediaStartNanos: 0},
	}
	line, err := BuildTrackLine(4, KindAudio, tb, raw, edits, false)
	if err != nil {
		t.Fatal(err)
	}
	if line.Points[0].StartNanos != 5_000_000 {
		t.Errorf("first point after empty edit at %d, want 5ms", line.Points[0].StartNanos)
	}
	if !line.Monotonic {
		t.Error("edited line must stay monotonic")
	}
}

func TestEditMatchingNoSamplesErrors(t *testing.T) {
	tb, _ := NewTimeBase(1000)
	raw := []RawSample{{MediaTick: 0, DurationTick: 10, Source: SourceDecoded}}
	edits := []Edit{{MovieDurationNanos: 10_000_000, MediaStartNanos: 999_000_000}}
	if _, err := BuildTrackLine(5, KindVideo, tb, raw, edits, false); err == nil {
		t.Fatal("edit segment without matching samples must error, not silently pass")
	}
}
