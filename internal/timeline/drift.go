package timeline

import (
	"fmt"
	"math"
)

// DriftReport is the least-squares linear fit between a track's normalised
// timestamps and a reference timebase: corrected = slope*t0 + intercept.
type DriftReport struct {
	TrackNumber uint64
	Samples     int
	Slope       float64 // 1.0 means no drift, ppm = (slope-1)*1e6
	Intercept   int64   // nanoseconds
	PPM         float64
	MaxResidual int64 // nanoseconds
}

// DriftEstimator accumulates (trackT0, referenceT0) samples in one streaming
// pass using running sums, then fits a line. Memory is O(1).
type DriftEstimator struct {
	track    uint64
	limitPPM float64
	n        float64
	sumX     float64
	sumY     float64
	sumXX    float64
	sumXY    float64
	maxRes   int64
}

// NewDriftEstimator rejects drift whose magnitude exceeds limitPPM.
func NewDriftEstimator(trackNumber uint64, limitPPM float64) *DriftEstimator {
	return &DriftEstimator{track: trackNumber, limitPPM: limitPPM}
}

// Add pairs one corrected track timestamp with its reference timestamp.
func (d *DriftEstimator) Add(trackT0, refT0 int64) {
	x, y := float64(trackT0), float64(refT0)
	d.n++
	d.sumX += x
	d.sumY += y
	d.sumXX += x * x
	d.sumXY += x * y
}

// Fit computes the linear model. Fewer than two samples mean an identity fit.
func (d *DriftEstimator) Fit() (DriftReport, error) {
	rep := DriftReport{TrackNumber: d.track, Slope: 1, Samples: int(d.n)}
	if d.n < 2 {
		return rep, nil
	}
	den := d.n*d.sumXX - d.sumX*d.sumX
	if math.Abs(den) < 1e-9 {
		return rep, fmt.Errorf("timeline: degenerate drift fit for track %d (zero time span)", d.track)
	}
	rep.Slope = (d.n*d.sumXY - d.sumX*d.sumY) / den
	rep.Intercept = int64((d.sumY - rep.Slope*d.sumX) / d.n)
	rep.PPM = (rep.Slope - 1) * 1e6
	rep.MaxResidual = d.maxRes
	if math.Abs(rep.PPM) > d.limitPPM {
		return rep, &DriftError{Report: rep, LimitPPM: d.limitPPM}
	}
	return rep, nil
}

// DriftError fails the run closed when fitted drift exceeds the threshold.
type DriftError struct {
	Report  DriftReport
	LimitPPM float64
}

func (e *DriftError) Error() string {
	return fmt.Sprintf("timeline: track %d drift %.1f ppm exceeds limit %.1f ppm (max residual %d ns)",
		e.Report.TrackNumber, e.Report.PPM, e.LimitPPM, e.Report.MaxResidual)
}

// Corrector applies a DriftReport while keeping the *relative* spacing of a
// track's own frames: every frame is mapped through the same affine model.
type Corrector struct {
	slope     float64
	intercept float64
}

// NewCorrector builds a corrector from a fitted report.
func NewCorrector(rep DriftReport) *Corrector {
	return &Corrector{slope: rep.Slope, intercept: float64(rep.Intercept)}
}

// Apply maps one nanosecond timestamp onto the corrected timebase.
func (c *Corrector) Apply(t0 int64) int64 {
	return int64(math.Round(float64(t0)*c.slope + c.intercept))
}

// CorrectFrames returns corrected frames. VFR spacing is preserved because
// the affine map is monotonic; a non-monotonic slope is a hard failure.
func CorrectFrames(frames []Frame, rep DriftReport) ([]Frame, error) {
	if rep.Slope <= 0 {
		return nil, fmt.Errorf("timeline: non-monotonic correction slope %.6f for track %d", rep.Slope, rep.TrackNumber)
	}
	cor := NewCorrector(rep)
	out := make([]Frame, len(frames))
	var prev int64
	for i, f := range frames {
		f.T0 = cor.Apply(f.T0)
		if i > 0 && f.T0 < prev {
			return nil, fmt.Errorf("timeline: corrected timestamp regressed on track %d at offset=%d", f.TrackNumber, f.ByteOffset)
		}
		prev, out[i] = f.T0, f
	}
	return out, nil
}

// AssertCrossTrackMonotonic verifies that the merged boundary stream of all
// tracks never moves backwards. It returns the first offending frame.
func AssertCrossTrackMonotonic(tracks map[uint64][]Frame) error {
	idx := make(map[uint64]int, len(tracks))
	var globalPrev int64
	havePrev := false
	// K-way merge by T0; ties are allowed (interleaved blocks share a time).
	for remaining(tracks, idx) {
		var pick Frame
		pickTrack := uint64(0)
		first := true
		for tn, frames := range tracks {
			i := idx[tn]
			if i >= len(frames) {
				continue
			}
			f := frames[i]
			if first || f.T0 < pick.T0 || (f.T0 == pick.T0 && tn < pickTrack) {
				pick, pickTrack, first = f, tn, false
			}
		}
		if havePrev && pick.T0 < globalPrev {
			return fmt.Errorf("timeline: cross-track regression on track %d at offset=%d t0=%d < %d",
				pick.TrackNumber, pick.ByteOffset, pick.T0, globalPrev)
		}
		globalPrev, havePrev = pick.T0, true
		idx[pickTrack]++
	}
	return nil
}

func remaining(tracks map[uint64][]Frame, idx map[uint64]int) bool {
	for tn, frames := range tracks {
		if idx[tn] < len(frames) {
			return true
		}
	}
	return false
}
