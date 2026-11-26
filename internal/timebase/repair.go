package timebase

import (
	"fmt"
	"math"

	"example.com/archiveverify/internal/cmaf"
	"example.com/archiveverify/internal/model"
)

const uint32Mod = int64(1) << 32

// extendV0 applies RFC-style 32-bit wraparound extension. A modular delta in
// (-2^31, 2^31) distinguishes a real small regression from a counter wrap.
func extendV0(raw uint32, prevExtended int64) int64 {
	if prevExtended < 0 {
		return int64(raw)
	}
	prevLow := uint32(prevExtended % uint32Mod)
	delta := int32(raw - prevLow)
	return prevExtended + int64(delta)
}

func usDeltaToUnits(d model.Us, scale uint32) (int64, bool) {
	if scale == 0 {
		return 0, false
	}
	v, ok := safeMul(int64(d), int64(scale))
	if !ok {
		return 0, false
	}
	return v / 1_000_000, true
}

func (m *Mapper) resolveStart(sourceID string, f *cmaf.Fragment, tf *cmaf.TrackFragment, st *trackState) (int64, *model.Repair, *Issue, error) {
	raw := int64(tf.BaseDecodeTime)
	extended := raw
	if tf.TFDTVersion == 0 {
		if st.haveRaw && st.source == sourceID {
			extended = extendV0(uint32(tf.BaseDecodeTime), st.lastRaw)
		}
	} else if tf.BaseDecodeTime > math.MaxInt64 {
		return 0, nil, nil, &model.MediaError{Code: model.CodeParse, Source: sourceID, TrackID: tf.TrackID,
			BoxType: "tfdt", BoxOffset: tf.TFDTOffset, OriginalTimeSet: true,
			OriginalTime: int64(tf.BaseDecodeTime & math.MaxInt64), Err: fmt.Errorf("tfdt over int64")}
	}

	// First event for this track.
	if !st.haveRaw {
		return extended, nil, nil, nil
	}

	// Source switch: do not compare unrelated clocks. Append to prior end.
	if st.source != "" && st.source != sourceID {
		origUs := UnitsToUs(extended, tf.Timescale)
		fixedUs := st.lastEndUs
		offsetUs := fixedUs - origUs
		offsetRaw, _ := usDeltaToUnits(offsetUs, tf.Timescale)
		r := &model.Repair{
			Kind: "source_switch_continue", Source: sourceID, OldSource: st.source,
			TrackID: tf.TrackID, BoxType: "tfdt", BoxOffset: tf.TFDTOffset,
			OrigRaw: extended, FixedRaw: extended + offsetRaw,
			OrigUs: origUs, FixedUs: fixedUs, OffsetUs: offsetUs, Timescale: tf.Timescale,
			Description: "new source clock appended to previous track end",
		}
		return extended, r, nil, nil
	}

	// Same source: a negative extended delta is a genuine backward tfdt.
	if extended < st.lastRaw {
		origUs := UnitsToUs(extended, tf.Timescale)
		fixedUs := st.lastEndUs
		offsetUs := fixedUs - origUs
		me := &model.MediaError{
			Code: model.CodeConflict, Source: sourceID, TrackID: tf.TrackID,
			BoxType: "tfdt", BoxOffset: tf.TFDTOffset,
			OriginalTimeSet: true, OriginalTime: extended,
			FixedTimeSet: true, FixedTime: int64(fixedUs),
			Err: fmt.Errorf("same-source tfdt moved backward from %d to %d", st.lastRaw, extended),
		}
		r := &model.Repair{
			Kind: "tfdt_backward_monotonic_clamp", Source: sourceID, TrackID: tf.TrackID,
			BoxType: "tfdt", BoxOffset: tf.TFDTOffset,
			OrigRaw: extended, FixedRaw: st.lastRaw,
			OrigUs: origUs, FixedUs: fixedUs, OffsetUs: offsetUs, Timescale: tf.Timescale,
			Description: me.Err.Error(),
		}
		return extended, r, &Issue{MediaError: me}, nil
	}

	// A 32-bit wrap was expanded; retain an audit record when raw fell but the
	// extended value advanced.
	if tf.TFDTVersion == 0 && uint32(tf.BaseDecodeTime) < uint32(st.lastRaw%uint32Mod) && extended >= st.lastRaw {
		r := &model.Repair{
			Kind: "tfdt_v0_wraparound_extend", Source: sourceID, TrackID: tf.TrackID,
			BoxType: "tfdt", BoxOffset: tf.TFDTOffset,
			OrigRaw: int64(uint32(tf.BaseDecodeTime)), FixedRaw: extended,
			OrigUs:  UnitsToUs(int64(uint32(tf.BaseDecodeTime)), tf.Timescale),
			FixedUs: UnitsToUs(extended, tf.Timescale), Timescale: tf.Timescale,
			Description: "32-bit tfdt wraparound expanded",
		}
		return extended, r, nil, nil
	}

	return extended, nil, nil, nil
}

// AggregateReport combines fragment reports and mapper audit state.
func (m *Mapper) AggregateReport(reports ...*Report) *Report {
	out := &Report{TimeMin: math.MaxInt64, TimeMax: math.MinInt64}
	out.Repairs = append(out.Repairs, m.Repairs...)
	out.Gaps = append(out.Gaps, m.Gaps...)
	out.Issues = append(out.Issues, m.Issues...)
	for _, r := range reports {
		if r == nil {
			continue
		}
		out.Samples = append(out.Samples, r.Samples...)
		for _, s := range r.Samples {
			if s.StartUs < out.TimeMin {
				out.TimeMin = s.StartUs
			}
			if s.EndUs > out.TimeMax {
				out.TimeMax = s.EndUs
			}
		}
	}
	if len(out.Samples) == 0 {
		out.TimeMin, out.TimeMax = 0, 0
	}
	return out
}
