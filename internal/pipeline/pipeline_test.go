package pipeline

import (
	"path/filepath"
	"testing"
	"time"
)

func TestHealthyStreamMeetsAcceptanceThresholds(t *testing.T) {
	res, err := Run(Options{
		Seed: 7, Duration: 30 * time.Second, RetryLimit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	s := res.Summary
	if s.ExitCode != 0 {
		t.Fatalf("healthy run exit=%d blocked=%s", s.ExitCode, s.BlockedDependency)
	}
	if s.SyncErrorMs > 10 {
		t.Errorf("sync_error_ms=%f > 10", s.SyncErrorMs)
	}
	if s.PacketRecoveryRate < 0.9 {
		t.Errorf("recovery rate=%f < 0.9", s.PacketRecoveryRate)
	}
	if s.P95LatencyMs > 120 {
		t.Errorf("p95=%f > 120", s.P95LatencyMs)
	}
	if s.IndexEntries != s.ValidIntervals || s.IndexEntries == 0 {
		t.Errorf("index entries=%d intervals=%d", s.IndexEntries, s.ValidIntervals)
	}
	if len(s.OutputSHA256) != 64 {
		t.Errorf("output digest malformed: %q", s.OutputSHA256)
	}
}

// TestLossBurstPropagatesAcrossSubsystems covers fault class 1: a concealable
// burst must travel through clock repair, RTP recovery and index build, end
// to end, with the index still consistent and the run recoverable.
func TestLossBurstPropagatesAcrossSubsystems(t *testing.T) {
	res, err := Run(Options{
		Seed: 7, Duration: 30 * time.Second, LossBurst: 4, Reorder: 3,
		CorruptIndex: 2, RetryLimit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	s := res.Summary
	if s.ExitCode != 0 {
		t.Fatalf("concealable burst must be recoverable, exit=%d blocked=%s",
			s.ExitCode, s.BlockedDependency)
	}
	if res.Recover.Stats.Concealed != 4 {
		t.Errorf("expected 4 concealed packets, got %d", res.Recover.Stats.Concealed)
	}
	if s.PacketRecoveryRate < 0.9 {
		t.Errorf("recovery rate=%f", s.PacketRecoveryRate)
	}
	if s.DroppedIndexRecords != 2 {
		t.Errorf("expected 2 dropped corrupt records, got %d", s.DroppedIndexRecords)
	}
	if s.IndexEntries != s.ValidIntervals {
		t.Errorf("index/interval mismatch: %d vs %d", s.IndexEntries, s.ValidIntervals)
	}
	// MP4 round-trip invariant: every index offset stays inside the input.
	for _, rec := range res.Index.Records {
		if rec.ByteOffset+rec.Length > res.Index.InputLength {
			t.Fatalf("record escapes input: %+v", rec)
		}
	}
}

// TestLargeBurstBlocksTimeline covers fault class 2: a burst beyond the
// concealment limit blocks recovery and propagates through the DAG as a
// blocking dependency with exit code 2.
func TestLargeBurstBlocksTimeline(t *testing.T) {
	res, err := Run(Options{
		Seed: 11, Duration: 5 * time.Second, LossBurst: 12, Reorder: 8,
		TruncateAudio: 17, CorruptIndex: 5, RetryLimit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	s := res.Summary
	if s.ExitCode != 2 {
		t.Errorf("unrecoverable run exit=%d want 2", s.ExitCode)
	}
	if s.BlockedDependency == "" {
		t.Error("expected a named blocked dependency")
	}
	if s.TruncatedBytes != 17 {
		t.Errorf("truncated bytes=%d want 17", s.TruncatedBytes)
	}
}

func TestTruncationReportsMissingBytes(t *testing.T) {
	res, err := Run(Options{
		Seed: 3, Duration: 5 * time.Second, TruncateAudio: 33, RetryLimit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary.TruncatedBytes <= 0 || res.Parse == nil || !res.Parse.Truncated {
		t.Fatalf("truncation not surfaced: %+v", res.Summary)
	}
	if res.Summary.ExitCode != 0 {
		t.Fatal("partial WAV with surviving frames must still succeed")
	}
}

func TestDeterministicOutputAcrossRuns(t *testing.T) {
	opt := Options{Seed: 7, Duration: 5 * time.Second, LossBurst: 4,
		Reorder: 2, CorruptIndex: 3, RetryLimit: 2}
	a, err := Run(opt)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Run(opt)
	if err != nil {
		t.Fatal(err)
	}
	if a.Summary.OutputSHA256 != b.Summary.OutputSHA256 {
		t.Fatal("output digest differs across identical runs")
	}
	if a.Summary != b.Summary {
		t.Fatalf("summary differs:\n%+v\n%+v", a.Summary, b.Summary)
	}
}

func TestJournalCheckpointReuse(t *testing.T) {
	dir := t.TempDir()
	journalPath := filepath.Join(dir, "run.journal")
	opt := Options{Seed: 5, Duration: 3 * time.Second, RetryLimit: 1,
		JournalPath: journalPath}
	first, err := Run(opt)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Run(opt)
	if err != nil {
		t.Fatal(err)
	}
	if second.Journal == nil || second.Journal.Size() == 0 {
		t.Fatal("journal did not persist checkpoints")
	}
	if first.Summary.OutputSHA256 != second.Summary.OutputSHA256 {
		t.Fatal("checkpoint reuse changed the output")
	}
}

func TestDependencyFailureExhaustsRetries(t *testing.T) {
	res, err := Run(Options{
		Seed: 1, Duration: 3 * time.Second, RetryLimit: 1,
		FailNode: "parse", ForceFail: 5, // always fails, beyond retry limit
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary.ExitCode != 2 || res.Summary.BlockedDependency != "parse" {
		t.Fatalf("expected blocked parse with exit 2, got %+v", res.Summary)
	}
}

func TestInvalidArgumentExitPath(t *testing.T) {
	if _, err := Run(Options{Seed: 1, Duration: -1 * time.Second}); err == nil {
		t.Fatal("negative duration must be rejected before scheduling")
	}
}
