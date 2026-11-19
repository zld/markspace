package pipeline_test

import (
	"sync"
	"testing"
	"time"

	"mediakey/internal/fixture"
	"mediakey/internal/manifest"
	"mediakey/internal/pipeline"
	"mediakey/internal/replay"
)

// TestConcurrentNonceRace hammers the replay cache with the same nonce.
// Exactly one request may win regardless of worker scheduling.
func TestConcurrentNonceRace(t *testing.T) {
	cache, err := replay.New(64, time.Minute)
	if err != nil {
		t.Fatalf("cache: %v", err)
	}
	const racers = 64
	var wg sync.WaitGroup
	var mu sync.Mutex
	accepted, replays := 0, 0
	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			// Identical nonce and sequence: every loser must be classified
			// as a replay rather than a sequence regression.
			err := cache.Check("shared-nonce", 1, time.Unix(fixture.BaseTime, 0))
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				accepted++
			} else if manifest.ErrorCode(err) == manifest.ReasonReplay {
				replays++
			} else {
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if accepted != 1 {
		t.Fatalf("accepted = %d, want exactly 1", accepted)
	}
	if replays != racers-1 {
		t.Fatalf("replays = %d, want %d", replays, racers-1)
	}
}

// TestConcurrentVerifyConsistency runs the whole pipeline with many workers
// and asserts the rotation count matches single-worker execution while every
// chunk still gets exactly one result.
func TestConcurrentVerifyConsistency(t *testing.T) {
	base, m := buildHealthy(t, 24, 64, 4)

	base.Workers = 1
	_, serialRotations, err := pipeline.Verify(base)
	if err != nil {
		t.Fatalf("serial verify: %v", err)
	}
	for run := 0; run < 5; run++ {
		cfg := base
		cfg.Workers = 8
		// Fresh nonce namespace per run so the cache starts logically clean.
		cfg.NonceFor = func(i int) string { return fixture.NonceFor("test-asset", i) + "-r" }
		results, rotations, err := pipeline.Verify(cfg)
		if err != nil {
			t.Fatalf("concurrent verify: %v", err)
		}
		if rotations != serialRotations {
			t.Fatalf("run %d rotations = %d, want %d", run, rotations, serialRotations)
		}
		if len(results) != len(m.Chunks) {
			t.Fatalf("run %d results = %d, want %d", run, len(results), len(m.Chunks))
		}
		for _, r := range results {
			if r.Status != manifest.StatusOK {
				t.Fatalf("run %d chunk %d failed: %s", run, r.ChunkIndex, r.FailureReason)
			}
		}
	}
}
