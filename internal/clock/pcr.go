package clock

import (
	"tsrepair/internal/mediaerr"
)

// PCRMod is the modulus of the 42-bit PCR counter: 2^33 * 300.
// A PCR consists of a 33-bit 90 kHz base plus a 9-bit extension in units of
// 1/27 MHz (base*300 + ext).
// Multiplicative operators are left-associative in Go, so this evaluates as
// (1<<33)*300.
const PCRMod int64 = int64(1)<<33 * 300

// PCRHalfMod is half of PCRMod, the threshold for deciding whether a delta
// crosses a wrap boundary.
const PCRHalfMod = PCRMod / 2

// PCRUnwrapper expands 42-bit modulo PCR values onto a monotonic 27 MHz tick
// line, accounting for wrap-around at PCRMod.
type PCRUnwrapper struct {
	last    int64 // last raw modulo value seen
	wrap    int64 // accumulated wrap count * PCRMod
	started bool
}

// Unwrap maps a raw modulo PCR value onto the unwrapped tick line. A backward
// jump close to the modulus counts as a wrap; any other backward jump is a
// genuine discontinuity and reported as such without changing the wrap state.
// The returned bool reports whether this observation produced a wrap event.
func (u *PCRUnwrapper) Unwrap(raw int64) (ticks int64, wrapped bool, err error) {
	if raw < 0 || raw >= PCRMod {
		return 0, false, mediaerr.Fatal(mediaerr.CodeTimeOverflow, 0, 0,
			"PCR value outside 42-bit range", "drop observation")
	}
	if !u.started {
		u.started = true
		u.last = raw
		return raw, false, nil
	}
	delta := raw - u.last
	if delta < -PCRHalfMod {
		// Forward wrap: the raw counter crossed PCRMod and restarted near
		// zero, which looks on the wire like a large backward jump.
		next, aerr := CheckedAdd(u.wrap, PCRMod)
		if aerr != nil {
			return 0, false, aerr
		}
		u.wrap = next
		wrapped = true
	} else if delta < 0 {
		// Any other backward jump is a genuine discontinuity. Silently
		// unwinding the wrap budget would corrupt the timeline, so keep the
		// budget intact and surface the event; the caller may re-anchor.
		u.last = raw
		ticks, err = CheckedAdd(raw, u.wrap)
		return ticks, false, mediaerr.Recoverable(mediaerr.CodePCRDiscontinuity, 0, 0,
			"PCR counter jumped backward without a wrap boundary",
			"keep wrap budget; caller may re-anchor at this observation")
	}
	u.last = raw
	ticks, err = CheckedAdd(raw, u.wrap)
	return ticks, wrapped, err
}

// Started reports whether at least one PCR observation was expanded.
func (u *PCRUnwrapper) Started() bool { return u.started }

// DriftEstimator correlates the (unwrapped) sender PCR line with the local
// receiver 27 MHz tick line. The per-step drift is estimated from adjacent
// valid observations; every correction is bounded by MaxCorrectionTicks and
// anomalous observations are isolated so a single bad PCR cannot propagate
// into the repaired timeline.
type DriftEstimator struct {
	MaxCorrectionTicks int64
	OutlierTicks       int64

	anchorPCR   int64
	anchorLocal int64
	prevPCR     int64
	prevLocal   int64
	started     bool

	// TotalCorrection is the cumulative clamped offset applied, in 27 MHz
	// ticks. It never changes by more than MaxCorrectionTicks per step.
	TotalCorrection int64
	Outliers        int
}

// Observation is the result of feeding one PCR sample to the estimator.
type Observation struct {
	CorrectedTicks int64 // repaired sender ticks aligned to the local line
	RawTicks       int64
	Correction     int64 // offset applied at this step
	Clamped        bool
	Outlier        bool
}

// Observe ingests one (unwrapped PCR, local receiver tick) pair. When the
// observation is an outlier it is isolated: the returned Observation has
// Outlier set and the estimator state is untouched.
func (d *DriftEstimator) Observe(pcrTicks, localTicks int64, ec *mediaerr.Collector, pid uint16, offset int64) (Observation, error) {
	if !d.started {
		d.started = true
		d.anchorPCR, d.anchorLocal, d.prevPCR, d.prevLocal = pcrTicks, localTicks, pcrTicks, localTicks
		return Observation{CorrectedTicks: pcrTicks, RawTicks: pcrTicks}, nil
	}

	// Expected local delta implied by the sender PCR if there were no drift.
	pcrDelta, err := AbsDiff(pcrTicks, d.prevPCR)
	if err != nil {
		return Observation{}, err
	}
	localDelta, err := AbsDiff(localTicks, d.prevLocal)
	if err != nil {
		return Observation{}, err
	}
	deviation, err := AbsDiff(localDelta, pcrDelta)
	if err != nil {
		return Observation{}, err
	}

	if deviation > d.OutlierTicks {
		d.Outliers++
		ec.Add(mediaerr.Recoverable(mediaerr.CodePCROutlier, pid, offset,
			"PCR observation deviates beyond outlier threshold",
			"isolate observation; keep previous drift estimate"))
		return Observation{RawTicks: pcrTicks, Outlier: true}, nil
	}

	// Drift implied by this pair of adjacent observations, measured against
	// the anchor: positive means the local line runs ahead of the sender PCR.
	desired := (localTicks - d.anchorLocal) - (pcrTicks - d.anchorPCR)
	delta := desired - d.TotalCorrection

	clamped := false
	if delta > d.MaxCorrectionTicks {
		delta = d.MaxCorrectionTicks
		clamped = true
	} else if delta < -d.MaxCorrectionTicks {
		delta = -d.MaxCorrectionTicks
		clamped = true
	}

	next, aerr := CheckedAdd(d.TotalCorrection, delta)
	if aerr != nil {
		return Observation{}, aerr
	}
	d.TotalCorrection = next
	d.prevPCR, d.prevLocal = pcrTicks, localTicks

	if clamped {
		ec.Add(mediaerr.Recoverable(mediaerr.CodePCRDriftClamped, pid, offset,
			"single-step drift correction exceeded configured cap",
			"clamp correction step to configured maximum"))
	}

	corrected, err := CheckedAdd(pcrTicks, d.TotalCorrection)
	if err != nil {
		return Observation{}, err
	}
	return Observation{
		CorrectedTicks: corrected,
		RawTicks:       pcrTicks,
		Correction:     delta,
		Clamped:        clamped,
	}, nil
}

// Started reports whether an anchor observation exists.
func (d *DriftEstimator) Started() bool { return d.started }
