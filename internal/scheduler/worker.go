// Package scheduler executes segment jobs with bounded concurrency,
// retries, and interruption-safe state transitions.
package scheduler

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"transcode/internal/config"
	"transcode/internal/job"
	"transcode/internal/media"
	"transcode/internal/quality"
	"transcode/internal/rate"
	"transcode/internal/segment"
)

// Runner drives every pending segment to a terminal state.
type Runner struct {
	cfg   config.Config
	info  *media.StreamInfo
	segs  []segment.Segment
	state *job.State
	ctrl  *rate.Controller
	mu    sync.Mutex // serializes state transitions and saves
}

// New wires a Runner from already-planned components.
func New(cfg config.Config, info *media.StreamInfo, segs []segment.Segment, st *job.State, ctrl *rate.Controller) *Runner {
	return &Runner{cfg: cfg, info: info, segs: segs, state: st, ctrl: ctrl}
}

// VerifyReusable requeues succeeded segments whose artifacts vanished,
// so a resume only reuses results that are actually on disk.
func (r *Runner) VerifyReusable() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, seg := range r.state.Segments {
		if seg.Status != job.Succeeded {
			continue
		}
		if _, err := os.Stat(seg.File); err != nil {
			if err := r.state.Transition(i, job.Queued, fmt.Errorf("artifact %s missing, requeued", seg.File)); err != nil {
				return err
			}
		}
	}
	return nil
}

// Run processes all pending segments with at most cfg.Workers
// concurrent ffmpeg subprocesses. Targets are allocated serially in
// segment order (the controller reserves budget per allocation), while
// encoding proceeds concurrently. A cancelled context kills in-flight
// encodes and marks them interrupted; completed segments are kept.
func (r *Runner) Run(ctx context.Context) error {
	pending := r.state.Pending(r.cfg.MaxRetries)
	sem := make(chan struct{}, r.cfg.Workers)
	var wg sync.WaitGroup
	for _, idx := range pending {
		if ctx.Err() != nil {
			break
		}
		target := r.ctrl.Target(idx) // serial, in index order
		r.mu.Lock()
		r.state.Segments[idx].TargetKbps = target
		r.mu.Unlock()
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int, target float64) {
			defer wg.Done()
			defer func() { <-sem }()
			r.runSegment(ctx, idx, target)
		}(idx, target)
	}
	wg.Wait()
	return nil
}

// runSegment attempts one segment up to MaxRetries+1 times.
func (r *Runner) runSegment(ctx context.Context, idx int, target float64) {
	for attempt := 0; attempt <= r.cfg.MaxRetries; attempt++ {
		if ctx.Err() != nil {
			r.transition(idx, job.Interrupted, ctx.Err())
			return
		}
		err := r.encodeOnce(ctx, idx, target)
		if err == nil {
			return
		}
		if ctx.Err() != nil {
			r.transition(idx, job.Interrupted, err)
			return
		}
		r.transition(idx, job.Failed, err)
		st := r.state.Segments[idx]
		if st.Attempts > r.cfg.MaxRetries {
			return
		}
		r.transition(idx, job.Queued, nil)
	}
}

// encodeOnce performs encoding, measurement and quality regression
// for one segment with an already-allocated target.
func (r *Runner) encodeOnce(ctx context.Context, idx int, target float64) error {
	seg := r.segs[idx]
	r.mu.Lock()
	err := r.state.Transition(idx, job.Running, nil)
	r.mu.Unlock()
	if err != nil {
		return err
	}

	final := filepath.Join(r.cfg.OutDir, fmt.Sprintf("seg-%03d.mp4", idx))
	tmp := final + ".tmp.mp4"
	os.Remove(tmp)
	kbps := strconv.FormatFloat(target, 'f', 0, 64)
	args := []string{
		"-v", "error", "-nostdin", "-y",
		"-ss", strconv.FormatFloat(seg.StartSec, 'f', 6, 64),
		"-t", strconv.FormatFloat(seg.Duration(), 'f', 6, 64),
		"-i", r.cfg.Input,
		"-map", "0:v:0", "-c:v", r.cfg.Codec,
		"-b:v", kbps + "k", "-maxrate", kbps + "k",
		"-bufsize", strconv.FormatFloat(target*2, 'f', 0, 64) + "k",
		"-preset", "fast", "-an", tmp,
	}
	if out, err := exec.CommandContext(ctx, "ffmpeg", args...).CombinedOutput(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("encode segment %d: %w: %s", idx, err, tail(out))
	}
	if err := os.Rename(tmp, final); err != nil {
		os.Remove(tmp)
		return err
	}

	actual, err := measureKbps(final, seg.Duration())
	if err != nil {
		return err
	}
	qs, err := quality.Range(ctx, r.cfg.Input, final, seg.StartSec, seg.Duration(), r.info.AvgFPS, r.info.Width, r.info.Height, r.cfg.PSNRFloorDB)
	if err != nil {
		return fmt.Errorf("quality regression: %w", err)
	}
	if ctx.Err() != nil {
		return ctx.Err() // decode pipes were killed by cancellation
	}
	if qs.FrameMismatch {
		return fmt.Errorf("frame count mismatch between reference and segment %d; refusing to fabricate PSNR", idx)
	}
	r.ctrl.Feedback(idx, target, actual)
	r.mu.Lock()
	defer r.mu.Unlock()
	s := &r.state.Segments[idx]
	s.ActualKbps = actual
	s.PSNRMeanDB = qs.MeanDB
	s.PSNRMinDB = qs.MinDB
	s.MaxSwingDB = qs.MaxSwingDB
	s.BelowFloor = qs.BelowFloor
	s.Frames = qs.Frames
	s.File = final
	return r.state.Transition(idx, job.Succeeded, nil)
}

// transition serializes state machine transitions.
func (r *Runner) transition(idx int, to job.Status, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state.Transition(idx, to, err)
}

// measureKbps derives the actual video bitrate from the container's
// stream metadata, falling back to file size when the muxer did not
// record a stream bitrate.
func measureKbps(path string, dur float64) (float64, error) {
	out, err := exec.Command("ffprobe",
		"-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=bit_rate", "-of", "csv=p=0", path).Output()
	if err == nil {
		if v, perr := strconv.ParseFloat(strings.TrimSpace(string(out)), 64); perr == nil && v > 0 {
			return v / 1000, nil
		}
	}
	st, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	if dur <= 0 {
		return 0, fmt.Errorf("non-positive duration for %s", path)
	}
	return float64(st.Size()) * 8 / dur / 1000, nil
}

// tail keeps the last bytes of ffmpeg output for diagnostics.
func tail(b []byte) string {
	const n = 400
	if len(b) > n {
		return string(b[len(b)-n:])
	}
	return string(b)
}
