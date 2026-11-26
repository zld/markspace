package state_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"archivecheck/checkpoint"
	"archivecheck/fixture"
	"archivecheck/pipe"
	"archivecheck/state"
)

func writeFixture(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "sample")
	f := fixture.Build(name, 17)
	if _, _, _, err := f.Write(dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestRunNormalMetrics(t *testing.T) {
	dir := writeFixture(t, fixture.CaseNormal)
	res, err := state.Run(context.Background(), state.Config{
		WorkDir: dir, Queue: 2, MemoryBytes: 64 << 20, SlowMS: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	r := res.Report
	if r.Loss != 4 || r.Recovered != 4 || r.Duplicate != 2 {
		t.Fatalf("quality counts wrong: %+v", r)
	}
	if r.RecoverRatePct != 100 || r.SyncMS > 20 {
		t.Fatalf("rate/sync wrong: rate=%v sync=%v", r.RecoverRate, r.SyncMS)
	}
	if r.BadSegments != 0 || r.Fallback != 0 || r.Unresolved != 0 {
		t.Fatalf("unexpected media faults: %+v", r)
	}
	if r.ArchiveSHA256 == "" || r.P95MS > 1000 || r.InFlightPeakBytes > 64<<20 {
		t.Fatalf("report bounds failed: %+v", r)
	}
}

func TestRunCorruptIsolatesAndFallsBack(t *testing.T) {
	dir := writeFixture(t, fixture.CaseCorrupt)
	res, err := state.Run(context.Background(), state.Config{
		WorkDir: dir, Queue: 2, MemoryBytes: 64 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	r := res.Report
	if r.BadSegments != 2 || r.Fallback != 1 || r.Unresolved != 1 {
		t.Fatalf("corrupt handling wrong: %+v", r)
	}
	if res.CompleteThroughHLS != 0 {
		t.Fatalf("completion crossed unresolved HLS gap: through=%d", res.CompleteThroughHLS)
	}
}

func TestInterruptAfterCacheResumesToSameArchive(t *testing.T) {
	continuousDir := writeFixture(t, fixture.CaseRecovery)
	continuous, err := state.Run(context.Background(), state.Config{
		WorkDir: continuousDir, Queue: 2, MemoryBytes: 64 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}

	recoveryDir := writeFixture(t, fixture.CaseRecovery)
	_, err = state.Run(context.Background(), state.Config{
		WorkDir: recoveryDir, Queue: 2, MemoryBytes: 64 << 20, InterruptCursor: 1,
	})
	if !errors.Is(err, pipe.ErrHalt) {
		t.Fatalf("expected simulated halt, got %v", err)
	}
	resumed, err := state.Run(context.Background(), state.Config{
		WorkDir: recoveryDir, Queue: 2, MemoryBytes: 64 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Report.Resumed != 1 {
		t.Fatalf("resumed=%d, want 1", resumed.Report.Resumed)
	}
	if resumed.ArchiveSHA256 != continuous.ArchiveSHA256 {
		t.Fatalf("archive mismatch:\nresume=%s\ncont=%s", resumed.ArchiveSHA256, continuous.ArchiveSHA256)
	}
}

func TestReplayRejectsJournalLargerThanMemoryBudget(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "journal")
	j, err := checkpoint.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.AppendStage(context.Background(), checkpoint.StageRecord{
		Cursor: 1, InputDigest: "digest", Stage: checkpoint.StageCached,
		Source: "segment", Detail: "detail",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Replay(context.Background(), dir, pipe.NewBudget(1)); err == nil {
		t.Fatal("expected replay to respect the byte budget")
	}
}
