package testkit

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestCheckpointResume proves a run can restart from a retained checkpoint:
// a first bounded pass stops partway, then a second pass resumes and reaches
// the same sample count as a clean run.
func TestCheckpointResume(t *testing.T) {
	root := t.TempDir()
	fs := generateNormal(t, root)

	// Pass 1: a small batch budget forces many batches. Cancel only after
	// several batches have been consumed, guaranteeing a persisted checkpoint.
	pcfg := basePipeline()
	pcfg.MaxBatchBytes = 64 << 10
	pcfg.InFlightBytes = 256 << 10
	pcfg.SlowEvery = 1
	ctx, cancel := context.WithCancel(context.Background())
	batches := make(chan struct{}, 16)
	sleep := func() {
		select {
		case batches <- struct{}{}:
		default:
		}
	}
	go func() {
		// Wait for at least three batch stalls, then cancel.
		for i := 0; i < 3; i++ {
			<-batches
		}
		cancel()
	}()
	_, _ = RunPipeline(ctx, fs, pcfg, "", sleep)
	cancel() // ensure release even if the goroutine is still waiting
	cp1, err := os.ReadFile(fs.Checkpoint)
	if err != nil {
		t.Fatalf("checkpoint not retained: %v", err)
	}
	if len(cp1) == 0 {
		t.Fatal("empty checkpoint")
	}

	// Pass 2: resume with the standard budget and no cancellation.
	res, err := RunPipeline(context.Background(), fs, basePipeline(), "", nil)
	if err != nil {
		t.Fatalf("resume failed: %v", err)
	}
	if res.Records != demoSamples {
		t.Fatalf("resumed records=%d want %d", res.Records, demoSamples)
	}
	info, err := os.Stat(fs.Output)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != int64(demoSamples)*physicalPayload {
		t.Fatalf("resumed output size=%d", info.Size())
	}
}

// TestResumeDigestMatchesCleanRun verifies a resumed run converges to the
// same canonical output digest as a fully clean run on a fresh fixture copy.
func TestResumeDigestMatchesCleanRun(t *testing.T) {
	rootA, rootB := t.TempDir(), t.TempDir()
	fsResume := generateNormal(t, rootA)
	fsClean := generateNormal(t, rootB)

	// Interrupt the first fixture early.
	pcfg := basePipeline()
	pcfg.MaxBatchBytes = 64 << 10
	pcfg.InFlightBytes = 256 << 10
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()
	_, _ = RunPipeline(ctx, fsResume, pcfg, "", nil)

	resumed, err := RunPipeline(context.Background(), fsResume, basePipeline(), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	clean, err := RunPipeline(context.Background(), fsClean, basePipeline(), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.OutputDigest != clean.OutputDigest {
		t.Fatalf("digest mismatch resumed=%s clean=%s", resumed.OutputDigest, clean.OutputDigest)
	}
	if resumed.RepairDigest != clean.RepairDigest {
		t.Fatalf("repair digest mismatch")
	}
}
