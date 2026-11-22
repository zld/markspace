package stream

import (
	"context"
	"testing"
	"time"

	"mediacore/internal/clock"
)

func TestSemaphoreBudgetAndPeak(t *testing.T) {
	sem := NewSemaphore(100)
	ctx := context.Background()
	if err := sem.Acquire(ctx, 60); err != nil {
		t.Fatal(err)
	}
	if err := sem.Acquire(ctx, 40); err != nil {
		t.Fatal(err)
	}
	if sem.Peak() != 100 {
		t.Fatalf("peak=%d", sem.Peak())
	}
	sem.Release(60)
	if err := sem.Acquire(ctx, 30); err != nil {
		t.Fatal(err)
	}
	if sem.Used() != 70 {
		t.Fatalf("used=%d", sem.Used())
	}
}

func TestSemaphoreOversizeRejected(t *testing.T) {
	sem := NewSemaphore(10)
	err := sem.Acquire(context.Background(), 11)
	if clock.Classify(err) != clock.KindInvalidArgument {
		t.Fatalf("want arg error, got %v", err)
	}
}

func TestSemaphoreCancellationUnblocks(t *testing.T) {
	sem := NewSemaphore(10)
	ctx, cancel := context.WithCancel(context.Background())
	if err := sem.Acquire(ctx, 10); err != nil {
		t.Fatal(err)
	}
	errCh := make(chan error, 1)
	go func() {
		errCh <- sem.Acquire(ctx, 5)
	}()
	// Give the goroutine time to block, then cancel.
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-errCh:
		if clock.Classify(err) != clock.KindCanceled {
			t.Fatalf("want canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("acquire did not unblock on cancel")
	}
}

func TestConfigValidation(t *testing.T) {
	if err := (Config{MaxBatchSamples: 1, MaxBatchBytes: 10, MaxInFlightBytes: 5}).Validate();
		clock.Classify(err) != clock.KindInvalidArgument {
		t.Fatalf("batch>inflight not rejected: %v", err)
	}
	if err := (Config{MaxBatchSamples: 1, MaxBatchBytes: 10, MaxInFlightBytes: 10}).Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}

func TestCheckpointRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/cp.json"
	cp, err := LoadCheckpoint(path, "digest")
	if err != nil {
		t.Fatal(err)
	}
	if cp.NextSample != 0 {
		t.Fatal("fresh checkpoint not zeroed")
	}
	cp.NextSample = 42
	cp.OutputDigest = "abc"
	if err := cp.Save(path); err != nil {
		t.Fatal(err)
	}
	back, err := LoadCheckpoint(path, "digest")
	if err != nil {
		t.Fatal(err)
	}
	if back.NextSample != 42 || back.OutputDigest != "abc" {
		t.Fatalf("round trip lost state: %+v", back)
	}
	if _, err := LoadCheckpoint(path, "different"); clock.Classify(err) != clock.KindRecoverable {
		t.Fatalf("stale checkpoint accepted: %v", err)
	}
}
