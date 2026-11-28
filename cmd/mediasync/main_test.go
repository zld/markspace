package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"mediasync/internal/fixture"
	"mediasync/internal/pipeline"
	"mediasync/internal/report"
	"mediasync/internal/verifier"
)

// TestCanceledRunReturnsRuntimeExit mirrors the CLI verify path but cancels
// the context before the job runs, asserting no success report is produced.
func TestCanceledRunReturnsRuntimeExit(t *testing.T) {
	dir := t.TempDir()
	sc := fixture.BuildScenarios()[0]
	mp4Path := filepath.Join(dir, "ok.mp4")
	pktPath := filepath.Join(dir, "ok.bin")
	if err := os.WriteFile(mp4Path, sc.MP4, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pktPath, sc.Packets, 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel: nothing may run or succeed
	processor := func(ctx context.Context, job pipeline.Job) (any, error) {
		return verifier.VerifyPair(ctx, mp4Path, pktPath)
	}
	_, err := pipeline.Run(ctx, 2, 4, processor, []pipeline.Job{{Index: 0}})
	if err == nil {
		t.Fatal("a canceled run must return an error, not a result set")
	}
	// The CLI maps this onto ExitRuntime (1), never ExitOK or ExitInvalid.
	if code := mapErrToExit(err); code != report.ExitRuntime {
		t.Fatalf("want exit %d, got %d", report.ExitRuntime, code)
	}
}
