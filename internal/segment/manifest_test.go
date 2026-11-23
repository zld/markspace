package segment

import (
	"os"
	"path/filepath"
	"testing"

	"mediaverify/internal/clock"
	"mediaverify/internal/subtitle"
	"mediaverify/internal/timeline"
)

func buildPlan(t *testing.T) Plan {
	t.Helper()
	fr := clock.FrameRate25()
	planner, err := NewPlanner(fr, clock.Nanoseconds(2_000_000_000))
	if err != nil {
		t.Fatal(err)
	}
	// Keyframes every 2s exactly on the grid for 6s of content.
	var events []timeline.Event
	for i := int64(0); i <= 6; i++ {
		events = append(events, timeline.Event{
			Seq: uint64(i + 1), Time: clock.Nanoseconds(i * 2_000_000_000), Keyframe: true,
		})
	}
	intervals := []timeline.Interval{{
		Start:  clock.Nanoseconds(2_500_000_000),
		End:    clock.Nanoseconds(3_500_000_000),
		Reason: "burst-loss",
	}}
	plan, err := planner.Plan(events, intervals, clock.Nanoseconds(6_000_000_000))
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestPlanAlignmentAndUntrusted(t *testing.T) {
	plan := buildPlan(t)
	if len(plan.Segments) != 3 {
		t.Fatalf("want 3 segments, got %d", len(plan.Segments))
	}
	if plan.AlignmentMaxFrame > 1 {
		t.Fatalf("alignment %d frames exceeds one-frame limit", plan.AlignmentMaxFrame)
	}
	if !plan.Segments[1].Untrusted || plan.Segments[0].Untrusted {
		t.Fatalf("untrusted marking wrong: %+v", plan.Segments)
	}
	if plan.RecoveredCount() != 1 {
		t.Fatalf("want 1 recovery, got %d", plan.RecoveredCount())
	}
	if len(plan.DegradedIntervals()) != 1 {
		t.Fatal("degraded interval not reported")
	}
}

func TestManifestSharedCutsAndSubtitleSpans(t *testing.T) {
	plan := buildPlan(t)
	cues := []subtitle.Cue{
		{Index: 1, Start: clock.Nanoseconds(1_900_000_000), End: clock.Nanoseconds(2_100_000_000)},
		{Index: 2, Start: clock.Nanoseconds(4_000_000_000), End: clock.Nanoseconds(4_500_000_000)},
	}
	rungs := []RenditionInput{
		{TargetBPS: 400_000, ActualBPS: 400_000},
		{TargetBPS: 900_000, ActualBPS: 950_000},
	}
	m, err := BuildManifest(clock.FrameRate25(), "deadbeef", plan, rungs, cues, Stats{})
	if err != nil {
		t.Fatal(err)
	}
	// Both renditions carry byte-identical cuts.
	if len(m.Renditions) != 2 {
		t.Fatal("want two renditions")
	}
	for i := range plan.Segments {
		if m.Renditions[0].SegmentNS[i][0] != m.Renditions[1].SegmentNS[i][0] {
			t.Fatalf("cut %d differs across renditions", i)
		}
	}
	// Cue 1 straddles the cut at 2s and must attach to both segments with
	// its original, unmodified time interval.
	if len(m.SubtitleSpans) != 2 {
		t.Fatalf("want 2 spans, got %d", len(m.SubtitleSpans))
	}
	span0 := m.SubtitleSpans[0]
	if len(span0.SegmentIndex) != 2 || span0.SegmentIndex[0] != 0 || span0.SegmentIndex[1] != 1 {
		t.Fatalf("cross-cut cue must attach to two segments: %+v", span0)
	}
	if span0.StartNS != 1_900_000_000 || span0.EndNS != 2_100_000_000 {
		t.Fatal("cue time interval was rewritten")
	}
}

func TestManifestHashStableAcrossRuns(t *testing.T) {
	plan := buildPlan(t)
	rungs := []RenditionInput{{400_000, 400_000}, {900_000, 900_000}}
	m1, _ := BuildManifest(clock.FrameRate25(), "abc", plan, rungs, nil, Stats{PacketsLost: 2})
	m2, _ := BuildManifest(clock.FrameRate25(), "abc", plan, rungs, nil, Stats{PacketsLost: 2})
	h1, err := m1.Hash()
	if err != nil {
		t.Fatal(err)
	}
	h2, _ := m2.Hash()
	if h1 != h2 {
		t.Fatalf("identical manifests hashed differently: %s vs %s", h1, h2)
	}
}

func TestManifestAtomicWrite(t *testing.T) {
	plan := buildPlan(t)
	rungs := []RenditionInput{{400_000, 400_000}, {900_000, 900_000}}
	m, _ := BuildManifest(clock.FrameRate25(), "abc", plan, rungs, nil, Stats{})
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "manifest.json")
	if err := m.WriteAtomic(path); err != nil {
		t.Fatalf("atomic write: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("manifest missing after write: %v", err)
	}
	// No temp files left behind.
	matches, _ := filepath.Glob(filepath.Join(dir, "nested", ".manifest-*.tmp"))
	if len(matches) != 0 {
		t.Fatalf("temp file not cleaned: %v", matches)
	}
}

func TestPlanFlagsCutBeyondOneFrame(t *testing.T) {
	fr := clock.FrameRate25()
	planner, _ := NewPlanner(fr, clock.Nanoseconds(2_000_000_000))
	// Keyframe 5 frames (200ms) away from the 2s grid cut.
	events := []timeline.Event{
		{Seq: 1, Time: 0, Keyframe: true},
		{Seq: 2, Time: clock.Nanoseconds(2_200_000_000), Keyframe: true},
	}
	plan, err := planner.Plan(events, nil, clock.Nanoseconds(4_000_000_000))
	if err != nil {
		t.Fatalf("distant keyframe must degrade, not error: %v", err)
	}
	if !plan.Segments[0].Untrusted || !plan.Segments[1].Untrusted {
		t.Fatal("segments touching a keyless cut must be untrusted")
	}
	// Trusted cuts still respect the one-frame alignment bound.
	if plan.AlignmentMaxFrame > 1 {
		t.Fatalf("trusted alignment %d exceeds one frame", plan.AlignmentMaxFrame)
	}
}
