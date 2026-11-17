package report

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"rateqa/internal/control"
	"rateqa/internal/jobs"
	"rateqa/internal/store"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(t.TempDir(), store.Parameters{
		SourceFingerprint: "fp", Width: 64, Height: 48, FPSNum: 25, FPSDen: 1,
		Segments: 2, TargetKbps: 600, FramesPerSegment: 25, TotalFrames: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func seedJob(t *testing.T, st *store.Store, seg, qp int, kbps, psnr float64) {
	t.Helper()
	id := fmt.Sprintf("seg%03d-qp%02d", seg, qp)
	if err := st.Enqueue(id, seg, qp); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.Claim(id); err != nil {
		t.Fatal(err)
	}
	if err := st.Complete(id, store.CandidateMetrics{
		Bytes: int64(kbps * 1000 / 8), BitrateKbps: kbps,
		PSNRMean: psnr, PSNRMin: psnr - 2, PSNRMax: psnr + 1, PSNRStd: 1.5, Frames: 25,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestBuildAndOutputs(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir, store.Parameters{
		SourceFingerprint: "fp", Width: 64, Height: 48, FPSNum: 25, FPSDen: 1,
		Segments: 2, TargetKbps: 600, FramesPerSegment: 25, TotalFrames: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	seedJob(t, st, 0, 14, 580, 36)
	seedJob(t, st, 1, 16, 640, 30)
	results := []jobs.SegmentResult{
		{Index: 0, Selected: control.Sample{QP: 14, BitrateKbps: 580, PSNRdB: 36}, Rounds: 2},
		{Index: 1, Selected: control.Sample{QP: 16, BitrateKbps: 640, PSNRdB: 30},
			Bounded: true, Reason: control.ReasonInversion, Rounds: 3},
	}
	rep := Build(st, results)
	if rep.Summary.Status != "complete" || rep.Summary.Segments != 2 {
		t.Fatalf("summary: %+v", rep.Summary)
	}
	if rep.Summary.SegmentPSNRMin > 30.1 || rep.Summary.SegmentPSNRMax < 35.9 {
		t.Fatalf("psnr range wrong: %+v", rep.Summary)
	}
	if rep.Segments[1].FallbackReason != control.ReasonInversion {
		t.Fatalf("fallback reason lost: %+v", rep.Segments[1])
	}
	var buf bytes.Buffer
	rep.PrintTerminal(&buf)
	out := buf.String()
	if !strings.Contains(out, "status=complete") || !strings.Contains(out, "segments=2") {
		t.Fatalf("terminal output missing fields:\n%s", out)
	}
	// JSONL: first line summary, then one line per segment, all valid JSON.
	p, err := rep.WriteJSONLFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("want 3 JSONL lines, got %d", len(lines))
	}
	var sm Summary
	if err := json.Unmarshal([]byte(lines[0]), &sm); err != nil || sm.Status != "complete" {
		t.Fatalf("summary JSONL: %v", err)
	}
}

func TestMarkIncomplete(t *testing.T) {
	st := openStore(t)
	defer st.Close()
	rep := Build(st, nil)
	rep.MarkIncomplete()
	if rep.Summary.Status != "incomplete" {
		t.Fatalf("status: %s", rep.Summary.Status)
	}
}

func TestRound2(t *testing.T) {
	if round2(1.236) != 1.24 || round2(1.0) != 1.0 {
		t.Fatal("round2 mismatch")
	}
}
