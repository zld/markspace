// Package sync aligns audio, video and subtitle tracks onto one shared time
// base, estimates clock drift from paired observations, and rejects silent
// subtitle shifts that would mask real audio/video desync.
package sync

import (
	"errors"
	"fmt"
	"math"
)

// Observation pairs a media timestamp (RTP-derived nanoseconds) with the
// corresponding shared-base position. Anchor points come from the first valid
// audio sample, the first video frame and subtitle interval boundaries.
type Observation struct {
	MediaNS  int64 // timestamp as carried by the source track
	SharedNS int64 // expected position on the shared base
	Weight   float64
}

// DriftModel is the fitted affine mapping shared = slope*media + intercept,
// where slope = 1 + driftPPM/1e6.
type DriftModel struct {
	InterceptNS int64
	Slope       float64
	DriftPPM    float64
	ResidualNS  float64 // RMS residual of the fit
	N           int
}

// ErrInsufficientPoints is returned when drift cannot be estimated.
var ErrInsufficientPoints = errors.New("need at least two distinct observations to estimate drift")

// FitDrift runs weighted ordinary least squares on media/shared pairs.
// All arithmetic is float64 on nanosecond magnitudes; inputs are synthetic and
// bounded so precision loss stays well below one nanosecond relative error.
func FitDrift(obs []Observation) (DriftModel, error) {
	if len(obs) < 2 {
		return DriftModel{}, ErrInsufficientPoints
	}
	var sw, sx, sy, sxx, sxy float64
	for _, o := range obs {
		w := o.Weight
		if w <= 0 {
			w = 1
		}
		x, y := float64(o.MediaNS), float64(o.SharedNS)
		sw += w
		sx += w * x
		sy += w * y
		sxx += w * x * x
		sxy += w * x * y
	}
	den := sw*sxx - sx*sx
	if math.Abs(den) < 1 {
		return DriftModel{}, fmt.Errorf("%w: observations collinear", ErrInsufficientPoints)
	}
	slope := (sw*sxy - sx*sy) / den
	intercept := (sy - slope*sx) / sw
	var sq float64
	for _, o := range obs {
		pred := slope*float64(o.MediaNS) + intercept
		r := pred - float64(o.SharedNS)
		sq += r * r
	}
	return DriftModel{
		InterceptNS: int64(math.Round(intercept)),
		Slope:       slope,
		DriftPPM:    (slope - 1) * 1_000_000,
		ResidualNS:  math.Sqrt(sq / float64(len(obs))),
		N:           len(obs),
	}, nil
}

// Apply maps a source-track nanosecond position through the drift model.
func (m DriftModel) Apply(mediaNS int64) int64 {
	return int64(math.Round(m.Slope*float64(mediaNS) + float64(m.InterceptNS)))
}
