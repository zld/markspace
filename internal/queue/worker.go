package queue

import (
	"context"
	"sync"
)

// JobFunc executes one job. A non-nil return marks the attempt failed.
type JobFunc func(ctx context.Context, j *Job) error

// Pool runs jobs with bounded concurrency. Because each worker runs at most
// one external process at a time, Concurrency also caps the number of
// simultaneous FFmpeg processes.
type Pool struct {
	Concurrency int
	Retries     int // retries after the first attempt
	Log         *StateLog
}

// Run schedules all jobs and blocks until they finish or ctx is canceled.
// On cancellation no new jobs are started and pending jobs become Canceled;
// in-flight jobs observe ctx and are expected to abort promptly.
func (p *Pool) Run(ctx context.Context, jobs []*Job, fn JobFunc) {
	ch := make(chan *Job)
	var wg sync.WaitGroup
	for i := 0; i < p.Concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range ch {
				p.exec(ctx, j, fn)
			}
		}()
	}
feed:
	for _, j := range jobs {
		select {
		case <-ctx.Done():
			// Stop accepting new work; drain the rest as canceled.
			for ; j != nil; j = nextJob(jobs, j) {
				from := j.State
				if err := j.Transition(Canceled); err == nil {
					p.Log.LogTransition(j.ID, from, Canceled, j.Attempts, ctx.Err())
				}
			}
			break feed
		case ch <- j:
		}
	}
	close(ch)
	wg.Wait()
}

// nextJob returns the job after cur in jobs, or nil.
func nextJob(jobs []*Job, cur *Job) *Job {
	for i, j := range jobs {
		if j == cur && i+1 < len(jobs) {
			return jobs[i+1]
		}
	}
	return nil
}

// exec runs one job with retries. Every transition is validated by the state
// machine and persisted to the log.
func (p *Pool) exec(ctx context.Context, j *Job, fn JobFunc) {
	for attempt := 1; ; attempt++ {
		from := j.State
		if err := j.Transition(Running); err != nil {
			j.Err = err.Error()
			p.Log.LogTransition(j.ID, from, j.State, attempt, err)
			return
		}
		j.Attempts = attempt
		p.Log.LogTransition(j.ID, from, Running, attempt, nil)

		runErr := fn(ctx, j)
		from = j.State
		switch {
		case runErr == nil:
			_ = j.Transition(Success)
			p.Log.LogTransition(j.ID, from, Success, attempt, nil)
			return
		case ctx.Err() != nil:
			j.Err = runErr.Error()
			_ = j.Transition(Canceled)
			p.Log.LogTransition(j.ID, from, Canceled, attempt, runErr)
			return
		case attempt > p.Retries:
			j.Err = runErr.Error()
			_ = j.Transition(Failed)
			p.Log.LogTransition(j.ID, from, Failed, attempt, runErr)
			return
		default:
			// Record the failed attempt, then retry via Failed -> Running.
			_ = j.Transition(Failed)
			p.Log.LogTransition(j.ID, from, Failed, attempt, runErr)
		}
	}
}
