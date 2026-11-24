package pipeline_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"medpipe/internal/pipeline"
	"medpipe/internal/sample"
)

func TestSlowConsumerBackpressureCheckpointRecoveryConsistency(t *testing.T) {
	cfg := sample.DefaultConfig()
	cfg.Seed = 29
	cfg.Packets = 320
	cfg.Loss = 0.03
	cfg.Reorder = 0.10
	cfg.Duplicate = 0.02
	cfg.CorruptOffset = 0
	gen, err := sample.Generate(cfg)
	if err != nil {
		t.Fatal(err)
	}

	fullDir := t.TempDir()
	if err := writeGeneratedSource(fullDir, gen.Data); err != nil {
		t.Fatal(err)
	}
	limit := int64(8192)
	opts := pipeline.Options{
		OutDir: fullDir, QueueBytes: limit, ConsumerDelay: time.Millisecond,
		CheckpointEvery: 75,
	}
	fullRunner := pipeline.NewRunner(opts, cfg, gen.Digest)
	full, err := fullRunner.Run(context.Background())
	if err != nil {
		t.Fatalf("full slow-consumer run: %v", err)
	}
	if full.QueuePeakBytes > limit {
		t.Fatalf("queue peak %d exceeds limit %d", full.QueuePeakBytes, limit)
	}
	if full.BackpressureWaits == 0 {
		t.Fatal("expected slow consumer to trigger backpressure")
	}
	if full.OutputSHA256 == "" || full.ManifestSHA256 == "" {
		t.Fatal("expected checksums from full run")
	}

	recoveryDir := t.TempDir()
	if err := writeGeneratedSource(recoveryDir, gen.Data); err != nil {
		t.Fatal(err)
	}
	interruptOpts := opts
	interruptOpts.OutDir = recoveryDir
	interruptOpts.InterruptAfterChunks = 130
	interruptRunner := pipeline.NewRunner(interruptOpts, cfg, gen.Digest)
	interrupted, err := interruptRunner.Run(context.Background())
	if !errors.Is(err, pipeline.ErrInterrupted) {
		t.Fatalf("interrupt err=%v result=%+v want ErrInterrupted", err, interrupted)
	}
	if peak := interruptRunner.Pipe().PeakBytes(); peak > limit {
		t.Fatalf("interrupted peak %d exceeds %d", peak, limit)
	}
	if used := interruptRunner.Pipe().UsedBytes(); used > limit {
		t.Fatalf("stopped consumer occupancy %d exceeds %d", used, limit)
	}

	resumeOpts := opts
	resumeOpts.OutDir = recoveryDir
	started := time.Now()
	resumed, err := pipeline.Resume(context.Background(), resumeOpts)
	if err != nil {
		t.Fatalf("resume after backpressure: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("resume took %v > 2s", elapsed)
	}
	if resumed.ResumeMS > 2000 {
		t.Fatalf("resume_ms=%d > 2000", resumed.ResumeMS)
	}
	if resumed.QueuePeakBytes > limit {
		t.Fatalf("resume queue peak %d exceeds %d", resumed.QueuePeakBytes, limit)
	}
	if resumed.ManifestSHA256 != full.ManifestSHA256 {
		t.Fatalf("manifest mismatch\nfull=%s\nresume=%s", full.ManifestSHA256, resumed.ManifestSHA256)
	}
	if resumed.OutputSHA256 != full.OutputSHA256 {
		t.Fatalf("output mismatch\nfull=%s\nresume=%s", full.OutputSHA256, resumed.OutputSHA256)
	}
	if len(resumed.Segments) != len(full.Segments) {
		t.Fatalf("segment count resumed=%d full=%d", len(resumed.Segments), len(full.Segments))
	}
	for i := range full.Segments {
		a, b := full.Segments[i], resumed.Segments[i]
		if a.Fingerprint != b.Fingerprint || a.DuplicateOf != b.DuplicateOf || a.SHA256 != b.SHA256 {
			t.Fatalf("segment %d state differs: full=%+v resumed=%+v", i, a, b)
		}
	}
}

func writeGeneratedSource(dir string, data []byte) error {
	return writeFile(dir, pipeline.SourceName, data)
}
