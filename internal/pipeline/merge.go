package pipeline

import (
	"context"
	"sort"

	"mediasync/internal/fault"
)

// Merge orders already-collected results by ascending input index, so output
// is independent of worker completion order. Missing indexes (jobs that were
// canceled and never delivered) are returned separately rather than guessed.
func Merge(results []Result) []Result {
	ordered := make([]Result, len(results))
	copy(ordered, results)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].Index < ordered[j].Index
	})
	return ordered
}

// Run submits every job, closes the pool and returns index-ordered results.
// On cancellation it stops accepting jobs, waits for in-flight workers to
// exit (via Close) and reports the cancellation instead of a success.
func Run(ctx context.Context, workers, queue int, processor Processor, jobs []Job) ([]Result, error) {
	pool := NewPool(ctx, workers, queue, processor)
	for _, job := range jobs {
		if err := pool.Submit(ctx, job); err != nil {
			pool.Close()
			return nil, err
		}
	}
	pool.Close()
	if err := ctx.Err(); err != nil {
		return nil, fault.New(fault.ErrCanceled, "", -1, "run canceled")
	}
	return Merge(pool.Collected()), nil
}

// P95LatencyNanos returns the 95th-percentile worker latency using the
// nearest-rank method. Zero jobs returns 0.
func P95LatencyNanos(results []Result) int64 {
	if len(results) == 0 {
		return 0
	}
	latencies := make([]int64, len(results))
	for i, r := range results {
		latencies[i] = r.Latency
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	// Nearest-rank: ceil(0.95 * N), converted to a zero-based index.
	rank := (95*len(latencies) + 99) / 100
	if rank < 1 {
		rank = 1
	}
	return latencies[rank-1]
}
