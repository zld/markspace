// Package jobs runs per-segment QP searches on a bounded worker pool,
// persisting every candidate through the store state machine.
package jobs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"rateqa/internal/control"
	"rateqa/internal/media"
	"rateqa/internal/store"
)

// MaxWorkers caps concurrent ffmpeg-driven work as required by the spec.
const MaxWorkers = 4

// ErrInterrupted marks a run stopped by fail-after; it is resumable.
var ErrInterrupted = errors.New("run interrupted after fail-after threshold")

// SegmentSpec describes one scene segment to process.
type SegmentSpec struct {
	Ref        media.SegmentRef
	TargetKbps float64
}

// Executor runs one (segment, QP) candidate (encode + decode + compare).
type Executor func(ctx context.Context, spec SegmentSpec, qp int) (ExecResult, error)

// ExecResult is what one candidate execution returns.
type ExecResult struct {
	Bytes int64
	Stats media.FrameStats
}

// Config configures a Scheduler run.
type Config struct {
	Workers   int
	FailAfter int // >0: stop after this many newly completed candidates
}

// SegmentResult is the final decision for one segment.
type SegmentResult struct {
	Index      int
	Selected   control.Sample
	Bounded    bool
	Reason     string
	Rounds     int
	Candidates []control.Sample
}

// Scheduler executes segment searches with bounded concurrency.
type Scheduler struct {
	st        *store.Store
	exec      Executor
	tmpDir    string
	workers   int
	failAfter int
}

// New builds a Scheduler. tmpDir holds per-candidate files removed right after
// measurement, bounding temporary space to ~2 files per in-flight candidate.
func New(st *store.Store, workspace string, cfg Config, exec Executor) *Scheduler {
	w := cfg.Workers
	if w < 1 {
		w = 1
	}
	if w > MaxWorkers {
		w = MaxWorkers
	}
	return &Scheduler{
		st: st, exec: exec, tmpDir: filepath.Join(workspace, "tmp"),
		workers: w, failAfter: cfg.FailAfter,
	}
}

// Workers returns the effective (capped) worker count.
func (s *Scheduler) Workers() int { return s.workers }

func jobID(seg, qp int) string { return fmt.Sprintf("seg%03d-qp%02d", seg, qp) }

