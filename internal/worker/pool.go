// Package worker runs frame jobs with bounded concurrency and a bounded byte
// cache. A hard job failure cancels the rest of that track only; the other
// tracks still complete and partial output is returned with a fault list.
package worker

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"medialab/internal/model"
)

// Job is one unit of render work.
type Job struct {
	Track model.TrackID
	// Ordinal is the timeline position, used for deterministic result order.
	Ordinal int
	// Key identifies the frame (stable across runs).
	Key string
	// Size is the cache bytes the job holds while running.
	Size int64
	// Run performs the work and returns the product bytes.
	Run func(ctx context.Context) ([]byte, error)
}

// Outcome is the result of one job.
type Outcome struct {
	Job     Job
	Product []byte
	Fault   *model.Fault
	Skipped bool
}

// Config bounds the pool.
type Config struct {
	// Workers is the maximum number of concurrent jobs. Values below 1 become 1.
	Workers int
	// MaxCacheBytes bounds the sum of in-flight job sizes. A job larger than
	// the whole cap is rejected and cancels its track rather than running.
	MaxCacheBytes int64
	// FillPolicy names the rule used to materialise recoverable gaps.
	FillPolicy string
}

// Report is the pool's result.
type Report struct {
	Outcomes    []Outcome
	Faults      model.Faults
	PeakCache   int64
	PeakWorkers int
	Completed   int
	Skipped     int
	// TrackCancelled records the first hard fault per cancelled track.
	TrackCancelled map[model.TrackID]*model.Fault
}

// cacheBudget is a condition-variable guarded byte budget. Accounting is done
// on reservation, so PeakCache is an upper bound on bytes held at any instant.
type cacheBudget struct {
	mu   sync.Mutex
	cond *sync.Cond
	cap  int64
	used int64
	peak int64
}

func newCacheBudget(capBytes int64) *cacheBudget {
	c := &cacheBudget{cap: capBytes}
	c.cond = sync.NewCond(&c.mu)
	return c
}

// acquire blocks until n bytes fit. It returns false if ctx is done first.
func (c *cacheBudget) acquire(n int64, ctx context.Context) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for c.used+n > c.cap {
		if ctx.Err() != nil {
			return false
		}
		// Re-check on every broadcast; a helper goroutine wakes waiters when the
		// context is cancelled.
		c.cond.Wait()
	}
	if ctx.Err() != nil {
		return false
	}
	c.used += n
	if c.used > c.peak {
		c.peak = c.used
	}
	return true
}

func (c *cacheBudget) release(n int64) {
	c.mu.Lock()
	c.used -= n
	c.cond.Broadcast()
	c.mu.Unlock()
}

func (c *cacheBudget) peakUsed() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.peak
}

// wakeOnCancel makes waiters observe context cancellation.
func (c *cacheBudget) wakeOnCancel(ctx context.Context, done chan struct{}) {
	go func() {
		select {
		case <-ctx.Done():
			c.cond.Broadcast()
		case <-done:
		}
	}()
}

