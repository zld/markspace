// Package pipeline orchestrates probe -> complexity -> allocation -> encode ->
// quality validation -> checkpoint -> report for one video job.
package pipeline

import (
	"context"
	"fmt"
	"path/filepath"

	"bitrack/internal/checkpoint"
	"bitrack/internal/encode"
	"bitrack/internal/media"
	"bitrack/internal/model"
	"bitrack/internal/quality"
	"bitrack/internal/rate"
	"bitrack/internal/task"
)

// Processor implements scheduler.Processor: it owns the bounded closed-loop
// encode for one segment, then validates frame alignment and PSNR.
type Processor struct {
	workDir   string
	inputPath string
	params    model.Params
	store     *checkpoint.Store
	inject    Injector
}

// NewProcessor builds a segment processor. injection is NoopInjector normally.
func NewProcessor(workDir, inputPath string, params model.Params,
	store *checkpoint.Store, inject Injector) *Processor {
	return &Processor{
		workDir: workDir, inputPath: inputPath, params: params,
		store: store, inject: inject,
	}
}

// Process runs the full per-segment pipeline. State moves follow the task
// state machine; every attempt is recorded with reason and outcome.
func (p *Processor) Process(ctx context.Context, seg *model.Segment) error {
	durationSec := float64(seg.FrameCount) / p.params.FrameRate
	ctrl := rate.Controller{
		TargetBits: seg.TargetBits, DurationSec: durationSec,
		TolerancePct: p.params.TolerancePct, MaxIterations: p.params.MaxIterations,
		MinKbps: p.params.MinSegmentKbps, MaxAdjustRatio: 2.0,
	}
	kbps := ctrl.InitialKbps()
	var finalPath string
	for attempt := 1; attempt <= p.params.MaxIterations; attempt++ {
		if err := p.inject.OnEncode(seg); err != nil {
			seg.LastError = err.Error()
			return err
		}
		outPath := filepath.Join(p.workDir, "segments",
			fmt.Sprintf("seg_%03d_attempt_%d.mp4", seg.Index, attempt))
		err := encode.EncodeSegment(ctx, p.inputPath, outPath, encode.Options{
			Width: p.params.Width, Height: p.params.Height,
			StartFrame: seg.StartFrame, FrameCount: seg.FrameCount,
			TargetKbps: kbps, Preset: p.params.Preset,
			Encoder: p.params.Encoder, PixelFormat: p.params.PixelFormat,
		})
		if err != nil {
			seg.Attempts = append(seg.Attempts, model.Attempt{
				Number: attempt, TargetKbps: kbps, Reason: "encode-error: " + truncate(err.Error()),
			})
			return err
		}
		actualBits, merr := encode.VideoPacketBytes(ctx, outPath)
		if merr != nil {
			return merr
		}
		nextKbps, deviation, converged := ctrl.NextKbps(kbps, actualBits)
		reason := "bitrate-correction"
		if converged {
			reason = "within-tolerance"
		}
		if attempt == p.params.MaxIterations {
			reason = "iteration-cap-reached"
		}
		seg.Attempts = append(seg.Attempts, model.Attempt{
			Number: attempt, TargetKbps: kbps, ActualBits: actualBits,
			Deviation: deviation, Reason: reason, OutputPath: outPath,
		})
		finalPath = outPath
		seg.ActualBits = actualBits
		if converged || attempt == p.params.MaxIterations {
			break
		}
		kbps = nextKbps
	}

	// Quality validation: decode aligned frame ranges from both files and
	// compare. Any frame or dimension mismatch is a hard error.
	task.Move(seg, model.StateValidating)
	srcFrames, err := media.DecodeFrames(ctx, p.inputPath,
		p.params.Width, p.params.Height, seg.StartFrame, seg.FrameCount)
	if err != nil {
		return err
	}
	dstFrames, err := media.DecodeFrames(ctx, finalPath,
		p.params.Width, p.params.Height, 0, seg.FrameCount)
	if err != nil {
		return err
	}
	cmp, err := quality.CompareFrames(srcFrames, dstFrames, p.params.Width, p.params.Height)
	if err != nil {
		return err
	}
	seg.PSNRDB = cmp.MeanDB

	if err := p.inject.OnValidate(seg); err != nil {
		seg.LastError = err.Error()
		return err
	}
	// Checksum the durable output and persist it on the segment record.
	sum, err := checkpoint.FileSHA256(finalPath)
	if err != nil {
		return err
	}
	seg.OutputPath = finalPath
	seg.Checksum = sum
	return nil
}

func truncate(s string) string {
	const max = 200
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
