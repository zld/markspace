package medpipe

import (
	"fmt"
	"math"
)

// UnifiedTimeBase is the single clock domain every manifest is rendered in.
// Microseconds give sub-microsecond accuracy for the common 90 kHz video and
// 48 kHz audio RTP clocks while keeping all arithmetic integer based.
const UnifiedTimeBase uint32 = 1_000_000

// RTP clock wrap period (2^32).
const rtpModulus = uint64(1) << 32

// TimestampAnomaly classifies a single repair event so manifests can expose
// both the original value and the applied correction.
type TimestampAnomaly string

const (
	AnomalyNone      TimestampAnomaly = "ok"
	AnomalyRollback  TimestampAnomaly = "rollback"
	AnomalyDuplicate TimestampAnomaly = "duplicate"
	AnomalyMissing   TimestampAnomaly = "missing"
)

// SampleRecord captures one RTP timestamp observation after normalization.
type SampleRecord struct {
	// Sequence is the RTP sequence number of the packet carrying the sample.
	Sequence uint16 `json:"sequence"`
	// FrameOrdinal is the zero-based index of this observation among all
	// pushed samples, used to reconstruct the ideal timing grid.
	FrameOrdinal int `json:"frame_ordinal"`
	// OriginalTS is the raw 32-bit RTP timestamp as received.
	OriginalTS uint32 `json:"original_ts"`
	// WrappedTS is the original timestamp unwrapped to a monotonic 64-bit
	// axis by adding whole 2^32 periods.
	WrappedTS uint64 `json:"wrapped_ts"`
	// RepairedTS is the clock-domain timestamp after repair; it is always
	// strictly increasing (duplicates are resolved deterministically).
	RepairedTS uint64 `json:"repaired_ts"`
	// UnifiedUS is RepairedTS converted into the unified microsecond clock.
	UnifiedUS int64 `json:"unified_us"`
	// Anomaly records what kind of repair, if any, was applied.
	Anomaly TimestampAnomaly `json:"anomaly"`
	// Correction is RepairedTS minus the unwrapped original. Zero means the
	// observation needed no correction.
	Correction int64 `json:"correction"`
}

// ClockStats summarizes repair work for the manifest and run statistics.
type ClockStats struct {
	Rollbacks   int   `json:"rollbacks"`
	Duplicates  int   `json:"duplicates"`
	Missing     int   `json:"missing"`
	MaxErrorUS  int64 `json:"max_error_us"`
	MeanErrorUS int64 `json:"mean_error_us"`
}

// Clock normalizes one RTP timestamp stream onto a monotonic axis and then
// into the unified microsecond time base. A fresh Clock is required per
// track; it is safe for sequential use by a single goroutine.
type Clock struct {
	track     string
	clockRate uint32
	scaleNum  uint64 // UnifiedTimeBase numerator
	scaleDen  uint64 // clock-rate denominator

	expectedDelta uint64 // learned per-sample RTP-tick delta, 0 until known
	explicitDelta bool   // true when the caller seeded a trusted nominal delta
	lastWrapped   uint64 // last unwrapped input timestamp
	lastRepaired  uint64 // last repaired clock-domain timestamp
	lastSeq       uint16
	havePrev      bool
	periods       uint64 // accumulated wrap periods
	// repairOffset is the persistent correction added to every unwrapped
	// timestamp after a rollback/duplicate. Keeping it persistent means later
	// untouched samples stay on the repaired monotonic grid instead of
	// carrying the sender's offset into the next delta.
	repairOffset int64

	records []SampleRecord
	stats   ClockStats
	errs    []*PipelineError
	b       *ErrorBuilder
}

// NewClock constructs a clock for one track. clockRate is the RTP clock rate
// in Hz; expectedDelta is the nominal per-sample tick delta (0 = learn it).
func NewClock(track string, clockRate uint32, expectedDelta uint64) *Clock {
	if clockRate == 0 {
		clockRate = UnifiedTimeBase
	}
	return &Clock{
		track:         track,
		clockRate:     clockRate,
		scaleNum:      uint64(UnifiedTimeBase),
		scaleDen:      uint64(clockRate),
		expectedDelta: expectedDelta,
		explicitDelta: expectedDelta > 0,
		b:             NewErrorBuilder(track),
	}
}

// unwrap places a 32-bit RTP timestamp on a monotonic 64-bit axis, applying
// whole 2^32 period corrections using the standard half-modulus heuristic.
func (c *Clock) unwrap(ts uint32) uint64 {
	if !c.havePrev {
		return uint64(ts) + c.periods*rtpModulus
	}
	prev := c.lastWrapped
	cur := uint64(ts) + c.periods*rtpModulus
	const halfMod = rtpModulus / 2
	switch {
	case cur+halfMod < prev:
		// Forward wrap: timestamp jumped past 2^32 back to a small value.
		c.periods++
		cur += rtpModulus
	case cur > prev+halfMod:
		// Backward wrap: a large value arrived after a small one; the
		// previous observation itself must have crossed a period.
		if c.periods > 0 {
			c.periods--
			cur -= rtpModulus
		}
	}
	return cur
}

