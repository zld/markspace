// Package pipeline runs bounded, ordered, cancellable sample-processing jobs.
package pipeline

import (
	"context"
	"sort"
	"sync"

	"archivecheck/internal/diagnostic"
)

const (
	MaxWorkers     = 8
	MaxQueue       = 64
	MaxJobs        = 10_000
	defaultWorkers = 2
)

// Options bounds concurrency and queue memory.
type Options struct {
	Workers              int
	QueueCapacity        int
	CancelAfterCompleted int
}

// Normalize clamps parameters to fixed upper bounds.
func (o Options) Normalize() Options {
	if o.Workers <= 0 {
		o.Workers = defaultWorkers
	}
	if o.Workers > MaxWorkers {
		o.Workers = MaxWorkers
	}
	if o.QueueCapacity <= 0 {
		o.QueueCapacity = o.Workers * 2
	}
	if o.QueueCapacity > MaxQueue {
		o.QueueCapacity = MaxQueue
	}
	if o.QueueCapacity < 1 {
		o.QueueCapacity = 1
	}
	return o
}

// Outcome is the deterministic processing result supplied by a Job.
type Outcome struct {
	Output          string `json:"output"`
	OutputBytes     []byte `json:"-"`
	InputBytes      int    `json:"input_bytes"`
	OutputByteCount int    `json:"output_bytes"`
	EstimatedBytes  int64  `json:"estimated_bytes"`
	TimeDeltaNS     int64  `json:"time_delta_ns"`
	ByteDelta       int64  `json:"byte_delta"`
	Details         any    `json:"details,omitempty"`
}

// Job is one numbered input. Index controls stable output order.
type Job struct {
	Index  int
	Input  string
	Sample int
	Work   func(context.Context) (Outcome, error)
}

// Result is the terminal state for a Job.
type Result struct {
	Index  int    `json:"index"`
	Input  string `json:"input"`
	Sample int    `json:"sample"`
	Status Status `json:"status"`
	Outcome
	Diagnostic *diagnostic.Error `json:"diagnostic,omitempty"`
}

// Counts summarizes terminal statuses.
type Counts struct {
	Total      int `json:"total"`
	Success    int `json:"success"`
	Failed     int `json:"failed"`
	Completed  int `json:"completed"`
	Canceled   int `json:"canceled"`
	NotStarted int `json:"not_started"`
}

// Run executes jobs with a bounded worker pool and returns index-ordered results.
func Run(parent context.Context, jobs []Job, opts Options) []Result {
	opts = opts.Normalize()
	if len(jobs) > MaxJobs {
		err := diagnostic.New(diagnostic.CodeTooManyItems, diagnostic.PhasePipeline, -1, 0,
			"job count %d exceeds limit %d", len(jobs), MaxJobs)
		return []Result{{Index: 0, Status: StatusFailed, Diagnostic: err}}
	}

	controller := NewController(parent)
	ctx := controller.Context()
	jobsCh := make(chan Job, opts.QueueCapacity)
	resultsCh := make(chan Result, len(jobs))
	var wg sync.WaitGroup

	for w := 0; w < opts.Workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobsCh {
				resultsCh <- executeJob(ctx, job)
			}
		}()
	}

	go produce(ctx, controller, jobs, jobsCh, resultsCh)
	go func() {
		wg.Wait()
		close(resultsCh)
	}()

	var results []Result
	completed := 0
	for result := range resultsCh {
		results = append(results, result)
		if result.Status == StatusSuccess || result.Status == StatusFailed {
			completed++
			if opts.CancelAfterCompleted > 0 && completed >= opts.CancelAfterCompleted {
				controller.Cancel(ErrCancelAfterBudget)
				opts.CancelAfterCompleted = 0
			}
		}
	}
	return MergeOrdered(results)
}

func produce(ctx context.Context, controller *Controller, jobs []Job, jobsCh chan<- Job, resultsCh chan<- Result) {
	defer close(jobsCh)
	for i, job := range jobs {
		if err := ctx.Err(); err != nil {
			for _, remaining := range jobs[i:] {
				resultsCh <- notStartedResult(remaining, context.Cause(ctx))
			}
			return
		}
		select {
		case jobsCh <- job:
		case <-ctx.Done():
			for _, remaining := range jobs[i:] {
				resultsCh <- notStartedResult(remaining, context.Cause(ctx))
			}
			return
		}
	}
}

func executeJob(ctx context.Context, job Job) Result {
	base := Result{Index: job.Index, Input: job.Input, Sample: job.Sample}
	if job.Work == nil {
		err := diagnostic.New(diagnostic.CodeInternal, diagnostic.PhasePipeline, job.Sample, 0, "job has no work function")
		base.Status = StatusFailed
		base.Diagnostic = err
		return base
	}

	select {
	case <-ctx.Done():
		base.Status = StatusCanceled
		base.Diagnostic = cancellationDiagnostic(job, context.Cause(ctx))
		return base
	default:
	}

	outcome, err := job.Work(ctx)
	base.Outcome = outcome
	if err != nil {
		if ctx.Err() != nil || diagnostic.HasCode(err, diagnostic.CodeCanceled) {
			base.Status = StatusCanceled
			base.Diagnostic = cancellationDiagnostic(job, err)
			return base
		}
		base.Status = StatusFailed
		base.Diagnostic = asDiagnostic(job, err)
		return base
	}
	if ctx.Err() != nil {
		base.Status = StatusCanceled
		base.Diagnostic = cancellationDiagnostic(job, context.Cause(ctx))
		return base
	}
	base.Status = StatusSuccess
	return base
}

func asDiagnostic(job Job, err error) *diagnostic.Error {
	if de, ok := diagnostic.From(err); ok {
		return de
	}
	return diagnostic.Wrap(diagnostic.CodeInternal, diagnostic.PhasePipeline, job.Sample, 0, err, "job failed")
}

func notStartedResult(job Job, cause error) Result {
	return Result{
		Index:      job.Index,
		Input:      job.Input,
		Sample:     job.Sample,
		Status:     StatusNotStarted,
		Diagnostic: cancellationDiagnostic(job, cause),
	}
}

// Count summarizes result statuses.
func Count(results []Result) Counts {
	c := Counts{Total: len(results)}
	for _, r := range results {
		switch r.Status {
		case StatusSuccess:
			c.Success++
			c.Completed++
		case StatusFailed:
			c.Failed++
			c.Completed++
		case StatusCanceled:
			c.Canceled++
		case StatusNotStarted:
			c.NotStarted++
		}
	}
	return c
}

// ByIndex returns results sorted by input index without mutating the argument.
func ByIndex(results []Result) []Result {
	out := append([]Result(nil), results...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Index < out[j].Index })
	return out
}
