package continuity

import (
	"strings"
	"testing"
)

func timelineFixture(t *testing.T) (*TrackMap, *InitSegment) {
	t.Helper()
	init, err := ParseInit("init.mp4", testInit())
	if err != nil {
		t.Fatal(err)
	}
	tm, err := BuildTrackMap([]TrackSource{{Label: "main", Init: init}})
	if err != nil {
		t.Fatal(err)
	}
	return tm, init
}

func sampleAt(idx int, dts, dur Ticks) Sample {
	return Sample{
		TrackKey: "video-avc1", SourceID: 1,
		Segment: "s.m4s", File: "s.m4s", Index: idx,
		DTS: dts, PTS: dts, Duration: dur,
		ByteOffset: int64(idx * 10), ByteLength: 10,
		Data: pattern(byte(idx), 10),
	}
}

func TestTimelineInOrder(t *testing.T) {
	tm, _ := timelineFixture(t)
	tl, err := NewTimeline(tm, DefaultTimelineConfig())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		if err := tl.Add("main", sampleAt(i, Ticks(i*3000), 3000)); err != nil {
			t.Fatalf("add %d: %v", i, err)
		}
	}
	prog, err := tl.Recompute()
	if err != nil {
		t.Fatal(err)
	}
	if prog.Stats.Samples != 8 || prog.Stats.LateRecomputed != 0 || prog.Stats.LateRejected != 0 {
		t.Fatalf("stats=%+v", prog.Stats)
	}
	if prog.Stats.TimeDeltaTicks != 0 || prog.Stats.ByteDelta != 0 {
		t.Fatalf("delta=%+v", prog.Stats)
	}
	if prog.Samples[0].OutputOffset != 0 || prog.Samples[7].OutputOffset != 7 {
		t.Fatalf("output offsets=%d..%d", prog.Samples[0].OutputOffset, prog.Samples[7].OutputOffset)
	}
	if prog.Samples[0].ByteOffset != 0 || prog.Samples[7].ByteOffset != 70 {
		t.Fatalf("source byte offsets=%d..%d", prog.Samples[0].ByteOffset, prog.Samples[7].ByteOffset)
	}
}

func TestTimelineLateWithinWindowRecomputes(t *testing.T) {
	tm, _ := timelineFixture(t)
	cfg := TimelineConfig{LateWindow: 3000, WrapModulus: 1 << 32, WrapGap: 1 << 31}
	tl, err := NewTimeline(tm, cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Leave a hole at 18000, then deliver it late.
	for _, pts := range []Ticks{9000, 12000, 15000, 21000} {
		if err := tl.Add("main", sampleAt(int(pts/3000), pts, 3000)); err != nil {
			t.Fatalf("add %d: %v", pts, err)
		}
	}
	// Highest observed event time is 24000; watermark 21000.
	if wm := tl.Watermark(); wm != 21000 {
		t.Fatalf("watermark=%d want 21000", wm)
	}
	if err := tl.Add("main", sampleAt(100, 18000, 3000)); err != nil {
		t.Fatalf("late add: %v", err)
	}
	prog, err := tl.Recompute()
	if err != nil {
		t.Fatal(err)
	}
	if prog.Stats.LateRecomputed != 1 {
		t.Fatalf("late_recomputed=%d", prog.Stats.LateRecomputed)
	}
	if prog.Stats.Samples != 5 {
		t.Fatalf("samples=%d", prog.Stats.Samples)
	}
	// The hole is filled and the program is ordered by presentation time.
	want := []Ticks{9000, 12000, 15000, 18000, 21000}
	for i, w := range want {
		if prog.Samples[i].PTS != w {
			t.Fatalf("sample %d PTS=%d want %d", i, prog.Samples[i].PTS, w)
		}
	}
	if prog.Samples[3].Source != "main" || prog.Samples[3].Index != 100 {
		t.Fatalf("late sample not placed: %+v", prog.Samples[2])
	}
}

func TestTimelineLateBeyondWindowRejectedAndDeterministic(t *testing.T) {
	tm, _ := timelineFixture(t)
	cfg := TimelineConfig{LateWindow: 3000, WrapModulus: 1 << 32, WrapGap: 1 << 31}
	tl, err := NewTimeline(tm, cfg)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		if err := tl.Add("main", sampleAt(i, Ticks(10000+i*3000), 3000)); err != nil {
			t.Fatalf("add %d: %v", i, err)
		}
	}
	beforeProg, err := tl.Recompute()
	if err != nil {
		t.Fatal(err)
	}
	beforeDigest := beforeProg.Digest(nil, nil)
	late := sampleAt(200, 10000, 3000) // far behind watermark
	err = tl.Add("main", late)
	if err == nil || !IsKind(err, KindLate) {
		t.Fatalf("expected late rejection, got %v", err)
	}
	if loc := LocationOf(err); loc.Sample != 200 {
		t.Fatalf("loc=%+v", loc)
	}
	afterProg, err := tl.Recompute()
	if err != nil {
		t.Fatal(err)
	}
	if afterProg.Stats.LateRejected != 1 {
		t.Fatalf("late_rejected=%d", afterProg.Stats.LateRejected)
	}
	if afterProg.Stats.Samples != 6 {
		t.Fatalf("samples=%d, committed output must be unchanged", afterProg.Stats.Samples)
	}
	// Rejection must not touch the committed output: identical sample plan and
	// therefore identical digest.
	if got := afterProg.Digest(nil, nil); got != beforeDigest {
		t.Fatalf("committed output digest changed after rejection")
	}
	if afterProg.Stats.FailureLocation == "none" || !strings.Contains(afterProg.Stats.FailureLocation, "@") {
		t.Fatalf("failure location=%q", afterProg.Stats.FailureLocation)
	}
}

