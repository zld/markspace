package timeline

import (
	"fmt"

	"archivist/internal/model"
)

// Reference audio profile used for normalization decisions.
const (
	RefSampleRate uint32 = 48000
	RefChannels   uint16 = 2
)

// canonicalSpeakerOrder maps channel counts to standard ITU/RFC speaker order.
var canonicalSpeakerOrder = map[uint16][]string{
	1: {"C"},
	2: {"L", "R"},
	6: {"L", "R", "C", "LFE", "Ls", "Rs"},
	8: {"L", "R", "C", "LFE", "Ls", "Rs", "Lb", "Rb"},
}

// NormalizeLayout validates a channel layout and returns the canonical label
// list plus a decision record. Unknown counts get generic CHn labels.
func NormalizeLayout(trackID uint32, in model.ChannelLayout) (model.ChannelLayout, model.Decision) {
	out := model.ChannelLayout{
		Count:      in.Count,
		RawTag:     in.RawTag,
		SampleRate: in.SampleRate,
	}
	if labels, ok := canonicalSpeakerOrder[in.Count]; ok {
		out.Labels = labels
		out.Valid = true
	} else {
		out.Labels = make([]string, in.Count)
		for i := range out.Labels {
			out.Labels[i] = fmt.Sprintf("CH%d", i+1)
		}
		out.Valid = false
	}
	action := "passthrough"
	reason := "layout matches canonical speaker order"
	if !out.Valid {
		action = "warning"
		reason = fmt.Sprintf("non-standard channel count %d; generic labels assigned", in.Count)
	}
	return out, model.Decision{
		Scope:  fmt.Sprintf("audio:%d", trackID),
		Field:  "channel-layout",
		Input:  fmt.Sprintf("count=%d tag=%d", in.Count, in.RawTag),
		Output: joinLabels(out.Labels),
		Action: action,
		Reason: reason,
	}
}

// NormalizeSampleRate records the mapping decision between a track rate and
// the 48 kHz reference. It only DESCRIBES the conversion; the kernel never
// resamples audio (spec function 4).
func NormalizeSampleRate(trackID uint32, rate uint32) (model.Rat, model.Decision, model.Warning) {
	ratio, _ := model.NewRat(int64(RefSampleRate), uint64(rate))
	if rate == RefSampleRate {
		return ratio, model.Decision{
			Scope:  fmt.Sprintf("audio:%d", trackID),
			Field:  "sample-rate",
			Input:  fmt.Sprintf("%dHz", rate),
			Output: fmt.Sprintf("%dHz", RefSampleRate),
			Action: "passthrough",
			Reason: "already at reference rate",
		}, model.Warning{}
	}
	w := model.Warning{
		Scope:   fmt.Sprintf("audio:%d", trackID),
		Code:    "sample-rate-mismatch",
		Message: fmt.Sprintf("track rate %dHz differs from reference %dHz; ratio recorded, no resampling performed", rate, RefSampleRate),
	}
	return ratio, model.Decision{
		Scope:    fmt.Sprintf("audio:%d", trackID),
		Field:    "sample-rate",
		Input:    fmt.Sprintf("%dHz", rate),
		Output:   fmt.Sprintf("%dHz (described only)", RefSampleRate),
		Action:   "map",
		Priority: 10,
		Reason:   "exact rational conversion ratio recorded; signal not transcoded",
	}, w
}

// ColorDecision resolves conflicting color metadata by track priority. The
// first (lower-index / higher-priority) representation wins; conflicts and
// missing values become explicit warnings rather than silent assumptions.
func ColorDecision(trackID uint32, priority int, color, ref model.ColorMeta) (model.Decision, []model.Warning) {
	scope := fmt.Sprintf("color:%d", trackID)
	if !color.Present {
		return model.Decision{
				Scope: scope, Field: "color", Action: "warning", Priority: priority,
				Reason: "color metadata absent; no primaries assumed",
			}, []model.Warning{{
				Scope: scope, Code: "color-missing",
				Message: "video track carries no colr box; color space unconstrained",
			}}
	}
	if ref.Present && (color.Primaries != ref.Primaries ||
		color.Transfer != ref.Transfer || color.Matrix != ref.Matrix) {
		return model.Decision{
				Scope:    scope,
				Field:    "color",
				Input:    fmt.Sprintf("%d/%d/%d", color.Primaries, color.Transfer, color.Matrix),
				Output:   fmt.Sprintf("%d/%d/%d", ref.Primaries, ref.Transfer, ref.Matrix),
				Action:   "conflict-resolved",
				Priority: priority,
				Reason:   "conflicting color metadata resolved by track priority order",
			}, []model.Warning{{
				Scope: scope, Code: "color-conflict",
				Message: fmt.Sprintf("color %d/%d/%d conflicts with reference %d/%d/%d",
					color.Primaries, color.Transfer, color.Matrix,
					ref.Primaries, ref.Transfer, ref.Matrix),
			}}
	}
	return model.Decision{
		Scope:    scope,
		Field:    "color",
		Input:    fmt.Sprintf("%d/%d/%d", color.Primaries, color.Transfer, color.Matrix),
		Output:   fmt.Sprintf("%d/%d/%d", color.Primaries, color.Transfer, color.Matrix),
		Action:   "passthrough",
		Priority: priority,
		Reason:   "color metadata consistent across representations",
	}, nil
}

func joinLabels(labels []string) string {
	out := ""
	for i, l := range labels {
		if i > 0 {
			out += ","
		}
		out += l
	}
	return out
}