// Run executes jobs, returning one outcome per job in Ordinal order.
func Run(ctx context.Context, cfg Config, jobs []Job) *Report {
	if cfg.Workers < 1 {
		cfg.Workers = 1
	}
	if cfg.MaxCacheBytes <= 0 {
		cfg.MaxCacheBytes = 1 << 30
	}
	if cfg.FillPolicy == "" {
		cfg.FillPolicy = "ramp-fill"
	}
	rep := &Report{TrackCancelled: make(map[model.TrackID]*model.Fault)}
	outs := make([]Outcome, len(jobs))
	for i, j := range jobs {
		outs[i] = Outcome{Job: j}
	}

	order := make([]int, len(jobs))
	for i := range jobs {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		ja, jb := jobs[order[a]], jobs[order[b]]
		if ja.Ordinal != jb.Ordinal {
			return ja.Ordinal < jb.Ordinal
		}
		return ja.Key < jb.Key
	})

	budget := newCacheBudget(cfg.MaxCacheBytes)
	doneWake := make(chan struct{})
	defer close(doneWake)
	budget.wakeOnCancel(ctx, doneWake)

	var stateMu sync.Mutex
	cancelledTracks := make(map[model.TrackID]bool)

	slots := make(chan struct{}, cfg.Workers)
	var active sync.WaitGroup
	var peakWorkers int

	for _, idx := range order {
		j := jobs[idx]
		if j.Size < 0 {
			outs[idx].Fault = model.NewFault(model.KindWorker, model.CodeSizeBounds,
				j.Track, uint32(j.Ordinal), "negative job size")
			cancelTrack(&stateMu, cancelledTracks, rep, j.Track, outs[idx].Fault)
			continue
		}
		if j.Size > cfg.MaxCacheBytes {
			outs[idx].Fault = model.NewFault(model.KindWorker, model.CodeCacheOverflow,
				j.Track, uint32(j.Ordinal),
				fmt.Sprintf("job needs %d bytes, cache cap is %d", j.Size, cfg.MaxCacheBytes))
			cancelTrack(&stateMu, cancelledTracks, rep, j.Track, outs[idx].Fault)
			continue
		}
		if ctx.Err() != nil {
			outs[idx].Fault = model.NewFault(model.KindWorker, model.CodePayloadCorrupt,
				j.Track, uint32(j.Ordinal), "run cancelled before the job started")
			continue
		}
		stateMu.Lock()
		skip := cancelledTracks[j.Track]
		stateMu.Unlock()
		if skip {
			outs[idx].Skipped = true
			continue
		}

		// Reserve cache before taking a worker slot: a waiter never blocks the
		// jobs that would release cache.
		if !budget.acquire(j.Size, ctx) {
			outs[idx].Fault = model.NewFault(model.KindWorker, model.CodePayloadCorrupt,
				j.Track, uint32(j.Ordinal), "run cancelled while waiting for cache")
			continue
		}

		slots <- struct{}{}
		if len(slots) > peakWorkers {
			peakWorkers = len(slots)
		}
		stateMu.Lock()
		skip = cancelledTracks[j.Track]
		stateMu.Unlock()
		if skip {
			<-slots
			budget.release(j.Size)
			outs[idx].Skipped = true
			continue
		}

		active.Add(1)
		go func(idx int, j Job) {
			defer active.Done()
			defer func() {
				<-slots
				budget.release(j.Size)
			}()
			product, err := j.Run(ctx)
			if err != nil {
				f := classify(err, j, cfg.FillPolicy)
				outs[idx].Fault = f
				if !f.Recoverable {
					// The fault is reported once, through Report.Faults.
					cancelTrack(&stateMu, cancelledTracks, rep, j.Track, f)
				}
				return
			}
			outs[idx].Product = product
		}(idx, j)
	}
	active.Wait()

	for i := range outs {
		rep.Outcomes = append(rep.Outcomes, outs[i])
		switch {
		case outs[i].Skipped:
			rep.Skipped++
		case outs[i].Fault != nil:
			rep.Faults = append(rep.Faults, outs[i].Fault)
		default:
			rep.Completed++
		}
	}
	rep.Faults.Sort()
	rep.PeakCache = budget.peakUsed()
	rep.PeakWorkers = peakWorkers
	return rep
}

// cancelTrack records the first hard fault for a track and marks its remaining
// jobs to be skipped. The fault is also emitted once through the job outcome.
func cancelTrack(mu *sync.Mutex, cancelled map[model.TrackID]bool, rep *Report,
	track model.TrackID, f *model.Fault) {
	mu.Lock()
	defer mu.Unlock()
	if cancelled[track] {
		return
	}
	cancelled[track] = true
	rep.TrackCancelled[track] = f
}

func classify(err error, j Job, fillPolicy string) *model.Fault {
	if f, ok := model.AsFault(err); ok {
		out := *f
		if out.Kind == "" {
			out.Kind = model.KindWorker
		}
		if out.Track == 0 {
			out.Track = j.Track
		}
		if out.Frame == 0 {
			out.Frame = uint32(j.Ordinal)
		}
		if out.Message == "" {
			out.Message = fmt.Sprintf("job failed under fill policy %q", fillPolicy)
		}
		return &out
	}
	return model.NewFault(model.KindWorker, model.CodeArtifactMismatch,
		j.Track, uint32(j.Ordinal), err.Error())
}
