package pipeline

import (
	"path/filepath"
	"testing"
)

func TestVideoDelayAppliesOnceAndTimelineContiguous(t *testing.T) {
	out := filepath.Join(t.TempDir(), "vd.mp4")
	cfg := baseCfg(t, "vdelay", out)
	cfg.VideoDelayTicks = 500
	cfg.SyncToleranceTicks = 1 << 20
	e, _ := NewEngine(cfg)
	rep := e.Run()
	if rep.FirstError != nil {
		t.Fatal(rep.FirstError)
	}
	if rep.SyncOffsetTicks != -500 {
		t.Fatalf("sync offset = %d, want -500 (audio 0 vs video 500)", rep.SyncOffsetTicks)
	}
	ivs := e.Store().Intervals("video")
	if len(ivs) != demoVideoFrames {
		t.Fatalf("video intervals = %d, want %d", len(ivs), demoVideoFrames)
	}
	if ivs[0].Start != 500 {
		t.Fatalf("first video start = %d, want 500", ivs[0].Start)
	}
	for i := 1; i < len(ivs); i++ {
		if ivs[i].Start != ivs[i-1].End {
			t.Fatalf("video interval %d not contiguous: %+v", i, ivs)
		}
	}
}
