package segment

import (
	"testing"

	"mediqc/internal/clock"
	"mediqc/internal/fault"
)

func TestAlignLadder(t *testing.T) {
	// 24fps anchor with 2s GOPs; 50fps rep with keyframes at the same ticks.
	anchorK := gops(0, 48, 4800)
	rep50K := make([]int64, 0, len(anchorK))
	for _, k := range anchorK {
		tick := clock.FramesToTicks(k, clock.FPS24)
		rep50K = append(rep50K, clock.TicksToFrames(tick, clock.FPS50))
	}
	reps := []Representation{
		{Name: "v24", Rate: clock.FPS24, Keyframes: anchorK},
		{Name: "v50", Rate: clock.FPS50, Keyframes: rep50K},
	}
	total := clock.FramesToTicks(4800, clock.FPS24)
	cuts, err := Align(reps, total, 2*clock.Timescale)
	if err != nil {
		t.Fatal(err)
	}
	if len(cuts) != 101 { // 100 segments + final boundary
		t.Fatalf("want 101 cuts, got %d", len(cuts))
	}
	for _, c := range cuts {
		f24 := c.Frames["v24"]
		f50 := c.Frames["v50"]
		// Same instant: 50fps frame must be within 1 frame of the 24fps cut.
		want := clock.TicksToFrames(clock.FramesToTicks(f24, clock.FPS24), clock.FPS50)
		if d := f50 - want; d < -1 || d > 1 {
			t.Fatalf("cut tick %d: v24=%d v50=%d deviates %d", c.Tick, f24, f50, d)
		}
	}
}

func TestAlignRejectsMisaligned(t *testing.T) {
	reps := []Representation{
		{Name: "a", Rate: clock.FPS24, Keyframes: gops(0, 48, 4800)},
		// GOPs offset by 10 frames: never within 1 frame of anchor cuts.
		{Name: "b", Rate: clock.FPS24, Keyframes: gops(10, 48, 4800)},
	}
	total := clock.FramesToTicks(4800, clock.FPS24)
	_, err := Align(reps, total, 2*clock.Timescale)
	if !fault.IsCode(err, fault.CodeMisalignedCut) {
		t.Fatalf("want E_MISALIGNED_CUT, got %v", err)
	}
}
