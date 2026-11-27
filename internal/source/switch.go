// Package source normalizes RTP transport coordinates after source switches
// and clock wraparound. It never rewrites the original values: every output
// sample keeps the source RTP timestamp and byte offset for traceability, and
// adds a monotonically non-decreasing normalized tick mapping on the shared
// integer clock grid.
package source

import (
	"gamepack/internal/clock"
	perr "gamepack/internal/errors"
)

const subsystem = "source"

// Config controls wrap detection. BitWidth is the modular RTP clock width
// (typically 32). A transition counts as wraparound only when the previous
// value is in the upper half of the period and the new value is in the lower
// half; every other backward step is a boundary error.
type Config struct {
	BitWidth uint `json:"bit_width"`
}

// Sample is one normalized transport sample. OriginalRTP and OriginalOffset
// are preserved verbatim; NormalizedTick is the unwrapped media time.
type Sample struct {
	Ordinal        int        `json:"ordinal"`
	OriginalRTP    uint32     `json:"original_rtp"`
	OriginalOffset int64      `json:"original_offset"`
	NormalizedTick clock.Tick `json:"normalized_tick"`
	// Wrapped is true on the first sample after a detected clock wraparound.
	Wrapped bool `json:"wrapped"`
	// Switched is true on the first sample delivered by a new source.
	Switched bool `json:"switched"`
}

// Normalizer consumes RTP timestamps in arrival order and emits normalized
// samples. It is stateful and single-goroutine.
type Normalizer struct {
	cfg         Config
	period      int64
	half        clock.Tick
	initialized bool
	prevRaw     int64
	last        clock.Tick // most recently emitted normalized tick
	switching   bool       // next Push begins a new, declared source
	samples     []Sample
}

// NewNormalizer validates the clock configuration.
func NewNormalizer(cfg Config) (*Normalizer, error) {
	if cfg.BitWidth == 0 || cfg.BitWidth > 32 {
		return nil, perr.New(subsystem, perr.CodeClockBackward,
			"wrap bit width %d must be in 1..32", cfg.BitWidth)
	}
	return &Normalizer{
		cfg:    cfg,
		period: clock.Period(cfg.BitWidth),
		half:   clock.HalfPeriod(cfg.BitWidth),
	}, nil
}

// Push feeds one RTP timestamp with its original stream byte offset. A
// backward jump that does not satisfy the half-period wrap rule fails with
// CodeClockBackward anchored at the original offset, and no sample is added.
// When a source switch has been declared via SwitchAt, a backward timestamp
// is accepted as a new-source baseline instead: the normalized timeline keeps
// advancing while the original RTP value is preserved verbatim.
func (n *Normalizer) Push(ordinal int, rtp uint32, offset int64) error {
	raw := int64(rtp)
	if !n.initialized {
		n.initialized = true
		n.prevRaw = raw
		n.last = 0
		n.samples = append(n.samples, Sample{
			Ordinal: ordinal, OriginalRTP: rtp, OriginalOffset: offset,
			NormalizedTick: 0, Switched: n.switching,
		})
		n.switching = false
		return nil
	}

	wrapped := false
	switched := n.switching
	n.switching = false
	var delta int64
	switch {
	case raw >= n.prevRaw:
		delta = raw - n.prevRaw
	case n.isWrap(n.prevRaw, raw):
		delta = (n.period - n.prevRaw) + raw
		wrapped = true
	case switched:
		// Declared source switch: new source clock restarts low. Re-baseline
		// the raw clock and hold the normalized timeline flat at this sample;
		// later deltas continue from the new raw origin.
		delta = 0
	default:
		return perr.At(subsystem, perr.CodeClockBackward, offset,
			"timestamp regression %d -> %d is not a half-period wraparound",
			n.prevRaw, raw)
	}

	next := n.last + clock.Tick(delta)
	if next.IsBefore(n.last) {
		return perr.At(subsystem, perr.CodeClockBackward, offset,
			"normalized tick overflow at offset %d", offset)
	}
	n.prevRaw = raw
	n.last = next
	n.samples = append(n.samples, Sample{
		Ordinal: ordinal, OriginalRTP: rtp, OriginalOffset: offset,
		NormalizedTick: next, Wrapped: wrapped, Switched: switched,
	})
	return nil
}

// SwitchAt declares that the next Push carries the first packet of a new
// source. A backward RTP timestamp on that packet is normalized rather than
// rejected; the original timestamp and byte offset remain stored.
func (n *Normalizer) SwitchAt() { n.switching = true }

// isWrap applies the configured rule: previous value in the upper half of
// the period AND new value in the lower half. Any smaller backward move is
// treated as a boundary error rather than a wrap.
func (n *Normalizer) isWrap(prev, next int64) bool {
	return clock.Tick(prev).IsAfter(n.half-1) && clock.Tick(next).IsBefore(n.half)
}

// Samples returns the accumulated normalized samples.
func (n *Normalizer) Samples() []Sample {
	out := make([]Sample, len(n.samples))
	copy(out, n.samples)
	return out
}

// LastTick returns the most recent normalized tick.
func (n *Normalizer) LastTick() clock.Tick { return n.last }
