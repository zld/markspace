// Package orchestrator wires planning, encoding, quality analysis and
// checkpointing into one resumable job. It contains no CLI logic: the
// command layer builds a Config and interprets the Result and exit code.
package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"sync"

	"transcode/internal/codec"
	"transcode/internal/media"
	"transcode/internal/model"
	"transcode/internal/plan"
	"transcode/internal/quality"
	"transcode/internal/safeio"
	"transcode/internal/scheduler"
	"transcode/internal/state"
)

// Config is the validated run configuration.
type Config struct {
	JobID           string
	WorkspaceRoot   string // all input, output and checkpoint paths stay inside
	InputPath       string
	OutputDir       string
	WorkDir         string // per-segment source clips for the encoder
	CheckpointPath  string
	DurationSec     float64
	SegmentSec      float64
	TargetKbps      float64
	MinKbps         float64
	Workers         int
	MaxOutputBytes  int64
	EncoderTemplate string
	Samples         []float64
	Params          state.Params
}

// Result is everything the CLI needs to render a report and pick an exit code.
type Result struct {
	Task            *model.Task
	Recovery        *state.RecoveryOutcome
	Quality         *quality.Report
	Summaries       []quality.SegmentSummary
	Succeeded       int
	Failed          int
	Interrupted     int
	Infeasible      []int
	CheckpointError error
}

// Engine runs one job end to end.
type Engine struct {
	cfg    Config
	runner *codec.Runner
	store  *state.Store
	cpPath string
	task   *model.Task
	source *media.Video
	mu     sync.Mutex // serializes checkpoint writes from worker callbacks
}

// New validates filesystem boundaries and prepares the checkpoint store.
func New(cfg Config) (*Engine, error) {
	if cfg.EncoderTemplate == "" {
		return nil, errors.New("encoder command must be explicitly specified")
	}
	for _, p := range []string{cfg.InputPath, cfg.OutputDir, cfg.WorkDir, cfg.CheckpointPath} {
		if _, err := safeio.ResolveUnder(cfg.WorkspaceRoot, p); err != nil {
			return nil, err
		}
	}
	store, cpPath, err := state.NewStore(cfg.WorkspaceRoot, cfg.CheckpointPath)
	if err != nil {
		return nil, err
	}
	return &Engine{cfg: cfg, runner: codec.NewRunner(), store: store, cpPath: cpPath}, nil
}

// Run plans (or recovers) the task and drives it through the scheduler.
func (e *Engine) Run(ctx context.Context) (*Result, error) {
	task, recovery, err := e.planOrRecover()
	if err != nil {
		return nil, err
	}
	src, err := media.ReadVideo(e.cfg.InputPath)
	if err != nil {
		return nil, fmt.Errorf("read source: %w", err)
	}
	e.source = src
	if err := mkdirAll(e.cfg.OutputDir, e.cfg.WorkDir); err != nil {
		return nil, err
	}

	infeasible := make([]int, 0)
	for _, seg := range task.Segments {
		if !seg.Feasible {
			infeasible = append(infeasible, seg.Index+1)
			seg.Status = model.StatusFailed
			seg.FailureNote = seg.Infeasible
		}
	}

	q, err := scheduler.New(e.cfg.Workers, e.worker, e.persistProgress)
	if err != nil {
		return nil, err
	}
	e.task = task
	succeeded, failed, interrupted := q.Run(ctx, task)
	res := &Result{
		Task: task, Recovery: recovery,
		Succeeded: succeeded, Failed: failed,
		Interrupted: interrupted, Infeasible: infeasible,
	}
	e.buildQuality(res)
	if cpErr := e.persist(task); cpErr != nil {
		res.CheckpointError = cpErr
	}
	return res, nil
}

func (e *Engine) planOrRecover() (*model.Task, *state.RecoveryOutcome, error) {
	specs, err := plan.Plan(e.cfg.DurationSec, e.cfg.SegmentSec,
		e.cfg.TargetKbps, e.cfg.MinKbps, e.cfg.Samples)
	if err != nil {
		return nil, nil, err
	}
	fp, err := media.Fingerprint(e.cfg.InputPath)
	if err != nil {
		return nil, nil, fmt.Errorf("fingerprint input: %w", err)
	}
	task := &model.Task{
		ID: e.cfg.JobID, InputPath: e.cfg.InputPath, InputFingerprint: fp,
		DurationSec: e.cfg.DurationSec, SegmentSec: e.cfg.SegmentSec,
		TargetKbps: e.cfg.TargetKbps, MinKbps: e.cfg.MinKbps,
		Segments: make([]*model.Segment, len(specs)),
	}
	for i, sp := range specs {
		task.Segments[i] = &model.Segment{
			Index: i, StartSec: sp.StartSec, DurationSec: sp.DurationSec,
			Complexity: sp.Complexity, TargetKbps: sp.TargetKbps,
			Feasible: sp.Feasible, Infeasible: sp.InfeasibleReason,
			Status: model.StatusQueued,
		}
	}

	cp, err := e.store.Load(e.cpPath)
	if err != nil {
		return nil, nil, err // ErrCorruptCheckpoint -> CLI exit 4
	}
	recovery := &state.RecoveryOutcome{}
	if cp != nil {
		recovery, err = state.Recover(cp, task, e.cfg.InputPath)
		if err != nil {
			return nil, nil, err
		}
	}
	return task, recovery, nil
}

