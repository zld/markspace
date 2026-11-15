// Package scheduler drives segment encoding with a bounded worker pool.
// It owns every mutation of segment state: workers stay pure and return an
// immutable Outcome, which the scheduler applies (and checkpoints) under its
// mutex. Succeeded segments are never resubmitted.
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

// Outcome is the immutable result of one worker invocation. Workers must
// not mutate the segment; they return its encode/quality payloads here.
type Outcome struct {
	Segment     *model.Segment
	Encode      *model.EncodeResult
	Quality     *model.QualityResult
	FailureNote string
	Err         error
}

// WorkerFunc executes one segment without touching shared state.
type WorkerFunc func(ctx context.Context, job Job) Outcome

// OnSegmentProgress is invoked after every terminal transition while the
// scheduler mutex is held, so the whole segment set is stable on disk reads.
type OnSegmentProgress func(seg *model.Segment)

// Queue schedules the segments of one task.
type Queue struct {
	workers    int
	work       WorkerFunc
	onProgress OnSegmentProgress
	mu         sync.Mutex
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

// Run processes every eligible segment and returns terminal counts when the
// pool drains. Cancellation marks in-flight segments interrupted; already
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
			continue // succeeded/infeasible: do not resubmit
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
		return seg.Status.Transition(model.StatusRunning) == nil
	case model.StatusInterrupted, model.StatusFailed:
		if err := seg.ResetToQueued(); err != nil {
			return false
		}
		return seg.Status.Transition(model.StatusRunning) == nil
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
		outcome := q.work(ctx, job)
		q.mu.Lock()
		seg := job.Segment
		switch {
		case ctx.Err() != nil:
			seg.MarkInterrupted()
		case outcome.Err != nil:
			seg.Encode = outcome.Encode // may carry partial verification data
			seg.FailureNote = firstNonEmpty(outcome.FailureNote, outcome.Err.Error())
			if seg.Status == model.StatusRunning {
				_ = seg.Status.Transition(model.StatusFailed)
			}
		default:
			seg.Encode = outcome.Encode
			seg.Quality = outcome.Quality
			if seg.Status == model.StatusRunning {
				_ = seg.Status.Transition(model.StatusSucceeded)
			}
		}
		seg.Attempts++
		if q.onProgress != nil && (seg.Status == model.StatusSucceeded ||
			seg.Status == model.StatusFailed || seg.Status == model.StatusInterrupted) {
			q.onProgress(seg)
		}
		q.mu.Unlock()
	}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
