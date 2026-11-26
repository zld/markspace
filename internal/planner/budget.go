// Package planner estimates duration, bitrate and processing cost for a
// slice candidate and rejects over-budget candidates with reasons before
// any worker is started.
package planner

import (
	"fmt"
	"math"
	"math/bits"
	"strings"
	"time"

	"replay/internal/audio"
	"replay/internal/events"
	"replay/internal/model"
	"replay/internal/video"
)

// Fixed delivery parameters used by the cost model.
const (
	AudioBitrate    = 128000
	MinVideoBitrate = 100000
)

// Limits bound what a candidate may cost. A candidate violating any limit
// is rejected with an explainable reason.
type Limits struct {
	MaxWindow    time.Duration
	MinBitrate   int64
	MaxBitrate   int64
	MaxEstBytes  int64
	MaxCostUnits int64
}

// DefaultLimits returns the delivery budget for match clips.
func DefaultLimits() Limits {
	return Limits{
		MaxWindow:    5 * time.Minute,
		MinBitrate:   200_000,
		MaxBitrate:   20_000_000,
		MaxEstBytes:  512 << 20,
		MaxCostUnits: 1_000_000,
	}
}

// Request describes one slice candidate.
type Request struct {
	Window        events.Window
	TargetBitrate int64
	AudioIn       model.AudioInfo
	ColorIn       *model.ColorInfo
}

// Candidate is an accepted, fully-costed slice plan.
type Candidate struct {
	Duration     time.Duration
	VideoBitrate int64
	AudioBitrate int64
	EstBytes     int64
	CostUnits    int64
	Audio        audio.Plan
	Color        video.Plan
}

// Evaluate estimates duration, bitrate and cost for req. When the
// candidate violates the budget it returns nil plus the list of rejection
// reasons; callers must not start workers in that case.
func Evaluate(req Request, lim Limits, rep *model.Report) (*Candidate, []string) {
	var reasons []string

	ap, err := audio.PlanAudio(req.AudioIn)
	if err != nil {
		rep.Addf(model.CodeUnsupportedAudio, model.SevWarn, "", -1, "%s", err)
		reasons = append(reasons, err.Error())
	}
	cp := video.PlanColor(req.ColorIn, rep)

	dur := req.Window.Dur()
	if dur <= 0 {
		reasons = append(reasons, "window is empty")
	}
	if dur > lim.MaxWindow {
		reasons = append(reasons, fmt.Sprintf("window %v exceeds max %v", dur, lim.MaxWindow))
	}
	if req.TargetBitrate < lim.MinBitrate {
		reasons = append(reasons, fmt.Sprintf("target bitrate %d below min %d", req.TargetBitrate, lim.MinBitrate))
	}
	if req.TargetBitrate > lim.MaxBitrate {
		reasons = append(reasons, fmt.Sprintf("target bitrate %d above max %d", req.TargetBitrate, lim.MaxBitrate))
	}
	vb := req.TargetBitrate - AudioBitrate
	if req.TargetBitrate > 0 && vb < MinVideoBitrate {
		reasons = append(reasons, fmt.Sprintf(
			"video bitrate %d below min %d after reserving %d audio", vb, MinVideoBitrate, AudioBitrate))
	}
	est := mulDivSat(req.TargetBitrate/8, int64(dur), int64(time.Second))
	if est > lim.MaxEstBytes {
		reasons = append(reasons, fmt.Sprintf("estimated size %d bytes exceeds max %d", est, lim.MaxEstBytes))
	}
	// Cost model: millisecond-seconds of output scaled by bitrate class.
	cost := mulDivSat(int64(dur/time.Millisecond), req.TargetBitrate, 1_000_000)
	if cost > lim.MaxCostUnits {
		reasons = append(reasons, fmt.Sprintf("estimated cost %d units exceeds max %d", cost, lim.MaxCostUnits))
	}
	if len(reasons) > 0 {
		rep.Addf(model.CodeBudgetExceeded, model.SevWarn, "", -1,
			"candidate rejected: %s", strings.Join(reasons, "; "))
		return nil, reasons
	}
	return &Candidate{
		Duration:     dur,
		VideoBitrate: vb,
		AudioBitrate: AudioBitrate,
		EstBytes:     est,
		CostUnits:    cost,
		Audio:        ap,
		Color:        cp,
	}, nil
}

// mulDivSat computes non-negative a*b/div without overflowing int64. If the
// exact quotient cannot fit in int64 (or the intermediate product wraps
// uint64), it saturates to math.MaxInt64, which is always over any practical
// budget. Negative factors are treated as zero.
func mulDivSat(a, b, div int64) int64 {
	if div <= 0 {
		return 0
	}
	if a < 0 || b < 0 {
		return 0
	}
	hi, lo := bits.Mul64(uint64(a), uint64(b))
	if hi != 0 {
		return math.MaxInt64
	}
	q := lo / uint64(div)
	if q > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(q)
}
