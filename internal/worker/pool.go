// Package worker runs slice jobs concurrently with idempotent re-runs:
// a job whose output already exists with matching parameters is reused,
// and duplicate job IDs are collapsed so repeated tasks are never
// counted twice.
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
)

// Result is the outcome of one job.
type Result struct {
	JobID      string `json:"job_id"`
	OutputPath string `json:"output_path"`
	SHA256     string `json:"sha256"`
	SizeBytes  int64  `json:"size_bytes"`
	DurationMS int64  `json:"duration_ms"`
	Reused     bool   `json:"reused"` // loaded from a previous run's marker
}

// Job is one unit of work. Run must produce OutputPath and fill in the
// result fields; the pool records identity fields itself.
type Job struct {
	ID         string
	ParamsHash string // changes whenever any input or parameter changes
	OutputPath string
	Run        func(ctx context.Context) (Result, error)
}

// Pool executes jobs with bounded concurrency.
type Pool struct {
	Workers int
}

// New returns a Pool with at least one worker.
func New(workers int) *Pool {
	if workers < 1 {
		workers = 1
	}
	return &Pool{Workers: workers}
}

type marker struct {
	ParamsHash string `json:"params_hash"`
	Result     Result `json:"result"`
}

// Run executes jobs concurrently, in input order for results. Jobs with a
// duplicate ID are dropped (first wins). A job whose marker file matches
// its ParamsHash is not re-executed; its recorded result is reused, so
// interrupted runs resume without double-counting work.
func (p *Pool) Run(ctx context.Context, jobs []Job) ([]Result, error) {
	seen := make(map[string]bool, len(jobs))
	uniq := make([]Job, 0, len(jobs))
	for _, j := range jobs {
		if j.ID == "" {
			return nil, errors.New("worker: job with empty ID")
		}
		if seen[j.ID] {
			continue
		}
		seen[j.ID] = true
		uniq = append(uniq, j)
	}

	results := make([]Result, len(uniq))
	errs := make([]error, len(uniq))
	sem := make(chan struct{}, p.Workers)
	var wg sync.WaitGroup
	for i, j := range uniq {
		if r, ok := loadMarker(j); ok {
			r.Reused = true
			results[i] = r
			continue
		}
		wg.Add(1)
		go func(i int, j Job) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				errs[i] = ctx.Err()
				return
			}
			r, err := j.Run(ctx)
			if err != nil {
				errs[i] = fmt.Errorf("job %s: %w", j.ID, err)
				return
			}
			r.JobID = j.ID
			r.OutputPath = j.OutputPath
			if err := writeMarker(j, r); err != nil {
				errs[i] = fmt.Errorf("job %s: %w", j.ID, err)
				return
			}
			results[i] = r
		}(i, j)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return results, err
		}
	}
	return results, nil
}

func markerPath(j Job) string { return j.OutputPath + ".done.json" }

func loadMarker(j Job) (Result, bool) {
	b, err := os.ReadFile(markerPath(j))
	if err != nil {
		return Result{}, false
	}
	var m marker
	if err := json.Unmarshal(b, &m); err != nil {
		return Result{}, false
	}
	if m.ParamsHash != j.ParamsHash {
		return Result{}, false
	}
	if _, err := os.Stat(j.OutputPath); err != nil {
		return Result{}, false
	}
	return m.Result, true
}

func writeMarker(j Job, r Result) error {
	b, err := json.Marshal(marker{ParamsHash: j.ParamsHash, Result: r})
	if err != nil {
		return err
	}
	tmp := markerPath(j) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, markerPath(j))
}
