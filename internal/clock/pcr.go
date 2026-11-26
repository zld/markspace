package clock

import "math"

// PCRUnwrapper expands 42-bit PCR values onto a monotonic 27 MHz tick line.
// A wrap is only accepted when the local continuity holds: the new raw value
// must be near zero and the step must be consistent with one full period.
type PCRUnwrapper struct {
	last   int64 // last unwrapped tick
	raw    int64 // last raw 42-bit value
	has    bool
	wraped int64 // number of accepted wraps
}

// Unwrap returns the monotonic tick count for a raw 42-bit PCR value.
// discont is the TS adaptation-field discontinuity indicator.
func (u *PCRUnwrapper) Unwrap(raw int64, discont bool, offset int64) (int64, error) {
	if raw < 0 || raw >= PCRWrapTicks {
		return 0, New(CodeTSPCR, "clock", offset, 0, 0,
			"PCR value %d outside 42-bit range", raw)
	}
	if !u.has {
		u.has, u.last, u.raw = true, raw, raw
		return raw, nil
	}
	delta := raw - u.raw
	switch {
	case delta == 0:
		// repeated PCR is legal; keep the monotonic line
		return u.last, nil
	case delta > 0:
		u.last += delta
	case discont:
		// explicit discontinuity: resync without inventing a wrap
		u.last += PCRWrapTicks + delta
	default:
		if u.raw > PCRWrapTicks/2 && raw < PCRWrapTicks/4 {
			// plausible wrap: high -> low near the period boundary
			u.wraped++
			u.last += PCRWrapTicks + delta
			break
		}
		return u.last, New(CodePCRDiscont, "clock", offset,
			PCRToNanos(u.last), PCRToNanos(u.last),
			"PCR jumped backwards from %d to %d without continuity", u.raw, raw)
	}
	u.raw = raw
	return u.last, nil
}

// Wraps reports how many wraps have been accepted.
func (u *PCRUnwrapper) Wraps() int64 { return u.wraped }

// Has reports whether at least one PCR was consumed.
func (u *PCRUnwrapper) Has() bool { return u.has }

// RTPUnwrapper expands uint32 RTP timestamps onto a monotonic 90 kHz line.
type RTPUnwrapper struct {
	last int64
	raw  uint32
	has  bool
}

// Unwrap expands one RTP timestamp using uint32 modular arithmetic.
func (u *RTPUnwrapper) Unwrap(ts uint32, offset int64) (int64, error) {
	if !u.has {
		u.has, u.raw, u.last = true, ts, int64(ts)
		return u.last, nil
	}
	delta := int32(ts - u.raw) // signed modular difference
	next := u.last + int64(delta)
	if next < 0 {
		return u.last, New(CodeRTPDiscont, "clock", offset, 0, 0,
			"RTP timestamp produced negative unwrapped value")
	}
	u.raw, u.last = ts, next
	return next, nil
}

// DriftEstimator correlates the RTP media clock against the PCR clock and
// reports the relative rate error in parts per million.
type DriftEstimator struct {
	sumX, sumY, sumXY, sumXX float64
	n                        int
}

// Add records one (RTP nanos, PCR nanos) anchor pair.
func (d *DriftEstimator) Add(rtpNanos, pcrNanos int64) {
	x := float64(rtpNanos)
	y := float64(pcrNanos)
	d.sumX += x
	d.sumY += y
	d.sumXY += x * y
	d.sumXX += x * x
	d.n++
}

// PPM returns (slope-1)*1e6: positive means the PCR clock runs fast versus
// the RTP clock. Zero is returned until two anchors exist.
func (d *DriftEstimator) PPM() float64 {
	if d.n < 2 {
		return 0
	}
	denom := float64(d.n)*d.sumXX - d.sumX*d.sumX
	if denom == 0 {
		return 0
	}
	slope := (float64(d.n)*d.sumXY - d.sumX*d.sumY) / denom
	return (slope - 1) * 1e6
}

// N is the number of anchor pairs consumed.
func (d *DriftEstimator) N() int { return d.n }

// RoundPPM snaps a ppm estimate to a stable integer for manifest output.
func RoundPPM(ppm float64) int64 { return int64(math.Round(ppm)) }
