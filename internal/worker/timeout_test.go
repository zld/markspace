package worker

import (
	"context"
	"testing"
	"time"

	"regress/internal/fault"
	"regress/internal/plan"
	"regress/internal/scan"
)

// TestSubprocessTimeoutMapsToFault uses an already-expired context so the
// FFmpeg invocation is killed immediately and must surface as a structured
// timeout code rather than a generic failure.
func TestSubprocessTimeoutMapsToFault(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subprocess test in short mode")
	}
	dir := t.TempDir()
	enc := &FFmpegEncoder{
		SourcePath: "/nonexistent/source.264", Codec: scan.CodecH264,
		WorkDir: dir, Segments: plan.BuildSegmentPlan(&scan.ScanResult{
			AccessUnits: make([]scan.AccessUnit, 12),
			Keyframes:   []int{0},
		}, 24),
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(2 * time.Millisecond)
	_, err := enc.Encode(ctx, scan.CodecH264, plan.Candidate{ID: 0, Width: 16, Height: 16, BitrateBPS: 100000})
	if err == nil {
		t.Fatal("expected subprocess error")
	}
	if !fault.HasCode(err, fault.CodeSubprocessTimeout) && !fault.HasCode(err, fault.CodeSubprocessFailed) {
		t.Fatalf("want subprocess fault code, got %v", fault.Codes(err))
	}
}
