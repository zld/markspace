package pipeline

import (
	"context"
	"fmt"
	"testing"
	"time"

	"rtpregress/internal/audio"
)

// runWithWorkers executes the same N jobs with a given worker count and
// returns the deterministic manifest hash plus ordered segment numbers.
func runWithWorkers(t *testing.T, workers, n int) (string, []int) {
	t.Helper()
	// jitter makes completion order differ across runs/worker counts
	fp := &fakeProcessor{delay: time.Microsecond * 100}
	pool, err := NewPool(workers, 64, fp)
	if err != nil {
		t.Fatal(err)
	}
	planner, err := audio.NewPlanner()
	if err != nil {
		t.Fatal(err)
	}
	jobs := make(chan Job, n)
	merger := NewMerger()
	// precompute placements on the deterministic timeline
	placements := map[int]audio.Placement{}
	for i := 0; i < n; i++ {
		seg := audio.Segment{ID: i, PID: uint16(0x100 + (i % 2)),
			StartNanos: int64(i) * 1_000_000}
		pl, perr := planner.Place(seg, 3)
		if perr != nil {
			t.Fatal(perr)
		}
		placements[i] = pl
		jobs <- Job{Segment: seg, PID: seg.PID, SegNumber: i}
	}
	close(jobs)
	if err := pool.Run(context.Background(), jobs, func(r PoolResult) {
		merger.Add(r, placements[r.Job.SegNumber])
	}); err != nil {
		t.Fatal(err)
	}
	ordered := merger.Sorted()
	segs := make([]int, len(ordered))
	for i, s := range ordered {
		segs[i] = s.SegNumber
	}
	return merger.ManifestHash(), segs
}

func TestWorkerCountDoesNotChangeManifest(t *testing.T) {
	const n = 24
	baseHash, baseOrder := runWithWorkers(t, 1, n)
	for _, w := range []int{2, 4, 8, 16} {
		hash, order := runWithWorkers(t, w, n)
		if hash != baseHash {
			t.Fatalf("worker count %d changed manifest hash:\n%s\n%s",
				w, hash, baseHash)
		}
		if fmt.Sprint(order) != fmt.Sprint(baseOrder) {
			t.Fatalf("worker count %d changed order: %v vs %v",
				w, order, baseOrder)
		}
	}
}
