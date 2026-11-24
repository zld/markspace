package pipeline

import (
	"testing"

	"github.com/zld/mediapipe/index"
	"github.com/zld/mediapipe/media"
)

func tr(start, end int64, keys ...int64) *index.TrackIndex {
	t := &index.TrackIndex{StartUS: start, EndUS: end, SampleCount: int(end/33_333) + 1}
	for i, k := range keys {
		t.Keyframes = append(t.Keyframes, index.Keyframe{Sample: i + 1, TimeUS: k})
	}
	return t
}

func ms(v int64) media.Time { return media.Time(v) * media.Time(1_000_000) }

func TestPlanCutsAtKeyframes(t *testing.T) {
	ref := tr(0, 8_000_000, 0, 1_000_000, 2_000_000, 3_000_000, 4_000_000, 5_000_000, 6_000_000, 7_000_000)
	plan, err := Plan(ref, nil, PlanConfig{Segment: ms(2000), MaxDeviation: ms(40)})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 4 {
		t.Fatalf("segments=%d want 4", len(plan))
	}
	want := [][2]int64{{0, 2_000_000}, {2_000_000, 4_000_000}, {4_000_000, 6_000_000}, {6_000_000, 8_000_000}}
	for i, p := range plan {
		if p.StartUS != want[i][0] || p.EndUS != want[i][1] {
			t.Fatalf("segment %d = %d..%d want %v", i+1, p.StartUS, p.EndUS, want[i])
		}
	}
	// Continuity is required, not assumed.
	for i := 1; i < len(plan); i++ {
		if plan[i].StartUS != plan[i-1].EndUS {
			t.Fatalf("segments not contiguous at %d", i)
		}
	}
}

func TestPlanDeviationWithinOneFramePasses(t *testing.T) {
	ref := tr(0, 8_000_000, 0, 1_000_000, 2_000_000, 3_000_000, 4_000_000, 5_000_000, 6_000_000, 7_000_000)
	layer := tr(0, 8_000_000, 0, 1_033_333, 2_033_333, 3_033_333, 4_033_333, 5_033_333, 6_033_333, 7_033_333)
	plan, err := Plan(ref, []*index.TrackIndex{layer}, PlanConfig{Segment: ms(2000), MaxDeviation: ms(40)})
	if err != nil {
		t.Fatal(err)
	}
	if plan[1].MaxDeviationUS != 33_333 {
		t.Fatalf("deviation=%d want 33333", plan[1].MaxDeviationUS)
	}
	if plan[0].MaxDeviationUS != 0 {
		t.Fatalf("first segment deviation=%d", plan[0].MaxDeviationUS)
	}
}

func TestPlanFailsBeyondThreshold(t *testing.T) {
	ref := tr(0, 8_000_000, 0, 1_000_000, 2_000_000, 3_000_000, 4_000_000, 5_000_000, 6_000_000, 7_000_000)
	// Layer keyframes drift 900ms from the shared boundaries.
	layer := tr(0, 8_000_000, 0, 1_900_000, 2_900_000, 3_900_000, 4_900_000, 5_900_000, 6_900_000, 7_900_000)
	_, err := Plan(ref, []*index.TrackIndex{layer}, PlanConfig{Segment: ms(2000), MaxDeviation: ms(40)})
	if err == nil {
		t.Fatal("expected deviation failure")
	}
	if !media.IsKind(err, media.KindMissingKeyframe) {
		t.Fatalf("error kind = %v", media.Category(err))
	}
}

func TestPlanRejectsBadConfig(t *testing.T) {
	ref := tr(0, 1_000_000, 0, 500_000)
	if _, err := Plan(ref, nil, PlanConfig{Segment: 0}); err == nil {
		t.Fatal("zero segment should fail")
	}
	if _, err := Plan(nil, nil, PlanConfig{Segment: ms(1000)}); err == nil {
		t.Fatal("nil reference should fail")
	}
	if _, err := Plan(&index.TrackIndex{StartUS: 0, EndUS: 0}, nil, PlanConfig{Segment: ms(1000)}); err == nil {
		t.Fatal("empty track should fail")
	}
}

func TestPlanIsDeterministic(t *testing.T) {
	ref := tr(0, 8_000_000, 0, 1_000_000, 2_000_000, 3_000_000, 4_000_000, 5_000_000, 6_000_000, 7_000_000)
	a, err := Plan(ref, nil, PlanConfig{Segment: ms(2000), MaxDeviation: ms(40)})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Plan(ref, nil, PlanConfig{Segment: ms(2000), MaxDeviation: ms(40)})
	for i := range a {
		if a[i].StartUS != b[i].StartUS || a[i].EndUS != b[i].EndUS {
			t.Fatal("non-deterministic plan")
		}
	}
}
