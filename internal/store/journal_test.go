package store

import (
	"errors"
	"os"
	"testing"
)

func testParams() Parameters {
	return Parameters{SourceFingerprint: "abc123", Width: 64, Height: 48,
		FPSNum: 25, FPSDen: 1, Segments: 2, TargetKbps: 600,
		FramesPerSegment: 10, TotalFrames: 20}
}

func TestLifecycleAndIdempotentEnqueue(t *testing.T) {
	st, err := Open(t.TempDir(), testParams())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Enqueue("s0-q8", 0, 8); err != nil {
		t.Fatal(err)
	}
	// Duplicate enqueue must not create a second claimable job.
	if err := st.Enqueue("s0-q8", 0, 8); err != nil {
		t.Fatal(err)
	}
	j, ok, err := st.Claim("s0-q8")
	if err != nil || !ok || j.State != StateRunning || j.Attempts != 1 {
		t.Fatalf("claim: ok=%v job=%+v err=%v", ok, j, err)
	}
	// A running job cannot be claimed again (no double pickup).
	if _, ok, _ := st.Claim("s0-q8"); ok {
		t.Fatal("running job claimed twice")
	}
	terminal, err := st.Retry("s0-q8", errors.New("ffmpeg boom"))
	if err != nil || terminal {
		t.Fatalf("first retry should be non-terminal: terminal=%v err=%v", terminal, err)
	}
	if got := st.Snapshot()["s0-q8"].State; got != StateRetryable {
		t.Fatalf("state: %s", got)
	}
	if _, ok, _ = st.Claim("s0-q8"); !ok {
		t.Fatal("retryable job should be claimable again")
	}
	terminal, err = st.Retry("s0-q8", errors.New("again"))
	if err != nil || !terminal {
		t.Fatalf("second retry should terminate: terminal=%v err=%v", terminal, err)
	}
	if got := st.Snapshot()["s0-q8"].State; got != StateFailed {
		t.Fatalf("state: %s", got)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryRequeuesUnfinished(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir, testParams())
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Enqueue("s0-q8", 0, 8)
	_ = st.Enqueue("s0-q16", 0, 16)
	_ = st.Enqueue("s1-q8", 1, 8)
	if _, _, err := st.Claim("s0-q8"); err != nil {
		t.Fatal(err)
	}
	if err := st.Complete("s0-q16", CandidateMetrics{Bytes: 100, Frames: 10}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	// Simulate crash: process gone without clean shutdown. Reopen.
	st2, err := Open(dir, testParams())
	if err != nil {
		t.Fatal(err)
	}
	snap := st2.Snapshot()
	if snap["s0-q8"].State != StateQueued {
		t.Errorf("running job not requeued: %s", snap["s0-q8"].State)
	}
	if snap["s1-q8"].State != StateQueued {
		t.Errorf("queued job lost: %s", snap["s1-q8"].State)
	}
	if snap["s0-q16"].State != StateCompleted {
		t.Errorf("completed job changed: %s", snap["s0-q16"].State)
	}
	if snap["s0-q16"].Metrics.Bytes != 100 {
		t.Errorf("metrics lost: %+v", snap["s0-q16"].Metrics)
	}
}

func TestFingerprintMismatch(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir, testParams())
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Enqueue("s0-q8", 0, 8)
	_ = st.Close()

	other := testParams()
	other.TargetKbps = 999
	if _, err := Open(dir, other); !errors.Is(err, ErrFingerprintMismatch) {
		t.Fatalf("want mismatch error, got %v", err)
	}
	other = testParams()
	other.SourceFingerprint = "different"
	if _, err := Open(dir, other); !errors.Is(err, ErrFingerprintMismatch) {
		t.Fatalf("want fingerprint error, got %v", err)
	}
}

func TestCheckpointFilePresent(t *testing.T) {
	dir := t.TempDir()
	st, _ := Open(dir, testParams())
	_ = st.Enqueue("s0-q8", 0, 8)
	_ = st.Close()
	if _, err := os.ReadFile(dir + "/" + cpName); err != nil {
		t.Fatalf("checkpoint missing/unreadable: %v", err)
	}
}
