package app

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/zld/media-check/media"
	"github.com/zld/media-check/pipeline"
)

// runCancelDrill drives a pool with exactly four jobs that block until the
// context is cancelled, then checks that every job is accounted as cancelled
// and that the result stream closes only after all of them are reported.
func runCancelDrill(outDir string) (int, error) {
	const jobs = 4
	dir := filepath.Join(outDir, "cancel-drill")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, media.WrapError(media.ClassIO, "cancel.mkdir", 0, err, "create drill dir")
	}
	if _, err := media.ValidateOutPath(outDir, dir); err != nil {
		return 0, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{}, jobs)
	pool, err := pipeline.NewPool(ctx, jobs, 1, func(jobCtx context.Context, j pipeline.Job) pipeline.Result {
		entered <- struct{}{}
		<-jobCtx.Done() // hold the task until cancellation
		return pipeline.Result{Index: j.Index, TrackID: j.TrackID, Segment: j.Segment}
	})
	if err != nil {
		return 0, err
	}
	done := make(chan struct{})
	counts := map[pipeline.Status]int{}
	go func() {
		defer close(done)
		for r := range pool.Results() {
			counts[r.Status]++
		}
	}()
	// One worker per job means every submission is picked up immediately and
	// blocks in the handler, so cancellation happens with all four in flight.
	for i := 0; i < jobs; i++ {
		if err := pool.Submit(pipeline.Job{Index: i, TrackID: 1}); err != nil {
			pool.Close()
			<-done
			return 0, err
		}
	}
	for i := 0; i < jobs; i++ {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			return 0, media.NewError(media.ClassIntegrity, "cancel.drill", 0, "workers never started")
		}
	}
	cancel()
	pool.Close()
	<-done
	if counts[pipeline.StatusCancelled] != jobs {
		return 0, media.NewError(media.ClassIntegrity, "cancel.drill", 0,
			"expected %d cancelled results, got %+v", jobs, counts)
	}
	return jobs, nil
}
