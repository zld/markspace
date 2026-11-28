package mediakernel

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func testConfig() PipelineConfig {
	return PipelineConfig{
		QueueCap:            4,
		MaxBlockSize:        1 << 20,
		SegmentDurNs:        200_000_000,
		Silence:             SilenceConfig{Threshold: 500, MinDuration: 100_000_000, MaxGap: 0},
		FingerprintWindowNs: 1_000_000_000,
	}
}

func writeFixture(t *testing.T, dir string, data []byte) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "input.webm")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPipelineInterleaved(t *testing.T) {
	dir := t.TempDir()
	in := writeFixture(t, dir, BuildInterleaved(7).Data)
	man, err := NewPipeline(testConfig(), dir).Run(context.Background(), in, false)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(man.Segments) != 4 {
		t.Fatalf("segments = %d, want 4", len(man.Segments))
	}
	if len(man.Silence) != 2 {
		t.Fatalf("silence intervals = %d, want 2", len(man.Silence))
	}
	if man.Duplicates < 4 {
		t.Fatalf("duplicates = %d, want >= 4", man.Duplicates)
	}
	for _, f := range []string{"manifest.json", "checkpoint.json", "summary.mp4"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Fatalf("missing output %s: %v", f, err)
		}
	}
	// Segment ranges are half-open and non-overlapping.
	for i := 1; i < len(man.Segments); i++ {
		if man.Segments[i].Segment.Start < man.Segments[i-1].Segment.End {
			t.Fatalf("segments overlap: %+v vs %+v", man.Segments[i-1], man.Segments[i])
		}
	}
}

func TestPipelineInterruptResume(t *testing.T) {
	fix := BuildInterleaved(7)
	cleanDir := t.TempDir()
	cleanIn := writeFixture(t, cleanDir, fix.Data)
	manClean, err := NewPipeline(testConfig(), cleanDir).Run(context.Background(), cleanIn, false)
	if err != nil {
		t.Fatalf("clean run: %v", err)
	}

	runDir := t.TempDir()
	runIn := writeFixture(t, runDir, fix.Data)
	cfg := testConfig()
	cfg.FailAfterSegments = 2
	_, err = NewPipeline(cfg, runDir).Run(context.Background(), runIn, false)
	mkErr, ok := err.(*Error)
	if !ok || mkErr.Code != ErrInterrupted {
		t.Fatalf("expected interrupt, got %v", err)
	}
	cfg.FailAfterSegments = 0
	manResumed, err := NewPipeline(cfg, runDir).Run(context.Background(), runIn, true)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if len(manResumed.Segments) != len(manClean.Segments) {
		t.Fatalf("segments differ: resumed %d, clean %d",
			len(manResumed.Segments), len(manClean.Segments))
	}
	if manResumed.OutputSHA256 != manClean.OutputSHA256 {
		t.Fatalf("output checksum differs after resume:\n%s\n%s",
			manResumed.OutputSHA256, manClean.OutputSHA256)
	}
}

func TestPipelineInputChangedRefusesResume(t *testing.T) {
	dir := t.TempDir()
	in := writeFixture(t, dir, BuildInterleaved(7).Data)
	if _, err := NewPipeline(testConfig(), dir).Run(context.Background(), in, false); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if err := os.WriteFile(in, BuildInterleaved(8).Data, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := NewPipeline(testConfig(), dir).Run(context.Background(), in, true)
	mkErr, ok := err.(*Error)
	if !ok || mkErr.Code != ErrInputChanged {
		t.Fatalf("err = %v, want %s", err, ErrInputChanged)
	}
}

func TestPipelineCorruptInput(t *testing.T) {
	dir := t.TempDir()
	fix := BuildTruncatedVINT(7)
	in := writeFixture(t, dir, fix.Data)
	_, err := NewPipeline(testConfig(), dir).Run(context.Background(), in, false)
	mkErr, ok := err.(*Error)
	if !ok || mkErr.Code != ErrTruncatedHeader {
		t.Fatalf("err = %v, want %s", err, ErrTruncatedHeader)
	}
}

func TestPipelineCanceledContext(t *testing.T) {
	dir := t.TempDir()
	in := writeFixture(t, dir, BuildInterleaved(7).Data)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewPipeline(testConfig(), dir).Run(ctx, in, false)
	if err == nil {
		t.Fatalf("canceled context must stop the pipeline")
	}
}