// Push ingests one access-unit observation, identified by a unique RTP
// timestamp (the scanner calls it exactly once per access unit even when the
// unit spans several RTP packets). A timestamp delta larger than one nominal
// step indicates missing samples; a non-increasing delta indicates rollback
// or duplicates. An anomaly that cannot be uniquely derived is fatal and
// stops the track per the processing requirements.
func (c *Clock) Push(seq uint16, ts uint32) (*SampleRecord, error) {
	wrapped := c.unwrap(ts)
	ordinal := len(c.records)
	rec := SampleRecord{Sequence: seq, OriginalTS: ts, WrappedTS: wrapped, FrameOrdinal: ordinal}

	if !c.havePrev {
		rec.RepairedTS = wrapped
		rec.Anomaly = AnomalyNone
		c.havePrev = true
	} else {
		delta := int64(wrapped - c.lastWrapped)
		// The ideal grid point for this ordinal, anchored on the first sample.
		ideal := c.records[0].RepairedTS + uint64(ordinal)*c.expectedDelta
		switch {
		case delta < 0:
			// Rollback: the sender clock moved backwards. Place the sample on
			// its ideal grid point and adopt a persistent offset so later,
			// untouched samples continue to land on the same grid. Requires an
			// explicitly configured nominal delta to be unambiguous.
			if !c.explicitDelta {
				return nil, c.b.FatalAt(CodeTimestampRepair,
					fmt.Sprintf("timestamp rollback (%d ticks) without a configured nominal delta", delta),
					"provide a fixed frame duration or resend the stream with sender-clock reports", 0)
			}
			rec.Anomaly = AnomalyRollback
			rec.RepairedTS = ideal
			c.repairOffset = int64(ideal) - int64(wrapped)
			c.stats.Rollbacks++
		case delta == 0:
			// Duplicate timestamp on a new access unit: place it one nominal
			// step after the previous sample, only with a configured delta.
			if !c.explicitDelta {
				return nil, c.b.FatalAt(CodeTimestampRepair,
					"duplicate timestamp without a configured nominal delta",
					"provide a fixed frame duration or deduplicate the input", 0)
			}
			rec.Anomaly = AnomalyDuplicate
			rec.RepairedTS = c.lastRepaired + c.expectedDelta
			c.repairOffset = int64(rec.RepairedTS) - int64(wrapped)
			c.stats.Duplicates++
		default:
			candidate := uint64(int64(wrapped) + c.repairOffset)
			if c.expectedDelta == 0 {
				// Learn the nominal delta from the first clean forward step.
				c.expectedDelta = uint64(delta)
				candidate = wrapped
			} else if c.repairOffset == 0 {
				// Missing-sample detection only makes sense on an unshifted
				// axis; after a repair the offset already accounts for it.
				gap := candidate - c.lastRepaired
				if gap > c.expectedDelta {
					if gap%c.expectedDelta != 0 {
						return nil, c.b.FatalAt(CodeTimestampRepair,
							fmt.Sprintf("gap of %d ticks is not a multiple of nominal delta %d", gap, c.expectedDelta),
							"resend the missing range or supply a corrected timestamp index", 0)
					}
					rec.Anomaly = AnomalyMissing
					c.stats.Missing += int(gap/c.expectedDelta) - 1
				}
			}
			if rec.Anomaly == "" {
				rec.Anomaly = AnomalyNone
			}
			rec.RepairedTS = candidate
		}
	}

	rec.Correction = int64(rec.RepairedTS - wrapped)
	rec.UnifiedUS = c.toUnified(rec.RepairedTS)
	c.lastWrapped = wrapped
	c.lastRepaired = rec.RepairedTS
	c.lastSeq = seq
	c.records = append(c.records, rec)
	return &rec, nil
}

// toUnified converts clock-domain ticks to unified microseconds with
// rounded integer division.
func (c *Clock) toUnified(ticks uint64) int64 {
	return int64(math.Round(float64(ticks) * float64(c.scaleNum) / float64(c.scaleDen)))
}

// toUnifiedI is toUnified for a non-negative int64 magnitude.
func (c *Clock) toUnifiedI(ticks int64) int64 {
	return c.toUnified(uint64(ticks))
}

// Finalize closes the stream and computes alignment error statistics. The
// error is measured against the ideal monotonic grid implied by the nominal
// delta; well-formed streams therefore report zero error.
func (c *Clock) Finalize() (ClockStats, []SampleRecord, []*PipelineError) {
	if len(c.records) <= 1 || c.expectedDelta == 0 {
		return c.stats, c.records, c.errs
	}
	// Alignment error measures residual jitter only on the untouched prefix
	// of the stream: the samples before the first rollback/duplicate repair.
	// Once a repair introduces a persistent offset, later samples follow a
	// shifted-but-consistent grid that is not "error", so they are excluded.
	var sum, maxAbs int64
	var measured int
	base := c.records[0].RepairedTS
	for _, r := range c.records {
		if r.Anomaly != AnomalyNone {
			break // a repair (and everything after it) is out of scope
		}
		ideal := base + uint64(r.FrameOrdinal)*c.expectedDelta
		errTicks := int64(r.RepairedTS) - int64(ideal)
		errUS := c.toUnifiedI(abs64(errTicks))
		sum += errUS
		measured++
		if errUS > maxAbs {
			maxAbs = errUS
		}
	}
	c.stats.MaxErrorUS = maxAbs
	if measured > 0 {
		c.stats.MeanErrorUS = sum / int64(measured)
	}
	return c.stats, c.records, c.errs
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// Records returns the normalized sample records collected so far.
func (c *Clock) Records() []SampleRecord { return c.records }

// ClockRate returns the RTP clock rate of this clock.
func (c *Clock) ClockRate() uint32 { return c.clockRate }