func TestTimelineOverlapResolutionPrefersPrimary(t *testing.T) {
	init, err := ParseInit("init.mp4", testInit())
	if err != nil {
		t.Fatal(err)
	}
	tm, err := BuildTrackMap([]TrackSource{
		{Label: "main", Init: init},
		{Label: "backup", Init: init},
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := DefaultTimelineConfig()
	cfg.SourceOrder = []string{"main", "backup"}
	tl, err := NewTimeline(tm, cfg)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if err := tl.Add("main", sampleAt(i, Ticks(i*3000), 3000)); err != nil {
			t.Fatal(err)
		}
	}
	// Backup overlaps samples 1..2 with different bytes.
	for i := 1; i <= 2; i++ {
		b := sampleAt(i, Ticks(i*3000), 3000)
		b.Data = pattern(byte(100+i), 10)
		if err := tl.Add("backup", b); err != nil {
			t.Fatal(err)
		}
	}
	prog, err := tl.Recompute()
	if err != nil {
		t.Fatal(err)
	}
	if prog.Stats.Samples != 4 {
		t.Fatalf("samples=%d", prog.Stats.Samples)
	}
	for _, s := range prog.Samples {
		if s.Source != "main" {
			t.Fatalf("overlap should keep main, got %s", s.Source)
		}
	}
	if prog.Stats.OverlapsDropped != 2 {
		t.Fatalf("overlaps=%d", prog.Stats.OverlapsDropped)
	}
}

func TestTimelineIdenticalDuplicateDropped(t *testing.T) {
	init, err := ParseInit("init.mp4", testInit())
	if err != nil {
		t.Fatal(err)
	}
	tm, err := BuildTrackMap([]TrackSource{
		{Label: "main", Init: init}, {Label: "backup", Init: init},
	})
	if err != nil {
		t.Fatal(err)
	}
	tl, err := NewTimeline(tm, DefaultTimelineConfig())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		s := sampleAt(i, Ticks(i*3000), 3000)
		if err := tl.Add("main", s); err != nil {
			t.Fatal(err)
		}
		if err := tl.Add("backup", s); err != nil {
			t.Fatal(err)
		}
	}
	prog, err := tl.Recompute()
	if err != nil {
		t.Fatal(err)
	}
	if prog.Stats.Samples != 3 || prog.Stats.Duplicates != 3 {
		t.Fatalf("stats=%+v", prog.Stats)
	}
}

func TestTimelineGapDetection(t *testing.T) {
	tm, _ := timelineFixture(t)
	tl, err := NewTimeline(tm, DefaultTimelineConfig())
	if err != nil {
		t.Fatal(err)
	}
	tl.Add("main", sampleAt(0, 0, 3000))
	tl.Add("main", sampleAt(1, 9000, 3000)) // 6000-tick gap
	prog, err := tl.Recompute()
	if err != nil {
		t.Fatal(err)
	}
	if prog.Stats.Gaps != 1 {
		t.Fatalf("gaps=%d", prog.Stats.Gaps)
	}
}

