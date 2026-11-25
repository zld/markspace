package pipeline

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"

	"mediapack/internal/clock"
	"mediapack/internal/fault"
	"mediapack/internal/index"
	"mediapack/internal/recovery"
	"mediapack/internal/report"
	"mediapack/internal/stream"
)

// Options controls one demo invocation.
type Options struct {
	Out             string
	CorruptBoundary bool
	Resume          bool
}

// RunDemo executes the deterministic media packing workflow. The report is
// returned even when a classified verification error is returned.
func RunDemo(ctx context.Context, opts Options) (*report.Report, error) {
	if opts.Out == "" {
		opts.Out = filepath.Join("/tmp", "game-pack")
	}
	fx, err := NewFixture()
	if err != nil {
		return nil, err
	}
	specs, paramsByID, err := fx.Specs()
	if err != nil {
		return nil, err
	}
	identity := IdentityDigest(specs)

	store, err := index.LoadOrCreate(filepath.Join(opts.Out, "index.json"))
	if err != nil {
		return nil, err
	}
	plan := store.Plan(specs)
	decisions := decisionMap(plan)

	cp, err := loadCheckpoint(ctx, opts, fx, identity)
	if err != nil {
		return nil, err
	}

	rep := &report.Report{
		OK:              false,
		Command:         "demo",
		Out:             opts.Out,
		Platform:        "multi-platform",
		InputSHA256:     fx.SourceSHA256,
		FrameSHA256:     index.SHA256Hex(fx.Frame),
		ReusedAssets:    len(plan.Reused),
		RebuiltAssets:   len(plan.Rebuilt),
		ReusedAssetIDs:  decisionIDs(plan.Reused),
		RebuiltAssetIDs: decisionIDs(plan.Rebuilt),
		AssetDecisions:  decisionList(plan),
		PixelMean:       report.PixelMean(fx.Frame),
		PeakDBFS:        report.PeakDBFS(fx.Audio),
		FrameDurationMS: fx.OutputRate.FrameDuration().Milliseconds(),
		TimeRange: report.TimeRange{
			StartTicks: int64(fx.Windows[0].Start),
			EndTicks:   int64(fx.Windows[len(fx.Windows)-1].End),
			StartMS:    fx.Windows[0].Start.Milliseconds(),
			EndMS:      fx.Windows[len(fx.Windows)-1].End.Milliseconds(),
		},
		SubtitleOutputs: map[string]string{
			"srt": "subtitles/game-dialogue.srt",
			"vtt": "subtitles/game-dialogue.vtt",
		},
	}

	if err := prepareSubtitles(fx, opts.Out, specs, decisions, store); err != nil {
		return rep, err
	}

	fan := stream.NewFanout(fx.LayerNames(), SegmentCount)
	for _, w := range fx.Windows {
		frame := stream.Frame{Sequence: w.Index, Segment: w.Index, Start: w.Start, End: w.End, Boundary: true}
		if errs := fan.Broadcast(ctx, frame); len(errs) > 0 {
			return rep, errs[0]
		}
	}

	tolerance := fx.OutputRate.FrameDuration()
	var allSegments []stream.Segment
	var statuses []report.LayerStatus

	for _, spec := range specs {
		if !strings.HasPrefix(spec.ID, "mp4:") {
			continue
		}
		layer := strings.TrimPrefix(spec.ID, "mp4:")
		decision := decisions[spec.ID]
		if decision.Reason == "digests_match" {
			rec := store.Assets[spec.ID]
			st, segs := reusedLayerFromRecord(layer, rec, fx.Windows)
			statuses = append(statuses, st)
			allSegments = append(allSegments, segs...)
			continue
		}

		st, segs, layerErr := buildLayer(ctx, fx, opts, spec.ID, layer, paramsByID[spec.ID], cp, fan, tolerance)
		statuses = append(statuses, st)
		allSegments = append(allSegments, segs...)
		if layerErr != nil {
			_ = cp.MarkFailed(layer, asFault(layerErr))
			continue
		}

		hashes := map[string]string{}
		for _, sh := range st.SegmentHashes {
			hashes[strconv.Itoa(sh.Index)] = sh.SHA256
		}
		store.Commit(index.Record{ID: spec.ID, Key: spec.Key, SegmentHashes: hashes, Outputs: map[string]string{
			"directory": filepath.Join("mp4", strings.Split(layer, "/")[0], strings.Split(layer, "/")[1]),
		}})
		if err := store.Save(); err != nil {
			return rep, err
		}
	}

	rep.Layers = statuses
	rep.SegmentSHA256, rep.SegmentHashes, rep.SegmentSHA256ByID = hashCollections(statuses)
	ver, verifyErr := stream.Verify(fx.LayerNames(), fx.Windows, allSegments, tolerance)
	if ver != nil {
		rep.AlignmentErrorMS = ver.AlignmentErrorMS
		rep.MaxTickError = ver.MaxTickError
		rep.Boundary = &report.BoundaryResult{
			LayerCount:        ver.LayerCount,
			SegmentCount:      ver.SegmentCount,
			ValidatedSegments: ver.ValidatedSegments,
			AlignmentErrorMS:  ver.AlignmentErrorMS,
			MaxTickError:      ver.MaxTickError,
			Passed:            verifyErr == nil,
		}
	}
	if verifyErr != nil {
		fr := failureReport(verifyErr)
		rep.Failure = fr
		if fr.Kind == string(fault.BoundaryMismatch) {
			rep.AlignmentErrorMS = fr.OffsetMS
			var fe *fault.Error
			if errors.As(verifyErr, &fe) {
				rep.MaxTickError = fe.Offset
				if rep.Boundary != nil {
					rep.Boundary.MaxTickError = fe.Offset
					rep.Boundary.AlignmentErrorMS = clock.Ticks(fe.Offset).Milliseconds()
				}
			}
		}
		_ = store.Save()
		return rep, verifyErr
	}

	rep.OK = true
	rep.Published = false
	idempotentReuse := len(plan.Rebuilt) == 0
	if idempotentReuse {
		rep.Version = recovery.CurrentVersion(opts.Out)
	} else {
		rep.Version = recovery.NextVersion(opts.Out)
	}
	pre, err := report.CanonicalBytes(rep)
	if err != nil {
		return rep, err
	}
	rep.ManifestSHA256 = index.SHA256Hex(pre)
	final, err := report.CanonicalBytes(rep)
	if err != nil {
		return rep, err
	}
	if idempotentReuse {
		if err := recovery.WriteCurrent(opts.Out, final); err != nil {
			rep.Failure = failureReport(err)
			return rep, err
		}
	} else if _, err := recovery.Publish(opts.Out, final); err != nil {
		rep.Failure = failureReport(err)
		return rep, err
	}
	if err := index.AtomicWriteFile(filepath.Join(opts.Out, "release-manifest.json"), final, 0o644); err != nil {
		return rep, err
	}
	rep.Published = true
	return rep, nil
}

