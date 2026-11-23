package kernel

import (
	"os"
	"path/filepath"
	"testing"

	"mediad/internal/ebml"
)

// TestBuildPlanRejectsMisalignmentAndSavesCheckpoint drives alignment through
// the real stage wiring: two renditions whose keyframe grids differ by more
// than one frame must be rejected, and a resumable checkpoint must remain.
func TestBuildPlanRejectsMisalignmentAndSavesCheckpoint(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.WorkDir = dir
	mk := func(id string, frames []int64) *Representation {
		return &Representation{
			ID:        id,
			Bandwidth: 1000,
			Data:      []byte("synthetic"),
			Doc:       &ebml.Doc{TimecodeScale: 1_000_000, Size: 4096},
			Keyframes: frames,
			Duration:  10_000_000_000,
		}
	}
	// 2s grid vs a 2.25s grid: the 250ms offset exceeds one 33.3ms frame.
	reps := []*Representation{
		mk("a", []int64{0, 2e9, 4e9, 6e9, 8e9}),
		mk("b", []int64{0, 2_250_000_000, 4_500_000_000, 6_750_000_000, 9e9}),
	}
	_, err := BuildPlan(reps, cfg)
	if err == nil {
		t.Fatal("misaligned renditions were accepted")
	}
	if CodeOf(err) != ExitInput {
		t.Fatalf("exit code = %d, want input error", CodeOf(err))
	}
	cpPath := filepath.Join(dir, "checkpoint-rejected.json")
	if _, statErr := os.Stat(cpPath); statErr != nil {
		t.Fatalf("resumable checkpoint not written: %v", statErr)
	}
	cp, loadErr := pipelineLoad(cpPath, digestBytes(reps[0].Data), cfg.SegmentConfig(reps[0].Doc.TimecodeScale).Digest())
	if loadErr != nil {
		t.Fatalf("checkpoint unreadable: %v", loadErr)
	}
	if len(cp.Completed) != 0 {
		t.Fatal("rejected alignment marked segments completed")
	}
	if len(cp.Rejections) == 0 {
		t.Fatal("rejection reason not recorded")
	}
}

func TestBuildPlanRequiresTwoRepresentations(t *testing.T) {
	_, err := BuildPlan([]*Representation{{ID: "a"}}, DefaultConfig())
	if err == nil || CodeOf(err) != ExitInput {
		t.Fatalf("err = %v code=%d", err, CodeOf(err))
	}
}
