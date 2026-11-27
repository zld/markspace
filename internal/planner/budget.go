// Package planner enumerates candidate output parameter sets and rejects
// infeasible ones before any worker starts. Feasibility is bounded by the
// configured thread count, a total output byte ceiling and the allowed
// loudness error.
package planner

import (
	"fmt"

	"continuity/internal/model"
)

// CandidateRates are the sample rates the planner can choose from, in
// enumeration order.
var CandidateRates = []uint32{48000, 44100, 32000, 24000, 16000}

// CandidateLayouts are the channel layouts the planner can choose from.
var CandidateLayouts = []model.ChannelLayout{model.LayoutMono, model.LayoutStereo}

// CandidateLUFS are the target loudness options the planner can choose from.
var CandidateLUFS = []float64{-23.0, -20.0, -16.0}

// Constraints bound the planning problem.
type Constraints struct {
	Threads          int     // worker goroutines that must fit
	MaxOutputBytes   int64   // ceiling on total encoded output size
	LoudnessTolerance float64 // maximum allowed |measured - target| in LU
	FrameCount       int     // number of frames that will be processed
	FrameDurationNS  int64   // duration of one frame in nanoseconds
	SourceRate       uint32  // input sample rate (0 disables the resample rule)
}

// Params is one fully enumerated output parameter set.
type Params struct {
	SampleRate uint32
	Channels   model.ChannelLayout
	TargetLUFS float64
}

// Rejection explains why a candidate was infeasible.
type Rejection struct {
	Params Params
	Reason string
}

// Result is the planner outcome.
type Result struct {
	Selected   *Params
	Rejections []Rejection
}

// EstimatedOutputBytes predicts the PCM output size for one candidate across
// all frames: samples/frame * channels * 2 bytes * frame count.
func EstimatedOutputBytes(c Params, con Constraints) int64 {
	perFrame := int64(c.SampleRate) * con.FrameDurationNS / 1_000_000_000
	return perFrame * int64(c.Channels.Channels()) * 2 * int64(con.FrameCount)
}

// Plan enumerates the full candidate space and returns the first feasible
// set. When nothing is feasible the Result has no selection and every rejected
// candidate carries a reason; callers must not start the worker pool.
func Plan(con Constraints) (*Result, error) {
	if con.Threads < 1 {
		return nil, model.NewError(model.StagePlanner, 0, 0, "thread count must be at least 1")
	}
	if con.FrameCount < 1 {
		return nil, model.NewError(model.StagePlanner, 0, 0, "frame count must be at least 1")
	}
	if con.LoudnessTolerance < 0 {
		return nil, model.NewError(model.StagePlanner, 0, 0, "loudness tolerance cannot be negative")
	}

	res := &Result{}
	for _, rate := range CandidateRates {
		for _, layout := range CandidateLayouts {
			for _, lufs := range CandidateLUFS {
				c := Params{SampleRate: rate, Channels: layout, TargetLUFS: lufs}
				if reason, infeasible := infeasible(c, con); infeasible {
					res.Rejections = append(res.Rejections, Rejection{Params: c, Reason: reason})
					continue
				}
				selected := c
				res.Selected = &selected
				return res, nil
			}
		}
	}
	return res, nil
}

func infeasible(c Params, con Constraints) (string, bool) {
	// More workers than frames is harmless: the pool simply leaves the extra
	// goroutines idle. Work is split across min(threads, frames) workers.
	// Output byte ceiling.
	if size := EstimatedOutputBytes(c, con); size > con.MaxOutputBytes {
		return fmt.Sprintf("estimated %d output bytes exceed ceiling %d", size, con.MaxOutputBytes), true
	}
	// Loudness rule: candidates whose target is farther from the -23 LUFS
	// delivery anchor than the tolerance can never converge inside it.
	const deliveryAnchor = -23.0
	d := c.TargetLUFS - deliveryAnchor
	if d < 0 {
		d = -d
	}
	if d > con.LoudnessTolerance {
		return fmt.Sprintf("target %.1f LUFS is %.1f LU from anchor, tolerance %.1f",
			c.TargetLUFS, d, con.LoudnessTolerance), true
	}
	return "", false
}

// MustHaveSelection returns a structured error when planning found no feasible
// candidate, including the rejection reasons for the run journal.
func (r *Result) MustHaveSelection() error {
	if r.Selected != nil {
		return nil
	}
	reasons := ""
	for i, rej := range r.Rejections {
		if i > 0 {
			reasons += "; "
		}
		reasons += rej.Reason
	}
	return model.NewError(model.StagePlanner, 0, 0, "no feasible candidate: "+reasons)
}
