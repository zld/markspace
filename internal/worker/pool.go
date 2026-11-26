package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"regress/internal/fault"
	"regress/internal/plan"
)

// Job is one independently executable encode task.
type Job struct {
	Key       string // stable task key used for recovery/claim
	SegmentID int
	Candidate plan.Candidate
}

// Result is a staged job outcome. The product lives in the staging directory
// until Publish promotes it; its hash is computed before promotion so a later
// content mismatch can be detected.
type Result struct {
	Job          Job
	StagedPath   string
	Hash         string
	OutputBytes  int64
	FrameCount   int
	SegmentBytes []int64
}

// Executor turns a job into a staged output. The production implementation
// wraps FFmpegEncoder; tests inject an in-memory fake.
type Executor interface {
	Execute(ctx context.Context, j Job, stageDir string) (*Result, error)
}

// Pool runs jobs with a fixed concurrency limit. Memory use scales with the
// number of in-flight jobs, never with the total job count.
type Pool struct {
	workers int
	exec    Executor
	stage   string
}

// slot holds one job's outcome in original submission order.
type slot struct {
	res *Result
	err error
}

// NewPool creates a pool. A non-positive worker count is clamped to one.
func NewPool(workers int, exec Executor, stageDir string) *Pool {
	if workers < 1 {
		workers = 1
	}
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		panic(err) // construction error is a programmer/IO fault, surfaced early
	}
	return &Pool{workers: workers, exec: exec, stage: stageDir}
}

// Run executes every job, returning results in the original job order. The
// first error cancels the remaining work. No partially staged product is left
// referenced: failed jobs are removed from the staging directory.
func (p *Pool) Run(ctx context.Context, jobs []Job) ([]*Result, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	slots := make([]*slot, len(jobs))
	for i := range slots {
		slots[i] = &slot{}
	}
	jobsCh := make(chan int)
	var wg sync.WaitGroup
	var firstErr error
	var errMu sync.Mutex
	for w := 0; w < p.workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobsCh {
				if ctx.Err() != nil {
					return
				}
				res, err := p.runOne(ctx, jobs[idx])
				slots[idx].res = res
				slots[idx].err = err
				if err != nil {
					errMu.Lock()
					if firstErr == nil {
						firstErr = err
						cancel()
					}
					errMu.Unlock()
				}
			}
		}()
	}
	for i := range jobs {
		select {
		case jobsCh <- i:
		case <-ctx.Done():
		}
	}
	close(jobsCh)
	wg.Wait()

	if firstErr != nil {
		p.cleanupOnError(slots)
		return nil, firstErr
	}
	out := make([]*Result, len(jobs))
	for i, s := range slots {
		out[i] = s.res
	}
	return out, nil
}

func (p *Pool) runOne(ctx context.Context, j Job) (*Result, error) {
	stageDir := filepath.Join(p.stage, j.Key)
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		return nil, fault.New(fault.StageWorker, fault.CodeIO, "", err.Error()).Wrap(err)
	}
	res, err := p.exec.Execute(ctx, j, stageDir)
	if err != nil {
		_ = os.RemoveAll(stageDir)
		return nil, err
	}
	hash, err := HashFile(res.StagedPath)
	if err != nil {
		_ = os.RemoveAll(stageDir)
		return nil, err
	}
	res.Hash = hash
	return res, nil
}

func (p *Pool) cleanupOnError(slots []*slot) {
	seen := map[string]bool{}
	for _, s := range slots {
		if s.res != nil && !seen[s.res.Job.Key] {
			seen[s.res.Job.Key] = true
			_ = os.RemoveAll(filepath.Join(p.stage, s.res.Job.Key))
		}
	}
}

// HashFile streams a file through SHA-256 without loading it fully, returning
// a hex digest.
func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fault.New(fault.StageWorker, fault.CodeIO, "", err.Error()).Wrap(err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fault.New(fault.StageWorker, fault.CodeIO, "", err.Error()).Wrap(err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// AtomicPublish promotes a staged file into dest with a temp-file-and-rename
// sequence, so readers never observe a half-written product.
func AtomicPublish(staged, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fault.New(fault.StagePublish, fault.CodeIO, "", err.Error()).Wrap(err)
	}
	tmp := dest + ".tmp"
	if err := copyFile(staged, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return fault.New(fault.StagePublish, fault.CodeIO, "", err.Error()).Wrap(err)
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fault.New(fault.StagePublish, fault.CodeIO, "", err.Error()).Wrap(err)
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return fault.New(fault.StagePublish, fault.CodeIO, "", err.Error()).Wrap(err)
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return fault.New(fault.StagePublish, fault.CodeIO, "", err.Error()).Wrap(err)
	}
	return out.Sync()
}

// VerifyHash re-hashes a file and compares it to the recorded digest,
// returning fault.CodeHashMismatch on any difference.
func VerifyHash(path, want string) error {
	got, err := HashFile(path)
	if err != nil {
		return err
	}
	if got != want {
		return fault.New(fault.StageVerify, fault.CodeHashMismatch, "",
			fmt.Sprintf("hash mismatch for %s: recorded=%s actual=%s", path, want, got))
	}
	return nil
}