func TestTimelineWraparound(t *testing.T) {
	tm, _ := timelineFixture(t)
	tl, err := NewTimeline(tm, DefaultTimelineConfig())
	if err != nil {
		t.Fatal(err)
	}
	const mod = Ticks(1 << 32)
	const last = mod - 3000
	if err := tl.Add("main", sampleAt(0, last, 3000)); err != nil {
		t.Fatal(err)
	}
	// Next sample after wraparound: raw DTS 0 should continue at mod.
	if err := tl.Add("main", sampleAt(1, 0, 3000)); err != nil {
		t.Fatalf("wrap add: %v", err)
	}
	prog, err := tl.Recompute()
	if err != nil {
		t.Fatal(err)
	}
	if len(prog.Samples) != 2 {
		t.Fatalf("samples=%d", len(prog.Samples))
	}
	if prog.Samples[1].DTS != mod {
		t.Fatalf("wrapped DTS=%d want %d", prog.Samples[1].DTS, mod)
	}
	if prog.Samples[1].PTS <= prog.Samples[0].PTS {
		t.Fatalf("PTS did not advance across wrap: %d -> %d", prog.Samples[0].PTS, prog.Samples[1].PTS)
	}
}

func TestTimelineBackwardJump(t *testing.T) {
	cfg := TimelineConfig{LateWindow: 3000, WrapModulus: 1 << 32, WrapGap: 1 << 31}
	t.Run("inside window is late recompute", func(t *testing.T) {
		tm, _ := timelineFixture(t)
		tl, err := NewTimeline(tm, cfg)
		if err != nil {
			t.Fatal(err)
		}
		if err := tl.Add("main", sampleAt(0, 6000, 3000)); err != nil {
			t.Fatal(err)
		}
		// Watermark = 9000-3000 = 6000; PTS 6000 is exactly at the watermark.
		if err := tl.Add("main", sampleAt(1, 6000, 3000)); err != nil {
			t.Fatalf("backward step inside the window must be accepted: %v", err)
		}
		prog, err := tl.Recompute()
		if err != nil {
			t.Fatal(err)
		}
		if prog.Stats.LateRecomputed != 1 {
			t.Fatalf("stats=%+v", prog.Stats)
		}
	})
	t.Run("beyond window is rejected", func(t *testing.T) {
		tm, _ := timelineFixture(t)
		tl, err := NewTimeline(tm, cfg)
		if err != nil {
			t.Fatal(err)
		}
		if err := tl.Add("main", sampleAt(0, 90000, 3000)); err != nil {
			t.Fatal(err)
		}
		err = tl.Add("main", sampleAt(1, 45000, 3000))
		if err == nil || !IsKind(err, KindLate) {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestTimelineZeroDurationAllowed(t *testing.T) {
	tm, _ := timelineFixture(t)
	tl, err := NewTimeline(tm, DefaultTimelineConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := tl.Add("main", sampleAt(0, 0, 0)); err != nil {
		t.Fatalf("zero-duration sample: %v", err)
	}
	prog, err := tl.Recompute()
	if err != nil {
		t.Fatal(err)
	}
	if len(prog.Samples) != 1 || prog.Samples[0].Duration != 0 {
		t.Fatalf("zero-duration handling broken: %+v", prog.Samples)
	}
}

func TestTimelineDeterministicReplay(t *testing.T) {
	run := func() string {
		tm, _ := timelineFixture(t)
		tl, err := NewTimeline(tm, DefaultTimelineConfig())
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 8; i++ {
			if err := tl.Add("main", sampleAt(i, Ticks(i*3000), 3000)); err != nil {
				t.Fatal(err)
			}
		}
		prog, err := tl.Recompute()
		if err != nil {
			t.Fatal(err)
		}
		return prog.Digest([]byte("manifest"), []byte("media"))
	}
	if run() != run() {
		t.Fatal("identical input must yield identical digest")
	}
}

func TestTimelineRejectsUnmappedSource(t *testing.T) {
	tm, _ := timelineFixture(t)
	tl, err := NewTimeline(tm, DefaultTimelineConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := tl.Add("ghost", sampleAt(0, 0, 1000)); err == nil || !IsKind(err, KindMapping) {
		t.Fatalf("err=%v", err)
	}
}

func TestTimelineLateSampleWithinWindowProducesStableDigest(t *testing.T) {
	cfg := DefaultTimelineConfig()
	cfg.LateWindow = 9000
	run := func(withLate bool) string {
		tm, _ := timelineFixture(t)
		tl, _ := NewTimeline(tm, cfg)
		for i := 0; i < 6; i++ {
			if err := tl.Add("main", sampleAt(i, Ticks(i*3000), 3000)); err != nil {
				t.Fatal(err)
			}
		}
		if withLate {
			if err := tl.Add("main", sampleAt(50, 6000, 3000)); err != nil {
				if !IsKind(err, KindLate) {
					t.Fatalf("late: %v", err)
				}
			}
		}
		prog, err := tl.Recompute()
		if err != nil {
			t.Fatal(err)
		}
		return strings.Join([]string{prog.Digest(nil, nil)}, "")
	}
	if run(true) != run(true) {
		t.Fatal("late replay must be deterministic")
	}
}
