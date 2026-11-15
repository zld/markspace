// Package scheduler drives segment encoding with a bounded worker pool.
// It owns the running-state transitions and guarantees that a segment is
// submitted at most once per run: succeeded (and not invalidated) segments
// are skipped entirely, while queued/interrupted/failed ones are eligible.
package scheduler

import (
	"context"
	"fmt"
	"sync"

	"transcode/internal/model"
)

// Job is one unit of segment work handed to the worker function.
type Job struct {
	Segment *model.Segment
}

// WorkerFunc executes one segment. It must apply the terminal transition
// (succeeded or failed) on the segment itself; the scheduler only owns the
// queued -> running entry and interruption handling.
type WorkerFunc func(ctx context.Context, job Job) error

// OnSegmentProgress is invoked after every terminal transition while the
// checkpoint mutex is held, so the caller can persist safely.
type OnSegmentProgress func(seg *model.Segment)

// Queue schedules the segments of one task.
type Queue struct {
	workers  int
	work     WorkerFunc
	onProgress OnSegmentProgress
	mu       sync.Mutex
}

// New builds a queue. workers must be at least 1.
func New(workers int, work WorkerFunc, onProgress OnSegmentProgress) (*Queue, error) {
	if workers < 1 {
		return nil, fmt.Errorf("workers must be >= 1, got %d", workers)
	}
	if work == nil {
		return nil, fmt.Errorf("worker function is required")
	}
	return &Queue{workers: workers, work: work, onProgress: onProgress}, nil
}

// Run processes every eligible segment and returns counts when the pool
// drains. Cancellation marks in-flight segments interrupted; already
// succeeded segments are never touched or resubmitted.
func (q *Queue) Run(ctx context.Context, task *model.Task) (succeeded, failed, interrupted int) {
	jobs := make(chan Job)
	var wg sync.WaitGroup
	for w := 0; w < q.workers; w++ {
		wg.Add(1)
		go q.worker(ctx, jobs, &wg)
	}

	for _, seg := range task.Segments {
		if !q.claim(seg) {
			continue // succeeded/infeasible/already claimed: do not resubmit
		}
		select {
		case jobs <- Job{Segment: seg}:
		case <-ctx.Done():
			q.releaseOnCancel(seg)
		}
	}
	close(jobs)
	wg.Wait()

	for _, seg := range task.Segments {
		switch seg.Status {
		case model.StatusSucceeded:
			succeeded++
		case model.StatusFailed:
			failed++
		case model.StatusInterrupted:
			interrupted++
		}
	}
	return succeeded, failed, interrupted
}

// claim moves an eligible segment into running under the lock.
func (q *Queue) claim(seg *model.Segment) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	switch seg.Status {
	case model.StatusQueued:
		if err := seg.Status.Transition(model.StatusRunning); err != nil {
			return false
		}
		return true
	case model.StatusInterrupted, model.StatusFailed:
		// Re-enter via the validated reset path, then start running.
		if err := seg.ResetToQueued(); err != nil {
			return false
		}
		if err := seg.Status.Transition(model.StatusRunning); err != nil {
			return false
		}
		return true
	default:
		return false
	}
}

func (q *Queue) releaseOnCancel(seg *model.Segment) {
	q.mu.Lock()
	defer q.mu.Unlock()
	seg.MarkInterrupted()
}

func (q *Queue) worker(ctx context.Context, jobs <-chan Job, wg *sync.WaitGroup) {
	defer wg.Done()
	for job := range jobs {
		seg := job.Segment
		err := q.work(ctx, job)
		q.mu.Lock()
		switch {
		case ctx.Err() != nil:
			seg.MarkInterrupted()
		case err != nil:
			if seg.Status == model.StatusRunning {
				_ = seg.Status.Transition(model.StatusFailed)
			}
			if seg.FailureNote == "" {
				seg.FailureNote = err.Error()
			}
		default:
			if seg.Status == model.StatusRunning {
				_ = seg.Status.Transition(model.StatusSucceeded)
			}
		}
		if q.onProgress != nil && (seg.Status == model.StatusSucceeded ||
			seg.Status == model.StatusFailed || seg.Status == model.StatusInterrupted) {
			q.onProgress(seg)
		}
		q.mu.Unlock()
	}
}
