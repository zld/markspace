package pipeline

import (
	"mediascan/internal/clock"
)

// DriftObservation is one reference/source clock pair used for fitting.
type DriftObservation struct {
	// RefSeconds is the trusted manifest time of the segment boundary.
	RefSeconds float64
	// SourceTick is the expanded source tick (tfdt base decode time) at
	// the same boundary.
	SourceTick int64
}

// DriftFit is the multi-point least-squares result.
type DriftFit struct {
	EstimatedPPM float64 `json:"estimated_ppm"`
	AppliedPPM   float64 `json:"applied_ppm"`
	// RSquared is the fit goodness; below DriftFitQualityThreshold the
	// estimate is considered unreliable and reported as zero.
	RSquared float64 `json:"r_squared"`
	// Trusted says whether EstimatedPPM passed the quality gate.
	Trusted bool `json:"trusted"`
	// ResidualMicros is the post-correction residual per observation.
	ResidualMicros []int64 `json:"residual_micros"`
	Observations   int     `json:"observations"`
}

// DriftFitQualityThreshold is the minimum R-squared for an estimate to be
// treated as a reliable clock characterization.
const DriftFitQualityThreshold = 0.999

// EstimateDrift fits source drift from multiple observation points and
// records the estimate together with the correction that was applied.
// The timescale is the source tick rate; fewer than two points yield a
// zero estimate and the configured correction is left untouched.
func (p *Pipeline) EstimateDrift(timescale uint32, obs []DriftObservation) DriftFit {
	fit := DriftFit{AppliedPPM: p.DriftPPM, Observations: len(obs)}
	if len(obs) < 2 || timescale == 0 {
		return fit
	}
	est := clock.NewDriftEstimator(timescale)
	for _, o := range obs {
		est.Add(clock.Observation{
			RefSeconds: clock.NewRat(int64(o.RefSeconds*1e6), 1_000_000),
			Tick:       o.SourceTick,
		})
	}
	ppm, r2 := est.Fit()
	fit.RSquared = r2
	if r2 >= DriftFitQualityThreshold {
		fit.EstimatedPPM = ppm
		fit.Trusted = true
	}
	fit.ResidualMicros = est.Residuals(fit.AppliedPPM)
	return fit
}

// BuildObservations pairs each segment whose moof/tfdt was parsed (even
// if its mdat payload was truncated) with its running reference position
// in seconds. Segments with no recoverable base time, or absent inputs,
// terminate the contiguous fit window so an unmodeled gap cannot bias
// the slope. The reference span prefers actual sample durations (VFR
// correct) and falls back to the manifest duration when samples were
// lost to truncation.
func BuildObservations(parsed []*ParsedSegment, timescale uint32) []DriftObservation {
	var out []DriftObservation
	ref := 0.0
	for _, ps := range parsed {
		if ps == nil || ps.MP4 == nil {
			break
		}
		out = append(out, DriftObservation{
			RefSeconds: ref,
			SourceTick: int64(ps.MP4.BaseDTS),
		})
		switch {
		case len(ps.MP4.Samples) > 0:
			last := ps.MP4.Samples[len(ps.MP4.Samples)-1]
			span := int64(last.DTS) + int64(last.Duration) - int64(ps.MP4.BaseDTS)
			if span < 0 {
				span = 0
			}
			ref += float64(span) / float64(timescale)
		case ps.Input.DurationSeconds > 0:
			ref += ps.Input.DurationSeconds
		}
	}
	return out
}