// frameRange maps segment time bounds onto source frame indices.
func (e *Engine) frameRange(seg *model.Segment) (int, int) {
	fps := float64(e.source.FPS)
	n := len(e.source.Frames)
	start := int(math.Round(seg.StartSec * fps))
	end := int(math.Round((seg.StartSec + seg.DurationSec) * fps))
	if start < 0 {
		start = 0
	}
	if end > n {
		end = n
	}
	if end <= start { // guarantee at least one frame per segment
		end = start + 1
	}
	return start, end
}

func (e *Engine) worker(ctx context.Context, job scheduler.Job) scheduler.Outcome {
	seg := job.Segment
	start, end := e.frameRange(seg)
	clip := &media.Video{
		Width: e.source.Width, Height: e.source.Height, FPS: e.source.FPS,
		Frames: e.source.Frames[start:end],
	}
	segIn := filepath.Join(e.cfg.WorkDir, fmt.Sprintf("segment-%03d.vid", seg.Index+1))
	segOut := filepath.Join(e.cfg.OutputDir, fmt.Sprintf("segment-%03d.enc", seg.Index+1))
	if err := media.WriteVideo(segIn, clip); err != nil {
		return scheduler.Outcome{Segment: seg,
			FailureNote: "write segment input: " + err.Error(),
			Err:         err}
	}
	frameDuration := float64(end-start) / float64(e.source.FPS)
	res, err := e.runner.Run(ctx, codec.Spec{
		CommandTemplate: e.cfg.EncoderTemplate,
		InputPath:       segIn,
		OutputPath:      segOut,
		TargetKbps:      seg.TargetKbps,
		DurationSec:     frameDuration,
		MaxOutputBytes:  e.cfg.MaxOutputBytes,
	})
	if err != nil {
		note := err.Error()
		if res != nil && res.FailureNote != "" {
			note = res.FailureNote
		}
		return scheduler.Outcome{Segment: seg, Encode: res, FailureNote: note, Err: err}
	}
	q, err := e.analyze(segOut, clip)
	if err != nil {
		return scheduler.Outcome{Segment: seg, Encode: res,
			FailureNote: err.Error(), Err: err}
	}
	return scheduler.Outcome{Segment: seg, Encode: res, Quality: q}
}

func (e *Engine) analyze(outPath string, src *media.Video) (*model.QualityResult, error) {
	enc, err := media.ReadEncoded(outPath)
	if err != nil {
		return nil, fmt.Errorf("read encoded output: %w", err)
	}
	encodedVideo := &media.Video{Width: enc.Width, Height: enc.Height,
		FPS: enc.FPS, Frames: enc.Frames}
	frames, err := quality.Clip(src, encodedVideo)
	if err != nil {
		return nil, err
	}
	sum := quality.Summarize(frames)
	return &model.QualityResult{
		FrameCount: sum.FrameCount, MeanPSNRDB: sum.MeanDB,
		MinPSNRDB: sum.MinDB, MaxPSNRDB: sum.MaxDB,
		IdenticalRows: sum.IdenticalPairs, BlackFrames: sum.BlackPairs,
	}, nil
}

func (e *Engine) buildQuality(res *Result) {
	summaries := make([]quality.SegmentSummary, len(res.Task.Segments))
	for i, seg := range res.Task.Segments {
		if seg.Quality != nil {
			summaries[i] = quality.SegmentSummary{
				FrameCount: seg.Quality.FrameCount,
				FiniteCount: seg.Quality.FrameCount -
					seg.Quality.IdenticalRows - seg.Quality.BlackFrames,
				IdenticalPairs: seg.Quality.IdenticalRows,
				BlackPairs:     seg.Quality.BlackFrames,
				MeanDB:         seg.Quality.MeanPSNRDB,
				MinDB:          seg.Quality.MinPSNRDB,
				MaxDB:          seg.Quality.MaxPSNRDB,
			}
		} else {
			summaries[i] = quality.SegmentSummary{}
			summaries[i].MeanDB = math.NaN()
		}
	}
	res.Summaries = summaries
	r := quality.BuildReport(summaries)
	res.Quality = &r
}

// snapshot deep-copies the task segments. It is invoked while the scheduler
// holds its mutex (inside the progress callback), so no worker mutates the
// segments concurrently; the returned value can be marshaled lock-free.
func snapshot(task *model.Task) ([]*model.Segment, error) {
	raw, err := json.Marshal(task.Segments)
	if err != nil {
		return nil, err
	}
	var out []*model.Segment
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// persistProgress runs under the scheduler lock. The snapshot is taken
// atomically here; disk I/O happens on the immutable copy afterward.
func (e *Engine) persistProgress(_ *model.Segment) {
	if e.task == nil {
		return
	}
	segments, err := snapshot(e.task)
	if err != nil {
		return
	}
	cp := &state.Checkpoint{
		JobID: e.task.ID, InputFingerprint: e.task.InputFingerprint,
		Params: e.cfg.Params, Segments: segments,
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	_ = e.store.SaveLocked(e.cpPath, cp)
}

func (e *Engine) checkpoint(task *model.Task) *state.Checkpoint {
	segments, err := snapshot(task)
	if err != nil {
		// Final persist happens after the pool has drained, so a marshal
		// failure would indicate a programming error rather than a race.
		panic(fmt.Errorf("checkpoint snapshot: %w", err))
	}
	return &state.Checkpoint{
		JobID: task.ID, InputFingerprint: task.InputFingerprint,
		Params: e.cfg.Params, Segments: segments,
	}
}

func (e *Engine) persist(task *model.Task) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.store.Save(e.cpPath, e.checkpoint(task))
}
