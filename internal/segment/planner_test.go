package segment

import (
	"testing"

	"mediqc/internal/fault"
)

func gops(start, step, end int64) []int64 {
	var k []int64
	for f := start; f < end; f += step {
		k = append(k, f)
	}
	return k
}

func TestPlanCutsOnKeyframes(t *testing.T) {
	kfs := gops(0, 48, 4800) // 2s GOP at 24fps
	segs, err := Plan(kfs, 4800, 96)
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) != 50 {
		t.Fatalf("want 50 segments, got %d", len(segs))
	}
	var prev int64
	for i, s := range segs {
		if s.Index != i || s.StartFrame != prev {
			t.Fatalf("segment %d not contiguous: %+v", i, s)
		}
		if !s.KeyframeHit {
			t.Fatalf("segment %d start %d not on keyframe", i, s.StartFrame)
		}
		prev = s.EndFrame
	}
	if prev != 4800 {
		t.Fatalf("segments end at %d, want 4800", prev)
	}
	if got := KeyframeHitRate(segs); got != 1.0 {
		t.Fatalf("hit rate %v", got)
	}
}

func TestPlanLastSegmentClamped(t *testing.T) {
	kfs := gops(0, 50, 1000)
	segs, err := Plan(kfs, 1030, 200) // totalFrames past last keyframe
	if err != nil {
		t.Fatal(err)
	}
	last := segs[len(segs)-1]
	if last.EndFrame != 1030 {
		t.Fatalf("last end %d, want 1030", last.EndFrame)
	}
}

func TestPlanRejectsEmptyIndex(t *testing.T) {
	if _, err := Plan(nil, 100, 10); !fault.IsCode(err, fault.CodeMissingKeyframe) {
		t.Fatalf("want E_MISSING_KEYFRAME, got %v", err)
	}
	if _, err := Plan([]int64{5, 50}, 100, 10); !fault.IsCode(err, fault.CodeMissingKeyframe) {
		t.Fatalf("want E_MISSING_KEYFRAME for nonzero start, got %v", err)
	}
}

func TestPlanSparseKeyframes(t *testing.T) {
	kfs := []int64{0, 300, 900} // sparser than target
	segs, err := Plan(kfs, 1000, 100)
	if err != nil {
		t.Fatal(err)
	}
	// Cuts fall on the available keyframes, then the tail.
	want := []int64{0, 300, 900, 1000}
	for i, s := range segs {
		if s.StartFrame != want[i] || s.EndFrame != want[i+1] {
			t.Fatalf("seg %d = [%d,%d), want [%d,%d)", i, s.StartFrame, s.EndFrame, want[i], want[i+1])
		}
	}
}