func buildLayer(
	ctx context.Context,
	fx *Fixture,
	opts Options,
	assetID, layer string,
	params BuildParams,
	cp *recovery.Checkpoint,
	fan *stream.Fanout,
	tolerance clock.Ticks,
) (report.LayerStatus, []stream.Segment, error) {
	start := cp.NextIndex(layer)
	restored := map[int]string{}
	if st := cp.Layers[layer]; st != nil {
		for idx, h := range st.SegmentHashes {
			restored[idx] = h
		}
		if start > 0 {
			if h := st.SegmentHashes[start-1]; h != "" {
				if err := verifyExistingSegmentHash(opts.Out, layer, start-1, h); err != nil {
					return report.LayerStatus{Name: layer, Status: "failed"}, nil, fault.Wrap(fault.DigestMismatch, err, "verify checkpoint segment")
				}
			}
		}
	}

	q := fan.Queue(layer)
	for i := 0; i < start; i++ {
		if _, err := q.Dequeue(ctx); err != nil {
			return report.LayerStatus{Name: layer, Status: "failed"}, nil, err
		}
	}

	var built []builtSegment
	segs := make([]stream.Segment, 0, SegmentCount)
	for idx := 0; idx < start; idx++ {
		segs = append(segs, stream.Segment{
			Layer: layer, Index: idx,
			Start: fx.Windows[idx].Start, End: fx.Windows[idx].End,
			SHA256: restored[idx],
		})
	}
	for idx := start; idx < SegmentCount; idx++ {
		frame, err := q.Dequeue(ctx)
		if err != nil {
			return statusFromSegments(layer, "failed", built, restored), segs, err
		}
		if frame.Segment != idx {
			err := fault.InLayer(fault.Checkpoint, layer, idx+1, int64(idx), "fanout frame index %d != expected %d", frame.Segment, idx)
			return statusFromSegments(layer, "failed", built, restored), segs, err
		}
		bs, err := buildSegment(fx, params, layer, idx, opts.CorruptBoundary)
		if err != nil {
			return statusFromSegments(layer, "failed", built, restored), segs, err
		}
		built = append(built, bs)
		segs = append(segs, bs.segment)
		if err := writeSegment(opts.Out, layer, idx, bs.data); err != nil {
			return statusFromSegments(layer, "failed", built, restored), segs, fault.Wrap(fault.Checkpoint, err, "write segment")
		}
		w := fx.Windows[idx]
		if berr := windowError(layer, idx, w, bs.segment, tolerance); berr != nil {
			st := statusFromSegments(layer, "failed", built, restored)
			fr := failureReport(berr)
			st.Error = fr
			return st, segs, berr
		}
		if err := cp.CommitSegment(layer, idx, bs.hash); err != nil {
			return statusFromSegments(layer, "failed", built, restored), segs, err
		}
	}
	st := statusFromSegments(layer, "complete", built, restored)
	st.Status = "complete"
	return st, segs, nil
}

func windowError(layer string, idx int, w stream.Window, seg stream.Segment, tolerance clock.Ticks) error {
	startErr := absTick(seg.Start - w.Start)
	endErr := absTick(seg.End - w.End)
	worst := startErr
	if endErr > worst {
		worst = endErr
	}
	if worst <= tolerance {
		return nil
	}
	return fault.InLayer(fault.BoundaryMismatch, layer, idx+1, int64(worst),
		"boundary error %.3f ms exceeds one frame %.3f ms", worst.Milliseconds(), tolerance.Milliseconds())
}

func absTick(t clock.Ticks) clock.Ticks {
	if t < 0 {
		return -t
	}
	return t
}

func asFault(err error) *fault.Error {
	var fe *fault.Error
	if errors.As(err, &fe) {
		return fe
	}
	return fault.Wrap(fault.Unknown, err, "pipeline failure")
}
