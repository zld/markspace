// Package worker runs unit-processing tasks under a bounded concurrency and
// commits each completed unit to a checkpoint as it finishes.
//
// Only tasks that a budget plan admitted should be handed to the pool.  The
// pool is idempotent: a task whose key is already checkpointed is replayed
// from the record instead of being recomputed, so re-running the same input
// and parameters produces the same result and an interrupted process resumes
// from its last complete unit.
package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"sync"

	"mediapipeline/internal/checkpoint"
)

// Task is one unit of work.  Key is the stable identity used by the
// checkpoint; the same key must always denote the same input bytes.
type Task struct {
	Key       string
	UnitIndex int
	Offset    int64
	Payload   []byte
}

// Process turns a task into output bytes deterministically.  The same task
// must always yield the same bytes or checkpoint replay would be unsound.
type Process func(Task) ([]byte, error)

// Result is the outcome for one task, in key order.
type Result struct {
	Key      string
	Digest   string // hex SHA-256 of the task output
	Bytes    int64
	Replayed bool // satisfied from the checkpoint, not recomputed
}

// Summary describes a pool run.
type Summary struct {
	Total       int
	Processed   int // newly computed and committed
	Replayed    int // satisfied from existing checkpoint records
	Interrupted bool
	Results     []Result // ordered by key
}

// Options configure a pool.
type Options struct {
	// Concurrency caps simultaneously running processes.  Values below one
	// are raised to one.
	Concurrency int
	// Process performs the work.
	Process Process
	// InterruptAfter, when positive, simulates an abrupt stop once this many
	// units have been newly committed.  Scheduling becomes sequential so the
	// interruption point is exact and reproducible.
	InterruptAfter int
}

// Pool executes tasks against one checkpoint store.
type Pool struct {
	opts  Options
	store *checkpoint.Store
	mu    sync.Mutex // serialises checkpoint commits
}

// New validates options and builds a pool.
func New(opts Options, store *checkpoint.Store) (*Pool, error) {
	if store == nil {
		return nil, errors.New("worker: nil checkpoint store")
	}
	if opts.Process == nil {
		return nil, errors.New("worker: nil process function")
	}
	if opts.Concurrency < 1 {
		opts.Concurrency = 1
	}
	if opts.InterruptAfter > 0 {
		// A requested interruption is deterministic: run one unit at a time so
		// exactly InterruptAfter units are committed before the stop.
		opts.Concurrency = 1
	}
	return &Pool{opts: opts, store: store}, nil
}

// errInterrupt signals a deliberate stop; it never escapes Run.
var errInterrupt = errors.New("worker: interrupted")

// Run executes tasks, skipping those already committed.  It returns a summary
// even when interrupted; a processing or checkpoint error is returned as the
// second result with whatever completed work already persisted.
func (p *Pool) Run(ctx context.Context, tasks []Task) (Summary, error) {
	summary := Summary{Total: len(tasks)}
	seen := make(map[string]struct{}, len(tasks))
	var pending []Task
	for _, t := range tasks {
		if _, dup := seen[t.Key]; dup {
			return summary, errors.New("worker: duplicate task key " + t.Key)
		}
		seen[t.Key] = struct{}{}
		if p.store.Done(t.Key) {
			summary.Replayed++
			continue
		}
		pending = append(pending, t)
	}

	sem := make(chan struct{}, p.opts.Concurrency)
	var (
		wg                  sync.WaitGroup
		mu                  sync.Mutex
		firstErr            error
		stopped             bool
		processed, replayed int
		results             = make(map[string]Result, len(tasks))
	)
	// Seed results with checkpoint replays so the summary is complete.
	for _, t := range tasks {
		if rec, ok := p.store.Record(t.Key); ok {
			results[t.Key] = Result{Key: t.Key, Digest: rec.Digest, Bytes: rec.Bytes, Replayed: true}
		}
	}

schedule:
	for _, task := range pending {
		// Acquire a slot before consulting progress: with the interrupt
		// simulation forced to one worker this guarantees every earlier task
		// has finished, so the exact interruption point is reproducible.
		sem <- struct{}{}
		mu.Lock()
		stop := firstErr != nil || (p.opts.InterruptAfter > 0 && processed >= p.opts.InterruptAfter)
		if !stop {
			if err := ctx.Err(); err != nil {
				firstErr = err
				stop = true
			}
		}
		if stop {
			stopped = true
		}
		mu.Unlock()
		if stop {
			<-sem
			break schedule
		}

		wg.Add(1)
		go func(t Task) {
			defer wg.Done()
			defer func() { <-sem }()
			out, err := p.opts.Process(t)
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				return
			}
			sum := sha256.Sum256(out)
			digest := hex.EncodeToString(sum[:])
			p.mu.Lock()
			committed, cerr := p.store.Commit(t.Key, digest, int64(len(out)))
			p.mu.Unlock()
			if cerr != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = cerr
				}
				mu.Unlock()
				return
			}
			mu.Lock()
			if committed {
				processed++
			} else {
				replayed++
			}
			results[t.Key] = Result{Key: t.Key, Digest: digest, Bytes: int64(len(out)), Replayed: !committed}
			mu.Unlock()
		}(task)
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	summary.Processed = processed
	summary.Replayed += replayed
	summary.Interrupted = stopped
	if firstErr != nil {
		return summary, firstErr
	}
	summary.Results = make([]Result, 0, len(results))
	for _, r := range results {
		summary.Results = append(summary.Results, r)
	}
	sort.Slice(summary.Results, func(i, j int) bool { return summary.Results[i].Key < summary.Results[j].Key })
	return summary, nil
}

// OutputDigest folds the ordered per-task digests into one summary digest.
// The results are sorted by key first so the digest is independent of
// completion order and of the order results were collected in.
func OutputDigest(results []Result) string {
	ordered := make([]Result, len(results))
	copy(ordered, results)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Key < ordered[j].Key })
	h := sha256.New()
	for _, r := range ordered {
		h.Write([]byte(r.Key))
		h.Write([]byte{0})
		h.Write([]byte(r.Digest))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}
