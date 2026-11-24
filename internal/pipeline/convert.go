package pipeline

import (
	"medcheck/internal/checkpoint"
	"medcheck/internal/clock"
	"medcheck/internal/output"
)

func stringMap(in map[clock.StreamID]int64) map[string]int64 {
	out := make(map[string]int64, len(in))
	for k, v := range in {
		out[string(k)] = v
	}
	return out
}

func boolStringMap(in map[clock.StreamID]bool) map[string]bool {
	out := make(map[string]bool, len(in))
	for k, v := range in {
		out[string(k)] = v
	}
	return out
}

func clockIDMap(in map[string]int64) map[clock.StreamID]int64 {
	out := make(map[clock.StreamID]int64, len(in))
	for k, v := range in {
		out[clock.StreamID(k)] = v
	}
	return out
}

func clockBoolMap(in map[string]bool) map[clock.StreamID]bool {
	out := make(map[clock.StreamID]bool, len(in))
	for k, v := range in {
		out[clock.StreamID(k)] = v
	}
	return out
}

func clockToCheckpoint(st clock.State) checkpoint.ClockState {
	return checkpoint.ClockState{
		Have: st.Have, RateHz: st.RateHz, FirstExt: st.FirstExt, MaxExt: st.MaxExt,
		AnchorExt: st.AnchorExt, AnchorNS: st.AnchorNS,
	}
}

func clockFromCheckpoint(st checkpoint.ClockState) clock.State {
	return clock.State{
		Have: st.Have, RateHz: st.RateHz, FirstExt: st.FirstExt, MaxExt: st.MaxExt,
		AnchorExt: st.AnchorExt, AnchorNS: st.AnchorNS,
	}
}

func toCheckpointSamples(in []output.Sample) []checkpoint.Sample {
	out := make([]checkpoint.Sample, len(in))
	for i, s := range in {
		out[i] = checkpoint.Sample{PTSNS: s.PTSNS, DurationNS: s.DurationNS, Payload: s.Payload, Keyframe: s.Keyframe}
	}
	return out
}

func fromCheckpointSamples(in []checkpoint.Sample) []output.Sample {
	out := make([]output.Sample, len(in))
	for i, s := range in {
		out[i] = output.Sample{PTSNS: s.PTSNS, DurationNS: s.DurationNS, Payload: s.Payload, Keyframe: s.Keyframe}
	}
	return out
}
