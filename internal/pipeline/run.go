// Package pipeline coordinates the stages: manifest -> unified timeline ->
// bounded stream -> MP4 inspection -> GOP/damage analysis -> checkpoint.
package pipeline

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"time"

	"mediascan/internal/checkpoint"
	"mediascan/internal/manifest"
	"mediascan/internal/model"
	mp4inspect "mediascan/internal/mp4"
	"mediascan/internal/stream"
)

// Run executes the scan. A non-nil error means an argument/unrecoverable
// failure (CLI exit 1); reportable corruption yields ExitCode 2.
func Run(ctx context.Context, cfg Config) (res *Result, err error) {
	start := time.Now()
	memMiB := cfg.MemoryMiB
	if memMiB <= 0 {
		memMiB = 8
	}
	memLimit := int64(memMiB) << 20
	// Soft runtime limit keeps heap growth aligned with the user budget; the
	// hard buffer budget below is what actually blocks producers.
	prevLimit := debug.SetMemoryLimit(memLimit)
	defer debug.SetMemoryLimit(prevLimit)
	budget := stream.NewBudget(memLimit / 2)
	chunk := 64 << 10
	if int64(chunk) > budget.Max() {
		chunk = int(budget.Max())
	}
	mon := StartMemMonitor(budget)

	res = &Result{PrimaryOffset: model.Unknown, PrimaryTime: model.Unknown}
	defer func() {
		peak := mon.Stop()
		if res != nil {
			res.MemoryPeakBytes = peak
		}
	}()
	defer func() {
		if res != nil {
			res.ElapsedMS = time.Since(start).Milliseconds()
		}
	}()

	absInput, err := filepath.Abs(cfg.Input)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(absInput); err != nil {
		return nil, fmt.Errorf("input: %w", err)
	}
	root := cfg.MediaDir
	if root == "" {
		root = filepath.Dir(absInput)
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}

	manifestBytes, err := os.ReadFile(absInput)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	keySum := sha256.Sum256(manifestBytes)
	manifestKey := hex.EncodeToString(keySum[:])

	rep := &model.Report{UnifiedTimescale: model.UnifiedTimescale}
	var items []workItem
	var manifestFaults []model.Fault

	if filepath.Ext(absInput) == ".mpd" {
		d, perr := manifest.ParseDASH(bytes.NewReader(manifestBytes))
		if perr != nil {
			return nil, perr
		}
		rep.InputKind = "dash"
		for _, r := range d.Representations {
			rep.RepresentationIDs = append(rep.RepresentationIDs, r.ID)
		}
		manifestFaults = append(manifestFaults, manifest.AlignRepresentations(d.Representations, 2)...)
		chosen := d.Representations[0]
		for _, s := range chosen.Segments {
			items = append(items, workItem{
				URI: s.URI, InitURI: chosen.InitURI,
				GlobalStart: s.StartTicks, ManifestDuration: s.DurationTicks,
			})
		}
	} else {
		h, perr := manifest.ParseHLS(bytes.NewReader(manifestBytes))
		if perr != nil {
			return nil, perr
		}
		rep.InputKind = "hls"
		if h.IsMaster && len(h.Variants) > 0 {
			h, err = followBestVariant(rootAbs, h)
			if err != nil {
				return nil, err
			}
		}
		for _, s := range h.Segments {
			items = append(items, workItem{
				URI: s.URI, InitURI: s.MapURI, Period: s.Period,
				GlobalStart: s.GlobalStartTicks, ManifestDuration: s.DurationTicks,
			})
		}
	}
	rep.Input = manifest.RelativeURI(rootAbs, absInput)

	var store *checkpoint.Store
	if cfg.CheckpointPath != "" {
		store, err = checkpoint.Open(cfg.CheckpointPath)
		if err != nil {
			return nil, err
		}
	}
	state := &checkpoint.State{ManifestKey: manifestKey, TotalSegments: len(items)}
	startIdx := 0
	if store != nil {
		if cfg.Resume {
			lr, lerr := store.Load()
			if lerr != nil {
				return nil, lerr
			}
			res.CheckpointSource = lr.Source
			res.RollbackReason = lr.RollbackReason
			if lr.State.ManifestKey == manifestKey {
				state = lr.State
				startIdx = state.CompletedSegments
				res.ResumeFromSample = state.CommittedSamples
			} else if res.RollbackReason == "" {
				res.RollbackReason = "checkpoint belongs to a different input; starting at safe boundary 0"
			} else {
				res.RollbackReason += "; checkpoint belongs to a different input; starting at safe boundary 0"
			}
		} else {
			store.Remove()
		}
	}
	state.ManifestKey = manifestKey
	state.TotalSegments = len(items)

	if cfg.Resume && state.Complete && startIdx >= len(items) {
		rep.Segments = state.Segments
		finalizeReport(rep, manifestFaults, res)
		res.Report = rep
		res.ExitCode = exitCodeFor(rep)
		res.DriftPPM = driftFromSegments(rep.Segments)
		res.WrapEvents = wrapFromSegments(rep.Segments)
		return res, nil
	}

	rn := &runner{cfg: cfg, root: rootAbs, budget: budget, chunk: chunk,
		initRes: map[string]*mp4inspect.Result{}}

	for i := startIdx; i < len(items); i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		sr, ierr := rn.processItem(ctx, items[i])
		if ierr != nil {
			return nil, ierr
		}
		state.Segments = append(state.Segments, sr)
		state.CompletedSegments = i + 1
		state.CommittedSamples += sr.SampleCount
		if store != nil {
			state.TotalSegments = len(items)
			if serr := store.Save(state); serr != nil {
				return nil, serr
			}
		}
		if cfg.InterruptAfterSegments > 0 && (i-startIdx+1) >= cfg.InterruptAfterSegments {
			return res, ErrInterrupted
		}
	}

	rep.Segments = state.Segments
	finalizeReport(rep, manifestFaults, res)
	ppm, wraps := rn.finalizeDrift(rep.Segments)
	res.DriftPPM = ppm
	res.WrapEvents = wraps
	if store != nil {
		state.Complete = true
		state.ResultSHA256 = rep.SHA256
		_ = store.Save(state)
	}
	res.Report = rep
	res.ExitCode = exitCodeFor(rep)
	return res, nil
}
