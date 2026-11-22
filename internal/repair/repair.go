package repair

import (
	"math"

	"mediacore/internal/clock"
)

// Config bounds the repair heuristics. Every threshold is expressed in RTP
// clock ticks / sequence units so the logic stays independent of the track
// timescale.
type Config struct {
	// ReorderWindow: a packet arriving with a sequence within this many
	// units behind the highest seen sequence is treated as reordering.
	ReorderWindow uint16
	// MaxJumpTicks: an absolute (wrap-adjusted) backward DTS larger than
	// this is an unrepairable regression, not jitter.
	MaxJumpTicks int64
	// WrapMod is the 32-bit RTP timestamp modulus.
	WrapMod int64
}

// DefaultConfig matches typical 90 kHz video: 8 s of tolerated jitter.
func DefaultConfig() Config {
	return Config{ReorderWindow: 64, MaxJumpTicks: 90000 * 8, WrapMod: 1 << 32}
}

// Stats collects evidence about what the repairer actually changed. All
// counters are deterministic functions of the input packet stream.
type Stats struct {
	PacketsIn      uint32
	PacketsEmitted uint32
	WrapCount      uint32
	Reordered      uint32
	GapPackets     uint32 // missing sequence numbers (dropped samples)
	BigRegression  uint32 // events that produced a stable error (at most 1)
}

// Point is one repaired point on the extended (non-wrapping) RTP timeline.
type Point struct {
	Sequence     uint64 // extended 64-bit sequence
	Timestamp    int64  // extended, monotonic, non-wrapping
	RawTimestamp uint32 // original 32-bit value, retained for evidence
	WireBytes    uint32
}

// Engine is the per-track streaming repair state. It never buffers more than
// ReorderWindow points; everything beyond the window is finalized in order.
type Engine struct {
	cfg    Config
	stats  Stats
	roll   int64  // accumulated wrap count * WrapMod
	lastTS int64  // last emitted extended timestamp
	hiSeq  uint64 // highest extended sequence observed
	started bool
}

// NewEngine constructs a repair engine with validated thresholds.
func NewEngine(cfg Config) (*Engine, error) {
	if cfg.WrapMod <= 0 || cfg.WrapMod > (1<<32) {
		return nil, clock.ErrInvalidArgument("repair: wrap modulus must be within 32 bits")
	}
	if cfg.MaxJumpTicks <= 0 || cfg.MaxJumpTicks >= cfg.WrapMod/2 {
		return nil, clock.ErrInvalidArgument("repair: max jump must be within half the wrap modulus")
	}
	return &Engine{cfg: cfg}, nil
}

// Stats returns a copy of the running counters.
func (e *Engine) Stats() Stats { return e.stats }

// Push applies wrap extension, reordering and monotonicity checks to one
// packet. Returned points are already in emission order; reordering within
// the window makes the returned slice contain the buffered point as well,
// otherwise exactly one point is returned.
func (e *Engine) Push(rawSeq uint16, rawTS uint32, wire uint32) ([]Point, error) {
	e.stats.PacketsIn++
	seq := e.extendSeq(rawSeq)

	// Sequence gap accounting against the highest seen sequence.
	if !e.started {
		e.hiSeq = seq
	} else if seq > e.hiSeq {
		gap := seq - e.hiSeq - 1
		if gap > math.MaxUint32 {
			return nil, clock.ErrOverflow("repair.seq")
		}
		e.stats.GapPackets += uint32(gap)
		e.hiSeq = seq
	}

	extended := e.extendTS(rawTS)

	if !e.started {
		e.started = true
		e.lastTS = extended
		e.stats.PacketsEmitted++
		return []Point{{Sequence: seq, Timestamp: extended, RawTimestamp: rawTS, WireBytes: wire}}, nil
	}

	// Reordered duplicate-ish arrival within the window: emit at its own
	// extended time but do not move the DTS frontier.
	if seq < e.hiSeq && e.hiSeq-seq <= uint64(e.cfg.ReorderWindow) {
		e.stats.Reordered++
		if delta := extended - e.lastTS; delta < -e.cfg.MaxJumpTicks {
			e.stats.BigRegression++
			return nil, clock.ErrRecoverablef("repair.ts",
				"packet seq=%d regresses %d ticks beyond jitter limit %d", seq, -delta, e.cfg.MaxJumpTicks)
		}
		return []Point{{Sequence: seq, Timestamp: extended, RawTimestamp: rawTS, WireBytes: wire}}, nil
	}

	delta := extended - e.lastTS
	if delta < 0 {
		// extendTS may have missed a wrap because the roll estimate is based
		// on frontier order; re-check with explicit modular delta.
		if alt := modularDelta(rawTS, uint32(e.lastTS), e.cfg.WrapMod); alt > 0 && alt <= e.cfg.WrapMod/2 {
			extended = e.lastTS + alt
			delta = alt
		}
	}
	if delta < 0 {
		if -delta > e.cfg.MaxJumpTicks {
			e.stats.BigRegression++
			return nil, clock.ErrRecoverablef("repair.ts",
				"packet seq=%d regresses %d ticks beyond jitter limit %d", seq, -delta, e.cfg.MaxJumpTicks)
		}
		// Small disorder: clamp to frontier + 1 tick to keep strict
		// monotonicity; the clamp is counted as reordering evidence.
		e.stats.Reordered++
		extended = e.lastTS + 1
	}
	e.lastTS = extended
	e.stats.PacketsEmitted++
	return []Point{{Sequence: seq, Timestamp: extended, RawTimestamp: rawTS, WireBytes: wire}}, nil
}

func (e *Engine) extendSeq(raw uint16) uint64 {
	if !e.started {
		return uint64(raw)
	}
	base := e.hiSeq
	baseLow := uint16(base)
	delta := int32(raw) - int32(baseLow) // modular signed difference
	return uint64(int64(base) + int64(delta))
}

func (e *Engine) extendTS(raw uint32) int64 {
	if !e.started {
		return int64(raw)
	}
	last := e.lastTS
	mod := e.cfg.WrapMod
	rollBase := (last / mod) * mod
	cand := rollBase + int64(raw)
	// Pick the candidate nearest to the frontier (one roll up or down).
	opts := []int64{cand, cand + mod, cand - mod}
	best := opts[0]
	for _, o := range opts[1:] {
		if abs64(o-last) < abs64(best-last) {
			best = o
		}
	}
	if best > last && raw < uint32(last%mod) {
		e.stats.WrapCount++
	}
	return best
}

func modularDelta(a, b uint32, mod int64) int64 {
	d := int64(a) - int64(b)
	if d < 0 {
		d += mod
	}
	return d
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
