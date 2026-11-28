// Package policy enforces the fixed delivery decision order:
//
//  1. Quality gate  - pixel statistics and loudness thresholds must pass; the
//     kernel never silently lowers quality to fit a budget or size cap.
//  2. Processing budget - when the per-asset millisecond budget is exhausted,
//     the asset is marked incomplete rather than downgraded.
//  3. Delivery size - only after both gates pass is the highest-bitrate
//     representation that fits the byte ceiling selected.
package policy

import (
	"fmt"

	merr "mediacheck/internal/errors"
)

// Decision is the final policy verdict.
type Decision string

const (
	// DecisionAccepted passed all three gates and selected a representation.
	DecisionAccepted Decision = "accepted"
	// DecisionQualityFailed failed the first gate; no downgrade is allowed.
	DecisionQualityFailed Decision = "quality_failed"
	// DecisionIncomplete ran out of processing budget on the second gate.
	DecisionIncomplete Decision = "incomplete"
	// DecisionSizeRejected fit no representation under the byte ceiling.
	DecisionSizeRejected Decision = "size_rejected"
)

// QualityThresholds describes the first-gate requirements.
type QualityThresholds struct {
	// MaxPixelMeanDeviation bounds |observed pixel mean - reference mean|.
	MaxPixelMeanDeviation float64
	// MaxPixelStddevDeviation bounds the same deviation for pixel stddev.
	MaxPixelStddevDeviation float64
	// MinPeakDBFS is the loudness floor in dBFS (more-negative is quieter);
	// observed audio peak must be at least this loud.
	MinPeakDBFS float64
}

// QualityObservation carries measured media statistics for one asset.
type QualityObservation struct {
	PixelMean        float64
	ReferenceMean    float64
	PixelStddev      float64
	ReferenceStddev  float64
	AudioPeakDBFS    float64
}

// Representation is one selectable ladder rung for the third gate.
type Representation struct {
	LayerID       uint32
	Label         string
	Bitrate       int
	EstimatedSize int64 // bytes the remuxed output would occupy
}

// Request bundles every policy input.
type Request struct {
	Quality   QualityObservation
	Threshold QualityThresholds
	// ElapsedMS is processing time already spent on the asset.
	ElapsedMS int64
	// BudgetMS is the per-asset processing budget.
	BudgetMS int64
	// MaxBytes is the delivery size ceiling.
	MaxBytes int64
	Ladder   []Representation
}

// Outcome explains the decision with the failing gate and chosen rung.
type Outcome struct {
	Decision   Decision
	Selected   *Representation
	Reasons    []string
	ErrorCode  string // stable code when rejected, empty on accept
}

// DefaultThresholds returns the gates used by the demo. The synthetic fixture
// is lossless through remux, so zero deviation is the required bar; audio
// must reach a conservative -24 dBFS loudness floor.
func DefaultThresholds() QualityThresholds {
	return QualityThresholds{
		MaxPixelMeanDeviation:   0.0,
		MaxPixelStddevDeviation: 0.0,
		MinPeakDBFS:             -24.0,
	}
}

// Evaluate runs the three gates strictly in order. It never mutates the
// request and never substitutes a lower quality rung to rescue a failed
// quality or budget gate.
func Evaluate(req Request) Outcome {
	// Gate 1: quality. Any failure terminates immediately.
	if reasons, ok := qualityFailures(req.Quality, req.Threshold); !ok {
		return Outcome{
			Decision: DecisionQualityFailed,
			Reasons:  reasons,
			ErrorCode: merr.CodeQualityGate,
		}
	}

	// Gate 2: processing budget. Exhaustion marks the asset incomplete; it
	// must not be quietly delivered at reduced effort.
	if req.BudgetMS > 0 && req.ElapsedMS > req.BudgetMS {
		return Outcome{
			Decision: DecisionIncomplete,
			Reasons: []string{
				fmt.Sprintf("processing budget exhausted: %dms > %dms",
					req.ElapsedMS, req.BudgetMS),
			},
			ErrorCode: merr.CodeBudgetExceeded,
		}
	}

	// Gate 3: size. Pick the highest-bitrate rung that fits. Ladder rungs are
	// not assumed sorted, so scan and remember the best feasible candidate.
	var best *Representation
	for i := range req.Ladder {
		r := req.Ladder[i]
		if req.MaxBytes > 0 && r.EstimatedSize > req.MaxBytes {
			continue
		}
		if best == nil || r.Bitrate > best.Bitrate {
			r := r
			best = &r
		}
	}
	if best == nil {
		return Outcome{
			Decision: DecisionSizeRejected,
			Reasons: []string{
				fmt.Sprintf("no representation fits %d-byte ceiling (%d rungs evaluated)",
					req.MaxBytes, len(req.Ladder)),
			},
			ErrorCode: merr.CodeSizeLimit,
		}
	}
	return Outcome{
		Decision: DecisionAccepted,
		Selected: best,
		Reasons: []string{
			fmt.Sprintf("selected %q at %d bps, estimated %d bytes",
				best.Label, best.Bitrate, best.EstimatedSize),
		},
	}
}

// qualityFailures returns the failing threshold descriptions, if any.
func qualityFailures(q QualityObservation, t QualityThresholds) ([]string, bool) {
	var reasons []string
	if d := absFloat(q.PixelMean - q.ReferenceMean); d > t.MaxPixelMeanDeviation {
		reasons = append(reasons, fmt.Sprintf(
			"pixel mean deviation %.4f exceeds %.4f", d, t.MaxPixelMeanDeviation))
	}
	if d := absFloat(q.PixelStddev - q.ReferenceStddev); d > t.MaxPixelStddevDeviation {
		reasons = append(reasons, fmt.Sprintf(
			"pixel stddev deviation %.4f exceeds %.4f", d, t.MaxPixelStddevDeviation))
	}
	// A missing/impossible peak (positive dBFS sentinel aside) fails the gate.
	if q.AudioPeakDBFS < t.MinPeakDBFS {
		reasons = append(reasons, fmt.Sprintf(
			"audio peak %.2f dBFS below floor %.2f dBFS",
			q.AudioPeakDBFS, t.MinPeakDBFS))
	}
	return reasons, len(reasons) == 0
}

func absFloat(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
