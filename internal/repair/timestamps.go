// Package repair reconstructs monotonic presentation timestamps for frame
// events whose source timestamps are missing, duplicated, regressive or
// affected by 33-bit MPEG-style wrap-around.
package repair

import (
	"archive-recovery/internal/media"
)

// Repair reasons recorded on every adjusted frame.
const (
	ReasonOK        = "ok"
	ReasonMissing   = "missing_timestamp"
	ReasonDuplicate = "duplicate_timestamp"
	ReasonRegress   = "regressive_timestamp"
	ReasonWrap33    = "33bit_wraparound"
)

const (
	wrapMod33  = int64(1) << 33
	wrapMask33 = wrapMod33 - 1
)

// TimestampRepairer is a stateful, single-pass repair kernel. All output
// ticks live on one unbounded (int64) axis; the raw source value and the
// reason for each decision are preserved for the provenance manifest.
type TimestampRepairer struct {
	clock    media.Clock
	tickStep int64 // nominal per-frame tick delta used for interpolation

	lastOut  int64 // last emitted repaired tick
	lastRaw  int64 // previous raw (modulo-2^33) tick
	haveLast bool
	unwrap   int64 // accumulated whole-cycle offset applied after wraps
}

// NewTimestampRepairer builds a repairer for the given clock and nominal
// frame tick step (e.g. clock_num/frame_rate).
func NewTimestampRepairer(clock media.Clock, tickStep int64) (*TimestampRepairer, error) {
	if tickStep <= 0 {
		return nil, media.NewError(media.ClassInvalidArgument, "repair",
			"tick step must be positive")
	}
	return &TimestampRepairer{clock: clock, tickStep: tickStep}, nil
}

// Decision is the audit trail of one frame repair.
type Decision struct {
	FrameIndex int
	RawPTS     int64
	HasRaw     bool
	FixedPTS   int64
	Reason     string
}

// Repair normalizes one frame event in stream order and returns its decision.
// Frames must be presented in presentation order; the kernel needs no
// buffering beyond the previous frame.
func (r *TimestampRepairer) Repair(ev *media.FrameEvent) Decision {
	d := Decision{FrameIndex: ev.Index, RawPTS: ev.RawPTS, HasRaw: ev.HasPTS, FixedPTS: ev.RawPTS}
	if !r.haveLast {
		if !ev.HasPTS {
			d.FixedPTS, d.Reason = 0, ReasonMissing
		} else {
			d.FixedPTS, d.Reason = ev.RawPTS, ReasonOK
			r.lastRaw = ev.RawPTS
		}
		r.commit(ev, d)
		return d
	}

	switch {
	case !ev.HasPTS:
		d.FixedPTS = r.lastOut + r.tickStep
		d.Reason = ReasonMissing
	case isWrap33(r.lastRaw, ev.RawPTS):
		r.unwrap += wrapMod33
		d.FixedPTS = ev.RawPTS + r.unwrap
		d.Reason = ReasonWrap33
		r.lastRaw = ev.RawPTS
	case r.clock.CmpTick(ev.RawPTS+r.unwrap, r.lastOut) > 0:
		d.FixedPTS = ev.RawPTS + r.unwrap
		d.Reason = ReasonOK
		r.lastRaw = ev.RawPTS
	default:
		// Equal or regressive even after unwrapping: force strict
		// monotonicity with the nominal step and keep the raw evidence.
		d.FixedPTS = r.lastOut + r.tickStep
		if ev.RawPTS == r.lastRaw {
			d.Reason = ReasonDuplicate
		} else {
			d.Reason = ReasonRegress
		}
	}
	r.commit(ev, d)
	return d
}

func (r *TimestampRepairer) commit(ev *media.FrameEvent, d Decision) {
	ev.RepairedPTS = d.FixedPTS
	ev.RepairReason = d.Reason
	r.lastOut = d.FixedPTS
	r.haveLast = true
}

// isWrap33 distinguishes a 33-bit counter wrap (previous raw tick in the
// upper half of the cycle, new raw tick in the lower quarter) from ordinary
// jitter and regression.
func isWrap33(prevRaw, raw int64) bool {
	return prevRaw > (wrapMask33>>1) && raw >= 0 && raw < (wrapMask33>>2)
}
