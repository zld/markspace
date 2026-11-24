package demo

import (
	"os"
	"path/filepath"
	"testing"

	"medpipe/internal/fault"
)

func allFaults() []string {
	return []string{"duplicate", "reorder", "burst-loss", "cc-jump", "psi-version", "truncate"}
}

func TestAcceptanceDemoContract(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "demo")
	rep := Run(Config{OutDir: dir, Seed: 17, Faults: allFaults(), Resume: true, LatencyRuns: 21})

	if rep.ExitCode != fault.ExitCorrupt {
		t.Fatalf("exit=%d want 2; report=%+v", rep.ExitCode, rep)
	}
	if rep.SyncErrorMS < 0 || rep.SyncErrorMS > 20 {
		t.Fatalf("sync_error_ms=%v outside [0,20]", rep.SyncErrorMS)
	}
	if rep.LossRecoveryRate < 0.95 || rep.LossRecoveryRate > 1 {
		t.Fatalf("recovery rate=%v", rep.LossRecoveryRate)
	}
	if rep.LatencySamples == 0 || rep.LatencySamples != 21 {
		t.Fatalf("latency samples=%d", rep.LatencySamples)
	}
	if rep.P95LatencyMS <= 0 {
		t.Fatalf("p95=%v with %d samples", rep.P95LatencyMS, rep.LatencySamples)
	}
	if rep.DroppedDuplicates == 0 {
		t.Fatal("dropped_duplicates must be positive")
	}
	if rep.GapEvents == 0 {
		t.Fatal("gap_events must not be zero")
	}
	if rep.PSIVersion != 1 {
		t.Fatalf("psi_version=%d want 1", rep.PSIVersion)
	}
	if len(rep.OutputSHA256) != 64 {
		t.Fatalf("output_sha256=%q", rep.OutputSHA256)
	}
	if rep.RollbackCount < 1 {
		t.Fatalf("rollback_count=%d", rep.RollbackCount)
	}
	if rep.Normal == nil || rep.Normal.ExitCode != 0 {
		t.Fatalf("normal sample bad: %+v", rep.Normal)
	}
	if rep.Resume == nil || rep.Resume.PreResumeSHA256 != rep.OutputSHA256 {
		t.Fatalf("resume pre/post digest mismatch: %+v", rep.Resume)
	}
	if rep.Corrupt == nil || rep.Corrupt.ErrorCode != fault.ErrTruncated {
		t.Fatalf("corrupt sample=%+v", rep.Corrupt)
	}
	if rep.Corrupt.GapEvents == 0 || rep.Corrupt.RTPStats == nil || rep.Corrupt.RTPStats.Duplicates == 0 {
		t.Fatalf("corrupt chain did not carry RTP faults and TS gaps: %+v", rep.Corrupt)
	}
	if _, err := os.Stat(filepath.Join(dir, "corrupt", "corrupt.mp4")); err == nil {
		t.Fatal("corrupt MP4 must not be committed")
	}
	if _, err := os.Stat(filepath.Join(dir, "corrupt", "corrupt.mp4.commit.json")); err == nil {
		t.Fatal("corrupt commit marker must not exist")
	}
}

func TestDemoOutputHashRepeats(t *testing.T) {
	dir1 := filepath.Join(t.TempDir(), "a")
	dir2 := filepath.Join(t.TempDir(), "b")
	r1 := Run(Config{OutDir: dir1, Seed: 17, Faults: allFaults(), Resume: true, LatencyRuns: 5})
	r2 := Run(Config{OutDir: dir2, Seed: 17, Faults: allFaults(), Resume: true, LatencyRuns: 5})
	if r1.OutputSHA256 == "" || r1.OutputSHA256 != r2.OutputSHA256 {
		t.Fatalf("hashes differ: %s vs %s", r1.OutputSHA256, r2.OutputSHA256)
	}
	if r1.ExitCode != r2.ExitCode {
		t.Fatalf("exit codes %d vs %d", r1.ExitCode, r2.ExitCode)
	}
}
