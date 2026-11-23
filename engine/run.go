package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"mediad/manifest"
	"mediad/render"
	"mediad/stream"
	"mediad/timeline"
)

// Run executes the full data flow with locally generated fixtures only.
func Run(ctx context.Context, cfg Config) (*Report, error) {
	if err := validate(cfg); err != nil {
		return nil, err
	}
	framesN := int(cfg.Duration.Seconds() * float64(cfg.FPS))
	intervalNS := int64(1_000_000_000 / cfg.FPS)
	durationNS := int64(framesN) * intervalNS
	frameBytes := render.PlanBitrate(cfg.TargetBitrate, cfg.FPS)

	tracks, err := generateTracks(cfg, framesN, frameBytes)
	if err != nil {
		return nil, err // truncation surfaces here (parse-stage propagation)
	}
	tl, err := timeline.Merge(tracks, 0, durationNS+intervalNS)
	if err != nil {
		return nil, err
	}
	return streamAndRender(ctx, cfg, tl, framesN, intervalNS, durationNS, frameBytes)
}

func streamAndRender(ctx context.Context, cfg Config, tl *timeline.Timeline,
	framesN int, intervalNS, durationNS int64, frameBytes int) (*Report, error) {

	cpPath := filepath.Join(cfg.OutputDir, "checkpoint.json")
	store, err := stream.NewFileStore(cpPath)
	if err != nil {
		return nil, err
	}
	startCP, hadCP, err := store.Load()
	if err != nil {
		return nil, err
	}
	switch {
	case !cfg.Recover && hadCP:
		_ = os.Remove(cpPath)
		store, _ = stream.NewFileStore(cpPath)
		startCP = stream.Checkpoint{}
	case cfg.Recover && !hadCP:
		interruptedAt := int64(framesN / 3)
		if err := store.Save(stream.Checkpoint{
			NextIndex: interruptedAt, TimeNS: interruptedAt * intervalNS,
			Completed: interruptedAt,
		}); err != nil {
			return nil, err
		}
		startCP, _, _ = store.Load()
	}

	maxSample := frameBytes + 1024
	q := stream.NewBoundedQueue(cfg.QueueCapacity, maxSample)
	p := stream.NewPipeline(q, store)
	gen := render.NewGenerator(tl)
	badEvents, firstBad := scanBad(tl)

	builder := manifest.NewBuilder(cfg.FPS, cfg.TargetBitrate, tl)
	var meanSum, varSum float64
	var totalBytes int64
	var badCount int64

	renderFrame := func(index int64) error {
		tns := index * intervalNS
		f := gen.Render(index, tns, badEvents, firstBad)
		m := render.Measure(f, frameBytes, cfg.TargetBitrate, intervalNS)
		if err := builder.AddFrame(m); err != nil {
			return err
		}
		meanSum += m.Quality.Mean
		varSum += m.Quality.Variance
		totalBytes += int64(m.Bytes)
		if m.Bad {
			badCount++
			builder.AddError(manifest.ErrorRecord{TrackID: 1, TimeNS: m.TimeNS,
				Kind: string(timeline.KindVideo), Reason: "corrupt keyframe data"})
		}
		return nil
	}
	for i := int64(0); i < startCP.NextIndex; i++ {
		if err := renderFrame(i); err != nil {
			return nil, err
		}
	}

	producer := func(ctx context.Context, q *stream.BoundedQueue) error {
		for i := startCP.NextIndex; i < int64(framesN); i++ {
			s := stream.Sample{Index: i, TrackID: 1, TimeNS: i * intervalNS,
				Payload: make([]byte, frameBytes)}
			if err := p.Produce(ctx, s); err != nil {
				return err
			}
		}
		q.Close()
		return nil
	}
	consume := func(ctx context.Context, s stream.Sample) error {
		if cfg.ConsumerDelay > 0 {
			select {
			case <-time.After(cfg.ConsumerDelay):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return renderFrame(s.Index)
	}
	cp, err := p.Run(ctx, producer, consume)
	if err != nil {
		return nil, err
	}
	rep := render.AggregateBitrate(totalBytes, durationNS, cfg.TargetBitrate)
	m := builder.Finalize(durationNS, rep)
	mpath, err := manifest.Write(cfg.OutputDir, m)
	if err != nil {
		return nil, err
	}
	if err := manifest.Verify(manifest.Inputs{Timeline: tl, Manifest: m, FrameCount: framesN}); err != nil {
		return nil, err
	}
	n := len(m.Frames)
	if badCount != int64(len(m.Errors)) {
		return nil, fmt.Errorf("engine: bad frame count %d disagrees with manifest errors %d",
			badCount, len(m.Errors))
	}
	r := &Report{
		Frames: n, TargetBPS: m.TargetBPS, ActualBPS: m.ActualBPS, BitrateDeltaPct: m.DeltaPct,
		QualityMean: meanSum / float64(n), QualityVariance: varSum / float64(n),
		ManifestMaxErr: m.MaxErrorNS, ManifestSHA: m.SHA256, Errors: m.Errors,
		FirstBadTimeNS: firstBad, ManifestPath: mpath, State: m.State,
		QueueCapacity: q.Cap(), MemoryCeiling: q.MemoryCeilingBytes(FixedWorkingBuffer),
	}
	if cfg.Recover {
		r.RecoveredFrames = cp.NextIndex - startCP.NextIndex
	}
	return r, nil
}
