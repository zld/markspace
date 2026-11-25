package timeline

import (
	"math"

	"github.com/pion/rtp"
)

// usPerTick converts ticks of a track timescale into microseconds.
// All common-axis values are int64 microseconds; a 90kHz track spanning
// ~2900 years still fits, so no precision loss occurs for real inputs.
func usPerTick(timescale uint32) float64 {
	if timescale == 0 {
		return 0
	}
	return 1_000_000.0 / float64(timescale)
}

// RTPUnwrapper expands 32-bit RTP timestamps into a monotonic uint64 series
// following RFC 3550 Appendix A.1 style wrap handling. It uses pion/rtp
// headers as its input type so callers stay on the pinned transport library.
type RTPUnwrapper struct {
	last    uint32
	cycles  uint64
	started bool
	maxJump uint32 // discontinuities larger than this cannot be a wrap
}

// NewRTPUnwrapper creates an unwrapper. maxJump bounds how far a packet may
// move forward before the change is considered non-wrap noise; zero disables
// the forward bound.
func NewRTPUnwrapper(clockHz uint32, maxJump uint32) *RTPUnwrapper {
	if maxJump == 0 {
		maxJump = math.MaxUint32 / 2
	}
	return &RTPUnwrapper{maxJump: maxJump}
}

// Expand returns the unwrapped 64-bit timestamp for one RTP header.
func (u *RTPUnwrapper) Expand(h *rtp.Header) (uint64, error) {
	ts := h.Timestamp
	if !u.started {
		u.started = true
		u.last = ts
		return uint64(ts), nil
	}
	delta := int64(ts) - int64(u.last)
	switch {
	case delta == 0:
		// same tick: multiple packets per frame, cycles unchanged
	case delta > 0:
		if uint32(delta) > u.maxJump {
			return 0, New(ErrTimeline, "", -1,
				"RTP timestamp jump of %d ticks exceeds max %d", delta, u.maxJump)
		}
	case delta < 0:
		// backward movement: either a 32-bit wrap or reorder/rollback.
		forward := uint64(math.MaxUint32) + 1 - uint64(u.last) + uint64(ts)
		if forward > uint64(u.maxJump) {
			return 0, New(ErrTimeline, "", -1,
				"RTP timestamp went backwards by %d ticks; no plausible wrap", -delta)
		}
		u.cycles += uint64(math.MaxUint32) + 1
	}
	u.last = ts
	return u.cycles + uint64(ts), nil
}

// ClockConfig parameterises one track's timeline builder.
type ClockConfig struct {
	TrackID         uint32
	Timescale       uint32 // ticks per second from mdhd; required for fMP4
	DriftPPMMax     int64  // tolerated clock drift in parts-per-million
	WrapWindowTicks uint64 // tfdt backward jump below this = 32-bit rollover
}

// SampleTime is one sample positioned on the common microsecond axis.
type SampleTime struct {
	Index   int
	StartUS int64
	EndUS   int64
	Invalid bool // true when this interval cannot be mapped deterministically
	ErrCode string
	ErrMsg  string
	// AdjustUS records the rollover/drift correction applied at this sample.
	AdjustUS int64
}

// TrackClock maps one track's tick timeline onto common-axis microseconds.
// It keeps the raw tfdt tick and a high-period cycle count, so rollover is
// absorbed exactly once and subsequent in-period samples stay ordinary.
type TrackClock struct {
	cfg        ClockConfig
	scale      float64
	lastRaw    uint64 // previous raw (pre-wrap) base tick
	cycles     uint64 // accumulated full WrapWindowTicks+1 periods
	prevEndUS  int64
	started    bool
	rollovers  int
	driftAccum int64
}

// NewTrackClock builds a track clock.
func NewTrackClock(cfg ClockConfig) *TrackClock {
	if cfg.WrapWindowTicks == 0 {
		// Default window is the natural 32-bit tfdt v0 period.
		cfg.WrapWindowTicks = math.MaxUint32
	}
	return &TrackClock{cfg: cfg, scale: usPerTick(cfg.Timescale)}
}

// Rollovers returns how many tfdt rollovers were absorbed.
func (c *TrackClock) Rollovers() int { return c.rollovers }

// DriftCorrectionUS returns total drift correction applied, in microseconds.
func (c *TrackClock) DriftCorrectionUS() int64 { return c.driftAccum }

// MapTick places a sample at absolute (possibly 32-bit-wrapped) tick `base`
// with the given duration ticks. Output is monotonic; intervals that cannot
// be mapped deterministically are flagged invalid rather than fabricated.
func (c *TrackClock) MapTick(index int, base, duration uint64) SampleTime {
	period := c.cfg.WrapWindowTicks + 1
	st := SampleTime{Index: index}

	if c.started && base < c.lastRaw {
		// Backward raw tick. A genuine 32-bit rollover has two signatures:
		// the previous raw tick sat in the top half of the window, and the
		// implied forward span (old->boundary->new) stays within one window.
		// A rollback in the middle of the window fails both and is refused.
		forwardSpan := c.cfg.WrapWindowTicks - c.lastRaw + base
		if c.lastRaw > c.cfg.WrapWindowTicks/2 && base <= c.cfg.WrapWindowTicks &&
			forwardSpan <= c.cfg.WrapWindowTicks {
			c.cycles += period
			c.rollovers++
			st.AdjustUS = c.tickToUS(period)
		} else {
			st.Invalid = true
			st.ErrCode = ErrTimeline
			st.ErrMsg = "tfdt rollback without plausible 32-bit rollover"
			return st
		}
	}

	absBase := c.cycles + base
	st.StartUS = c.tickToUS(absBase)
	st.EndUS = c.tickToUS(absBase + duration)

	if c.started && st.StartUS < c.prevEndUS {
		// Rounding collision: preserve strict monotonicity without inventing
		// media time beyond snapping to the previous boundary.
		st.StartUS = c.prevEndUS
		if st.EndUS < st.StartUS {
			st.EndUS = st.StartUS
		}
	}
	c.started = true
	c.lastRaw = base
	c.prevEndUS = st.EndUS
	return st
}

func (c *TrackClock) tickToUS(ticks uint64) int64 {
	if c.cfg.Timescale == 0 {
		return 0
	}
	return int64(math.Round(float64(ticks) * c.scale))
}

// CheckDrift compares elapsed media time against a reference elapsed duration
// (both microseconds) and returns the drift in ppm. Drift beyond the
// configured bound yields ErrTimeline; timestamps are never silently rewritten.
func (c *TrackClock) CheckDrift(mediaElapsedUS, refElapsedUS int64) (int64, error) {
	if refElapsedUS <= 0 {
		return 0, nil
	}
	ppm := (mediaElapsedUS - refElapsedUS) * 1_000_000 / refElapsedUS
	if c.cfg.DriftPPMMax > 0 && abs64(ppm) > c.cfg.DriftPPMMax {
		return ppm, New(ErrTimeline, "", -1,
			"clock drift %d ppm exceeds limit %d ppm", ppm, c.cfg.DriftPPMMax)
	}
	return ppm, nil
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
