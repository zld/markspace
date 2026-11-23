package repair

import "mediapack/internal/clock"

// TsDecision is the repaired decode-time placement of a fragment.
type TsDecision struct {
	Base     int64
	Duration int64
	Repaired bool
	Reason   string
}

// TimestampRepairer repairs tfdt values against the shared timeline.
type TimestampRepairer struct {
	set *clock.TimelineSet
}

// NewTimestampRepairer returns a repairer writing to the given timelines.
func NewTimestampRepairer(set *clock.TimelineSet) *TimestampRepairer {
	return &TimestampRepairer{set: set}
}

// Repair places a fragment on its track timeline. A missing or backwards
// tfdt is rebuilt from the previous decode end and the track timescale; a
// fragment whose duration cannot be derived is unrecoverable and must
// never reach the index.
func (r *TimestampRepairer) Repair(trackID, timescale uint32, hasTfdt bool, base uint64, duration int64) (TsDecision, error) {
	tl, err := r.set.Track(trackID, timescale)
	if err != nil {
		return TsDecision{}, err
	}
	if duration <= 0 {
		return TsDecision{}, &clock.Error{Cat: clock.CatUnrecoverable,
			Track: trackID, Msg: "fragment duration not derivable"}
	}
	expected, started := tl.ExpectedStart()
	dec := TsDecision{Base: int64(base), Duration: duration}
	switch {
	case !hasTfdt:
		if started {
			dec.Base = expected
		} else {
			dec.Base = 0
		}
		dec.Repaired, dec.Reason = true, "missing-tfdt"
	case started && int64(base) < expected:
		dec.Base = expected
		dec.Repaired, dec.Reason = true, "tfdt-backstep"
	}
	if err := tl.Advance(dec.Base, dec.Duration); err != nil {
		return TsDecision{}, err
	}
	return dec, nil
}