// Run processes all segments. Bootstrap candidates of every segment are
// enqueued first so they persist as queued work; a fail-after stop therefore
// leaves a durable backlog that resume reclaims without duplicate claims.
func (s *Scheduler) Run(ctx context.Context, specs []SegmentSpec) ([]SegmentResult, error) {
	if err := os.MkdirAll(s.tmpDir, 0o755); err != nil {
		return nil, err
	}
	for _, spec := range specs {
		for _, qp := range control.InitialProbes() {
			if err := s.st.Enqueue(jobID(spec.Ref.Index, qp), spec.Ref.Index, qp); err != nil {
				return nil, err
			}
		}
	}
	sem := make(chan struct{}, s.workers)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var results []SegmentResult
	var firstErr error
	var completed int64
	interrupted := atomic.Bool{}

	for _, spec := range specs {
		if ctx.Err() != nil || interrupted.Load() {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(spec SegmentSpec) {
			defer wg.Done()
			defer func() { <-sem }()
			if _, done := s.st.DoneSegment(spec.Ref.Index); done {
				if r, ok := s.buildResult(spec); ok {
					mu.Lock()
					results = append(results, r)
					mu.Unlock()
				}
				return
			}
			res, err := s.runSegment(ctx, spec, &completed, &interrupted)
			if err != nil {
				if !errors.Is(err, ErrInterrupted) {
					mu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					mu.Unlock()
				}
				return
			}
			mu.Lock()
			results = append(results, res)
			mu.Unlock()
		}(spec)
	}
	wg.Wait()
	results = sortByIndex(results)
	if firstErr != nil {
		return results, firstErr
	}
	if interrupted.Load() || ctx.Err() != nil {
		_ = s.st.MarkInterrupted()
		return results, ErrInterrupted
	}
	return results, nil
}

func (s *Scheduler) runSegment(ctx context.Context, spec SegmentSpec,
	completed *int64, interrupted *atomic.Bool) (SegmentResult, error) {
	search := control.NewSegmentSearch(spec.TargetKbps)
	for _, j := range s.st.Snapshot() {
		if j.Segment == spec.Ref.Index && j.State == store.StateCompleted {
			search.Observe(control.Sample{
				QP: j.QP, BitrateKbps: j.Metrics.BitrateKbps, PSNRdB: j.Metrics.PSNRMean,
			})
		}
	}
	for {
		if ctx.Err() != nil || interrupted.Load() {
			return SegmentResult{Index: spec.Ref.Index}, ErrInterrupted
		}
		qp := search.Next()
		if qp < 0 {
			break
		}
		id := jobID(spec.Ref.Index, qp)
		if err := s.st.Enqueue(id, spec.Ref.Index, qp); err != nil {
			return SegmentResult{}, err
		}
		_, ok, err := s.st.Claim(id)
		if err != nil {
			return SegmentResult{}, err
		}
		if !ok {
			if j := s.st.Snapshot()[id]; j.State == store.StateCompleted {
				search.Observe(control.Sample{QP: qp,
					BitrateKbps: j.Metrics.BitrateKbps, PSNRdB: j.Metrics.PSNRMean})
				continue
			}
			return SegmentResult{}, fmt.Errorf("jobs: candidate %s not claimable", id)
		}
		res, execErr := s.exec(ctx, spec, qp)
		if execErr != nil {
			terminal, rerr := s.st.Retry(id, execErr)
			if rerr != nil {
				return SegmentResult{}, rerr
			}
			if terminal {
				return SegmentResult{}, fmt.Errorf("jobs: segment %d qp %d failed: %w",
					spec.Ref.Index, qp, execErr)
			}
			continue
		}
		dur := spec.Ref.Header().DurationSeconds(spec.Ref.Frames)
		kbps := float64(res.Bytes) * 8 / 1000 / dur
		m := store.CandidateMetrics{
			Bytes: res.Bytes, BitrateKbps: kbps,
			ErrorPct: 100 * (kbps - spec.TargetKbps) / spec.TargetKbps,
			PSNRMean: res.Stats.PSNRMean, PSNRMin: res.Stats.PSNRMin,
			PSNRMax: res.Stats.PSNRMax, PSNRStd: res.Stats.PSNRStd,
			Frames: res.Stats.Frames,
		}
		if err := s.st.Complete(id, m); err != nil {
			return SegmentResult{}, err
		}
		search.Observe(control.Sample{QP: qp, BitrateKbps: kbps, PSNRdB: m.PSNRMean})
		n := atomic.AddInt64(completed, 1)
		if s.failAfter > 0 && n >= int64(s.failAfter) {
			interrupted.Store(true)
			return SegmentResult{Index: spec.Ref.Index}, ErrInterrupted
		}
	}
	best, err := search.Best()
	if err != nil {
		return SegmentResult{}, err
	}
	if err := s.st.MarkSegmentDone(spec.Ref.Index, best.QP); err != nil {
		return SegmentResult{}, err
	}
	return SegmentResult{
		Index: spec.Ref.Index, Selected: best, Bounded: search.Bounded(),
		Reason: search.Reason(), Rounds: search.Rounds(), Candidates: search.Samples(),
	}, nil
}

func (s *Scheduler) buildResult(spec SegmentSpec) (SegmentResult, bool) {
	search := control.NewSegmentSearch(spec.TargetKbps)
	for _, j := range s.st.Snapshot() {
		if j.Segment == spec.Ref.Index && j.State == store.StateCompleted {
			search.Observe(control.Sample{
				QP: j.QP, BitrateKbps: j.Metrics.BitrateKbps, PSNRdB: j.Metrics.PSNRMean,
			})
		}
	}
	best, err := search.Best()
	if err != nil {
		return SegmentResult{}, false
	}
	return SegmentResult{Index: spec.Ref.Index, Selected: best,
		Bounded: search.Bounded(), Reason: search.Reason(),
		Rounds: search.Rounds(), Candidates: search.Samples()}, true
}

func sortByIndex(in []SegmentResult) []SegmentResult {
	for i := 1; i < len(in); i++ {
		for j := i; j > 0 && in[j-1].Index > in[j].Index; j-- {
			in[j-1], in[j] = in[j], in[j-1]
		}
	}
	return in
}
