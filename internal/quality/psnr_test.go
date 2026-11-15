package quality

import (
	"math"
	"testing"

	"transcode/internal/media"
)

func TestFramePSNREdgeCases(t *testing.T) {
	// Identical, non-black -> +Inf, flagged identical.
	f, err := Frame(0, []byte{10, 20, 30}, []byte{10, 20, 30})
	if err != nil || !f.Identical || !math.IsInf(f.PSNRDB, 1) {
		t.Fatalf("identical frame handling wrong: %+v err=%v", f, err)
	}
	// Black/black -> NaN, flagged black pair.
	f, err = Frame(1, []byte{0, 0}, []byte{0, 0})
	if err != nil || !f.BlackPair || !math.IsNaN(f.PSNRDB) {
		t.Fatalf("black pair handling wrong: %+v err=%v", f, err)
	}
	// Black source vs non-black encoded: finite PSNR (MSE > 0).
	f, err = Frame(2, []byte{0, 0}, []byte{10, 10})
	if err != nil || math.IsNaN(f.PSNRDB) || math.IsInf(f.PSNRDB, 0) {
		t.Fatalf("black-vs-signal must be finite: %+v err=%v", f, err)
	}
	// Noisy pair: lower distortion must give higher PSNR.
	good, _ := Frame(3, []byte{100, 100}, []byte{99, 101})
	bad, _ := Frame(4, []byte{100, 100}, []byte{80, 120})
	if good.PSNRDB <= bad.PSNRDB {
		t.Fatalf("PSNR ordering wrong: good=%.2f bad=%.2f", good.PSNRDB, bad.PSNRDB)
	}
}

func TestSummarizeExcludesNonFinite(t *testing.T) {
	frames := []FramePSNR{
		{PSNRDB: math.NaN(), BlackPair: true},
		{PSNRDB: math.Inf(1), Identical: true},
		{PSNRDB: 30}, {PSNRDB: 40},
	}
	s := Summarize(frames)
	if s.FiniteCount != 2 || s.BlackPairs != 1 || s.IdenticalPairs != 1 {
		t.Fatalf("summary counts wrong: %+v", s)
	}
	if s.MeanDB != 35 || s.MinDB != 30 || s.MaxDB != 40 {
		t.Fatalf("summary aggregates wrong: %+v", s)
	}
	allEdge := Summarize(frames[:2])
	if !math.IsNaN(allEdge.MeanDB) {
		t.Fatal("all-edge summary must report NaN, not a fabricated mean")
	}
}

func TestClipRejectsMismatch(t *testing.T) {
	src := &media.Video{Width: 2, Height: 2, FPS: 1, Frames: [][]byte{{1, 2, 3, 4}}}
	wrongLen := &media.Video{Width: 2, Height: 2, FPS: 1, Frames: [][]byte{{1, 2}, {3, 4}}}
	if _, err := Clip(src, wrongLen); err == nil {
		t.Fatal("frame count mismatch must error")
	}
	wrongDim := &media.Video{Width: 4, Height: 1, FPS: 1, Frames: [][]byte{{1, 2, 3, 4}}}
	if _, err := Clip(src, wrongDim); err == nil {
		t.Fatal("dimension mismatch must error")
	}
}

func TestBuildReport(t *testing.T) {
	segs := []SegmentSummary{
		{MeanDB: 40, FrameCount: 3, FiniteCount: 3},
		{MeanDB: 30, FrameCount: 3, FiniteCount: 2, BlackPairs: 1},
		{MeanDB: 35, FrameCount: 3, FiniteCount: 3},
	}
	r := BuildReport(segs)
	if r.MeanPSNRDB != 35 || r.WorstSegment != 2 || r.WorstSegmentDB != 30 {
		t.Fatalf("aggregate wrong: %+v", r)
	}
	if len(r.SegmentPSNRDB) != 3 || len(r.SegmentDeltaDB) != 2 {
		t.Fatalf("array lengths wrong: %+v", r)
	}
	if math.Abs(r.SegmentDeltaDB[0]-10) > 1e-9 || math.Abs(r.SegmentDeltaDB[1]-5) > 1e-9 {
		t.Fatalf("deltas wrong: %v", r.SegmentDeltaDB)
	}
	rank := r.RankedWorst()
	if len(rank) != 3 || rank[0] != 2 || rank[1] != 3 || rank[2] != 1 {
		t.Fatalf("ranking wrong: %v", rank)
	}
}

func TestBuildReportWithNaNKeepsDeltaLength(t *testing.T) {
	r := BuildReport([]SegmentSummary{
		{MeanDB: 40, FiniteCount: 1},
		{MeanDB: math.NaN(), FiniteCount: 0},
		{MeanDB: 36, FiniteCount: 1},
	})
	if len(r.SegmentDeltaDB) != 2 ||
		!math.IsNaN(r.SegmentDeltaDB[0]) || !math.IsNaN(r.SegmentDeltaDB[1]) {
		t.Fatalf("NaN neighbors must yield NaN deltas with fixed length: %v", r.SegmentDeltaDB)
	}
	if r.WorstSegment != 3 {
		t.Fatalf("worst should ignore NaN segment: %+v", r)
	}
}
