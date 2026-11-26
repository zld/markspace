package plan

import (
	"context"
	"fmt"

	"regress/internal/fault"
	"regress/internal/scan"
)

// EncoderOutput is one trial encode's produced material.
type EncoderOutput struct {
	Path        string // transcoded elementary stream
	OutputBytes int64
	FrameCount  int
	// SegmentBytes[i] is the byte count of segment i, aligned to the shared
	// keyframe table.
	SegmentBytes []int64
}

// TrialEncoder is the process boundary to FFmpeg. The worker package supplies
// the production implementation; tests inject a deterministic fake.
type TrialEncoder interface {
	// Encode runs one candidate and returns its output descriptor.
	Encode(ctx context.Context, codec string, c Candidate) (*EncoderOutput, error)
	// MeasureSSIM re-decodes source and candidate and returns mean SSIM in
	// [0,1].
	MeasureSSIM(ctx context.Context, codec string, out *EncoderOutput) (float64, error)
}

// Planner evaluates a fixed candidate ladder under hard constraints.
type Planner struct {
	encoder     TrialEncoder
	codec       string
	scan        *scan.ScanResult
	constraints Constraints
	segments    *SegmentPlan
}

// NewPlanner wires the planner. Timescale defaults to the supplied source
// clock denominator.
func NewPlanner(enc TrialEncoder, codec string, res *scan.ScanResult, con Constraints) *Planner {
	if con.Timescale == 0 {
		con.Timescale = 24
	}
	return &Planner{
		encoder:     enc,
		codec:       codec,
		scan:        res,
		constraints: con,
		segments:    BuildSegmentPlan(res, con.Timescale),
	}
}

// Decision is the full, explainable outcome of a planning run.
type Decision struct {
	Selected   *Candidate
	Metric     *Metric
	ConfigHash string
	Results    []CandidateResult
	Segments   *SegmentPlan
}

// Run evaluates every rung in order. It returns a Decision even when nothing
// is feasible; the caller must inspect Selected and treat a nil selection plus
// a fault.CodeBudgetUnsatisfiable error as "do not publish".
func (p *Planner) Run(ctx context.Context) (*Decision, error) {
	decision := &Decision{Segments: p.segments}
	width, height := baseDimensions(p.scan)
	ladder := Ladder(width, height)

	var budgetErrs []error
	for _, c := range ladder {
		if ctx.Err() != nil {
			return nil, fault.New(fault.StagePlan, fault.CodeSubprocessTimeout, p.codec,
				"planning canceled").Wrap(ctx.Err())
		}
		out, err := p.encoder.Encode(ctx, p.codec, c)
		if err != nil {
			return nil, err
		}
		ssim, err := p.encoder.MeasureSSIM(ctx, p.codec, out)
		if err != nil {
			return nil, err
		}
		metric := p.measure(c, out, ssim)
		rejects := Evaluate(c, metric, p.constraints)
		result := CandidateResult{Candidate: c, Metric: metric, Rejects: rejects, Accepted: len(rejects) == 0}
		decision.Results = append(decision.Results, result)
		for _, rj := range rejects {
			budgetErrs = append(budgetErrs, fault.New(fault.StagePlan, fault.Code(rj.Code), p.codec,
				fmt.Sprintf("candidate %d (%s): %s", c.ID, c.Label, rj.Reason)))
		}
		if result.Accepted && decision.Selected == nil {
			sel := c
			decision.Selected = &sel
			m := metric
			decision.Metric = &m
			decision.ConfigHash = ConfigHash(p.codec, c, p.constraints, len(p.segments.KeyframeOrdinals))
		}
	}
	if decision.Selected == nil {
		return decision, fault.New(fault.StagePlan, fault.CodeBudgetUnsatisfiable, p.codec,
			"no candidate satisfied quality, bitrate and work constraints").
			Wrap(fault.Aggregate(budgetErrs))
	}
	return decision, nil
}

// measure computes the deterministic metric triple for one finished trial.
func (p *Planner) measure(c Candidate, out *EncoderOutput, ssim float64) Metric {
	m := Metric{
		SSIM:        ssim,
		OutputBytes: out.OutputBytes,
		WorkUnits:   DeterministicWork(c, out.FrameCount, out.OutputBytes),
	}
	peak := int64(0)
	for i, bytes := range out.SegmentBytes {
		if i >= len(p.segments.FramesPerSegment) {
			break
		}
		dur := float64(p.segments.FramesPerSegment[i]) / float64(p.segments.TimeDen)
		if dur <= 0 {
			continue
		}
		bps := int64(float64(bytes*8) / dur)
		if bps > peak {
			peak = bps
		}
	}
	m.BitratePeakBPS = peak
	return m
}

// baseDimensions returns the source frame dimensions carried by the scan's
// first keyframe configuration. The encoder knows the real dimensions; the
// planner only needs a stable ladder basis, so it derives values from the
// supplied stream spec when the bitstream lacks parsed SPS geometry.
func baseDimensions(res *scan.ScanResult) (int, int) {
	if w, h, ok := parsedDimensions(res); ok {
		return w, h
	}
	return 176, 144
}
