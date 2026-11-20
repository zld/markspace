package validate

import (
	"os"
	"path/filepath"
	"testing"

	"hlscheck/internal/fsutil"
	"hlscheck/internal/hls"
	"hlscheck/internal/model"
)

// buildTree writes a synthetic two-rendition index plus playlists and dummy
// segment files so the validator can run without any FFmpeg dependency.
func buildTree(t *testing.T) (root string, idx *hls.Index) {
	t.Helper()
	root = t.TempDir()
	idx = &hls.Index{TargetSeconds: 2, FrameRate: 25}
	for _, bps := range []int{350000, 800000} {
		dir := "rendition_" + itoa(bps)
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
		var segs []model.Segment
		for seq := 0; seq < 3; seq++ {
			name := "seg_" + itoa(seq) + ".ts"
			path, err := fsutil.SafeJoin(root, dir, name)
			if err != nil {
				t.Fatal(err)
			}
			// Pad each dummy segment to whole TS packets.
			data := make([]byte, model.PacketSize*(seq+1))
			if err := os.WriteFile(path, data, 0o644); err != nil {
				t.Fatal(err)
			}
			start := int64(seq) * 2 * model.ClockRate
			segs = append(segs, model.Segment{
				Seq: seq, File: name, StartPTS: start,
				EndPTS: start + 2*model.ClockRate, DurationMSField: 2000,
				Bytes: int64(len(data)),
			})
		}
		idx.Renditions = append(idx.Renditions, hls.IndexRendition{
			Rendition: model.Rendition{Bps: bps, Dir: dir},
			Segments:  segs, Playlist: dir + "/playlist.m3u8",
		})
	}
	if err := hls.WriteAll(root, idx); err != nil {
		t.Fatal(err)
	}
	return root, idx
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var b []byte
	for v > 0 {
		b = append([]byte{byte('0' + v%10)}, b...)
		v /= 10
	}
	return string(b)
}

func hasCode(findings []model.Finding, code string, bps, seq int) bool {
	for _, f := range findings {
		if f.Code == code && f.Bps == bps && f.Seq == seq {
			return true
		}
	}
	return false
}

func TestRunCleanTree(t *testing.T) {
	root, idx := buildTree(t)
	rep := Run(root, idx)
	if !rep.OK {
		t.Fatalf("clean tree reported failures: %+v", rep.Findings)
	}
}

func TestDropProducesMissingSegment(t *testing.T) {
	root, idx := buildTree(t)
	if _, err := ApplyDrops(root, idx, []DropRequest{{Bps: 800000, Seq: 1}}); err != nil {
		t.Fatalf("ApplyDrops: %v", err)
	}
	rep := Run(root, idx)
	if rep.OK {
		t.Fatal("expected failure after drop")
	}
	if !hasCode(rep.Findings, CodeMissingSegment, 800000, 1) {
		t.Fatalf("MISSING_SEGMENT not found: %+v", rep.Findings)
	}
}

func TestSwapProducesOrderMismatch(t *testing.T) {
	root, idx := buildTree(t)
	if _, err := ApplySwaps(root, idx, []SwapRequest{{Bps: 350000, A: 0, B: 2}}); err != nil {
		t.Fatalf("ApplySwaps: %v", err)
	}
	rep := Run(root, idx)
	if rep.OK {
		t.Fatal("expected failure after swap")
	}
	if !hasCode(rep.Findings, CodeOrderMismatch, 350000, 0) ||
		!hasCode(rep.Findings, CodeOrderMismatch, 350000, 2) {
		t.Fatalf("ORDER_MISMATCH not found: %+v", rep.Findings)
	}
}

func TestValidatorAggregatesMultipleFindings(t *testing.T) {
	root, idx := buildTree(t)
	if _, err := ApplyDrops(root, idx, []DropRequest{{Bps: 800000, Seq: 0}}); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplySwaps(root, idx, []SwapRequest{{Bps: 350000, A: 1, B: 2}}); err != nil {
		t.Fatal(err)
	}
	rep := Run(root, idx)
	codes := map[string]bool{}
	for _, f := range rep.Findings {
		codes[f.Code] = true
	}
	if !codes[CodeMissingSegment] || !codes[CodeOrderMismatch] {
		t.Fatalf("expected both code families, got %+v", codes)
	}
}

func TestCrossRenditionBoundaryMisalignment(t *testing.T) {
	root, idx := buildTree(t)
	// Shift the second rendition's second boundary beyond one frame.
	idx.Renditions[1].Segments[1].StartPTS += 10 * model.FrameTolerance
	rep := Run(root, idx)
	if !hasCode(rep.Findings, CodeBoundaryMisalign, 800000, 1) {
		t.Fatalf("boundary misalignment not detected: %+v", rep.Findings)
	}
}

func TestFaultRejectsUnknownRendition(t *testing.T) {
	root, idx := buildTree(t)
	if _, err := ApplyDrops(root, idx, []DropRequest{{Bps: 999, Seq: 0}}); err == nil {
		t.Fatal("expected error for unknown rendition")
	}
	if _, err := ApplySwaps(root, idx, []SwapRequest{{Bps: 999, A: 0, B: 1}}); err == nil {
		t.Fatal("expected error for unknown swap rendition")
	}
}
