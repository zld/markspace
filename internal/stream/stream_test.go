package stream

import (
	"testing"

	"github.com/local/medialab/internal/container"
	"github.com/local/medialab/internal/fixture"
	"github.com/local/medialab/internal/model"
	"github.com/local/medialab/internal/timeline"
)

func analyzeClean(t *testing.T) (*container.MP4Meta, FrameStats, *model.ErrorBook, []byte) {
	t.Helper()
	data, _ := fixture.GenerateMP4(fixture.DefaultMP4Spec(7))
	meta, err := container.ParseMP4(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	book := &model.ErrorBook{}
	stats := AnalyzeFrames(meta.Samples, int64(len(data)), book)
	return meta, stats, book, data
}

func TestAnalyzeFramesClean(t *testing.T) {
	_, stats, book, _ := analyzeClean(t)
	if len(stats.Frames) != 90 {
		t.Fatalf("frames = %d, want 90", len(stats.Frames))
	}
	want := []uint32{1, 16, 31, 46, 61, 76}
	if len(stats.KeyframeNumbers) != len(want) {
		t.Fatalf("keyframes = %v, want %v", stats.KeyframeNumbers, want)
	}
	for i := range want {
		if stats.KeyframeNumbers[i] != want[i] {
			t.Fatalf("keyframe %d mismatch", i)
		}
	}
	// Every 15 frames at 30fps = 500ms gaps.
	if stats.KeyframeGapMs != 500 {
		t.Fatalf("max keyframe gap = %dms, want 500", stats.KeyframeGapMs)
	}
	if stats.IncompleteCount != 0 || book.Has(model.CodeIncompleteFrame) {
		t.Fatal("clean stream must have no incomplete frames")
	}
}

func TestIncompleteFrameNotAKeyframe(t *testing.T) {
	data, _ := fixture.GenerateMP4(fixture.DefaultMP4Spec(7))
	meta, err := container.ParseMP4(data)
	if err != nil {
		t.Fatalf("clean parse should succeed: %v", err)
	}
	// Cut enough bytes to reach back into the video chunk (180 audio
	// samples * 80 bytes), so multiple video frames become incomplete.
	shortSize := int64(len(data)) - 80*180 - 120
	book := &model.ErrorBook{}
	stats := AnalyzeFrames(meta.Samples, shortSize, book)
	if stats.IncompleteCount == 0 {
		t.Fatal("expected incomplete video frames with reduced file size")
	}
	for _, f := range stats.Frames {
		if !f.Complete && f.Keyframe {
			t.Fatalf("incomplete frame %d must not be a valid keyframe", f.Number)
		}
	}
	// Frame 76 is a keyframe; force its bytes past EOF and prove the
	// keyframe flag is suppressed even for an indexed keyframe.
	meta2, _ := container.ParseMP4(data)
	cut := meta2.Samples[75].Offset // frame 76 starts here; everything from here is gone
	book2 := &model.ErrorBook{}
	stats2 := AnalyzeFrames(meta2.Samples, cut, book2)
	var frame76 *Frame
	for i := range stats2.Frames {
		if stats2.Frames[i].Number == 76 {
			frame76 = &stats2.Frames[i]
		}
	}
	if frame76 == nil {
		t.Fatal("frame 76 missing from scan")
	}
	if frame76.Complete || frame76.Keyframe {
		t.Fatalf("frame 76 must be incomplete and not a valid keyframe: %+v", frame76)
	}
	if !book2.Has(model.CodeIncompleteFrame) {
		t.Fatal("incomplete frame not recorded in error book")
	}
}

func TestDetectDamageTruncationRanges(t *testing.T) {
	data, _ := fixture.GenerateMP4(fixture.DefaultMP4Spec(7))
	meta, err := container.ParseMP4(data)
	if err != nil {
		t.Fatal(err)
	}
	shortSize := int64(len(data)) - 80*180 - 120
	book := &model.ErrorBook{}
	stats := AnalyzeFrames(meta.Samples, shortSize, book)
	rep := DetectDamage(meta, shortSize, stats, book)
	if !rep.Truncated || !rep.IncompleteFrame {
		t.Fatalf("report = %+v, want truncated+incomplete", rep)
	}
	if len(rep.Ranges) == 0 {
		t.Fatal("damage ranges must be reported")
	}
	for _, w := range rep.Ranges {
		if w.End <= w.Start {
			t.Fatalf("non-positive damage window %v", w)
		}
	}
}

func TestDetectMissingIndex(t *testing.T) {
	clean, _ := fixture.GenerateMP4(fixture.DefaultMP4Spec(7))
	damaged := fixture.CorruptMP4(clean, fixture.CorruptMissingIndex)
	meta, err := container.ParseMP4(damaged)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	book := &model.ErrorBook{}
	stats := AnalyzeFrames(meta.Samples, int64(len(damaged)), book)
	rep := DetectDamage(meta, int64(len(damaged)), stats, book)
	if !rep.MissingIndex {
		t.Fatal("missing index not detected")
	}
	if !book.Has(model.CodeMissingIndex) {
		t.Fatal("missing index error not recorded")
	}
	if len(rep.Ranges) == 0 {
		t.Fatal("missing index must contribute a damage range")
	}
}

func TestAVSyncHealthy(t *testing.T) {
	meta, _, _, _ := analyzeClean(t)
	got := AVSync(meta)
	if got > 20 {
		t.Fatalf("av sync = %dms, want <= 20ms", got)
	}
}

func TestAVSyncDetectsOffset(t *testing.T) {
	data, _ := fixture.GenerateMP4(fixture.DefaultMP4Spec(7))
	meta, err := container.ParseMP4(data)
	if err != nil {
		t.Fatal(err)
	}
	// Shift every audio sample by 60ms to simulate lip-sync drift.
	for i := range meta.Samples {
		if meta.Samples[i].Track == 2 {
			meta.Samples[i].TimeTick += 3 * timeline.MasterHz / 50
		}
	}
	if got := AVSync(meta); got < 50 {
		t.Fatalf("shifted audio av sync = %dms, want >= 50ms", got)
	}
}
