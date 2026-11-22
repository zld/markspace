package qcrun

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"mediaqc/internal/samples"
)

func setup(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := samples.WriteAll(dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestRunNormalEndToEnd(t *testing.T) {
	dir := setup(t)
	out := filepath.Join(dir, "out.mp4")
	sum, code := Run(context.Background(), Options{
		Input:    filepath.Join(dir, "normal.webm"),
		Output:   out,
		QueueCap: 4,
	})
	if code != 0 {
		t.Fatalf("exit %d: %+v", code, sum)
	}
	if sum.IndexEntries < 1 || sum.InputSHA256 == "" || sum.OutputSHA256 == "" {
		t.Fatalf("summary incomplete: %+v", sum)
	}
	if sum.SyncErrorMs > 2.0 {
		t.Fatalf("sync error %.2fms exceeds 2ms", sum.SyncErrorMs)
	}
	if sum.PacketLossRecoveryRate < 0.90 {
		t.Fatalf("recovery rate %.3f below 0.90", sum.PacketLossRecoveryRate)
	}
	if sum.PeakBufferBytes > 8<<20 {
		t.Fatalf("peak buffer %d exceeds 8MiB", sum.PeakBufferBytes)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("output missing: %v", err)
	}
	// State file is cleaned up after a successful run.
	if _, err := os.Stat(out + ".qcstate"); !os.IsNotExist(err) {
		t.Fatalf("state file remains after success")
	}
}

func TestRunTruncatedExit2(t *testing.T) {
	dir := setup(t)
	sum, code := Run(context.Background(), Options{
		Input:  filepath.Join(dir, "corrupt-trunc-unknown.webm"),
		Output: filepath.Join(dir, "bad.mp4"),
	})
	if code != 2 || sum.ErrorClass != "truncated" || sum.Offset <= 0 {
		t.Fatalf("exit %d: %+v", code, sum)
	}
}

func TestRunRepeatConsistency(t *testing.T) {
	dir := setup(t)
	var sums [2]struct{ index, outSHA string }
	for i := 0; i < 2; i++ {
		sum, code := Run(context.Background(), Options{
			Input:  filepath.Join(dir, "normal.webm"),
			Output: filepath.Join(dir, "repeat.mp4"),
		})
		if code != 0 {
			t.Fatalf("run %d exit %d", i, code)
		}
		sums[i].index = sum.IndexChecksum
		sums[i].outSHA = sum.OutputSHA256
	}
	if sums[0] != sums[1] {
		t.Fatalf("repeat runs diverged: %+v", sums)
	}
}

func TestRunInterruptResume(t *testing.T) {
	dir := setup(t)
	out := filepath.Join(dir, "resumable.mp4")
	sum, code := Run(context.Background(), Options{
		Input:       filepath.Join(dir, "normal.webm"),
		Output:      out,
		CancelAfter: 3,
		Resume:      true,
	})
	if code != 3 || sum.ErrorClass != "interrupted" {
		t.Fatalf("interrupt: exit %d: %+v", code, sum)
	}
	if _, err := os.Stat(out + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("half-finished temp left behind")
	}
	sum, code = Run(context.Background(), Options{
		Input:  filepath.Join(dir, "normal.webm"),
		Output: out,
		Resume: true,
	})
	if code != 0 || sum.ResumeAction != "resumed" {
		t.Fatalf("resume: exit %d: %+v", code, sum)
	}
}
