package policy

import (
	"fmt"
	"math"

	"continuity/internal/media"
)

// TargetSampleRateHz is the project-wide unified audio sample rate.
const TargetSampleRateHz = 48000

// Minus3dB is the linear amplitude coefficient for center/surround mixing.
var Minus3dB = math.Pow(10.0, -3.0/20.0)

// AudioTrack is the policy package's decoupled audio metadata input.
type AudioTrack struct {
	ID               uint32
	Channels         int
	Layout           string
	LayoutKnown      bool
	InputSampleRate  int
	TargetSampleRate int
}

// MatrixRow explains one output channel's coefficients.
type MatrixRow struct {
	Output       string             `json:"output"`
	Coefficients map[string]float64 `json:"coefficients"`
}

// ChannelMapping is an explainable fixed downmix mapping.
type ChannelMapping struct {
	TrackID      uint32      `json:"track_id"`
	InputLayout  string      `json:"input_layout"`
	OutputLayout string      `json:"output_layout"`
	InputLabels  []string    `json:"input_labels"`
	OutputLabels []string    `json:"output_labels"`
	Matrix       []MatrixRow `json:"matrix"`
	LFEMixed     bool        `json:"lfe_mixed"`
	Reason       string      `json:"reason"`
}

// SampleRateDecision records why resampling is or is not needed.
type SampleRateDecision struct {
	TrackID  uint32 `json:"track_id"`
	InputHz  int    `json:"input_hz"`
	TargetHz int    `json:"target_hz"`
	DeltaHz  int    `json:"delta_hz"`
	Resample bool   `json:"resample"`
	Reason   string `json:"reason"`
}

// AudioPlan contains all audio conversion decisions.
type AudioPlan struct {
	OutputLayout string               `json:"output_layout"`
	MixLFE       bool                 `json:"mix_lfe"`
	Mappings     []ChannelMapping     `json:"channel_mappings"`
	SampleRates  []SampleRateDecision `json:"sample_rates"`
}

var layoutLabels = map[string][]string{
	"mono":   {"FC"},
	"stereo": {"FL", "FR"},
	"5.1":    {"FL", "FR", "FC", "LFE", "SL", "SR"},
}

// PlanAudio rejects unknown layouts rather than guessing channel meaning.
func PlanAudio(tracks []AudioTrack, targetRate int, mixLFE bool) (*AudioPlan, error) {
	if targetRate <= 0 {
		targetRate = TargetSampleRateHz
	}
	plan := &AudioPlan{OutputLayout: "stereo", MixLFE: mixLFE}
	for _, t := range tracks {
		if t.InputSampleRate > 0 {
			plan.SampleRates = append(plan.SampleRates, PlanSampleRate(t.ID, t.InputSampleRate, targetRate))
		}
		if t.Channels == 0 {
			continue
		}
		labels, ok := KnownLayout(t.Layout, t.Channels, t.LayoutKnown)
		if !ok {
			return nil, media.NewError(media.StageAudio, -1, "audio", media.ErrUnknownLayout).
				WithTrack(t.ID).
				WithKind(fmt.Sprintf("channels=%d layout=%q known=%v", t.Channels, t.Layout, t.LayoutKnown))
		}
		mapping, err := DownmixToStereo(t.ID, t.Layout, labels, mixLFE)
		if err != nil {
			return nil, err
		}
		plan.Mappings = append(plan.Mappings, mapping)
	}
	return plan, nil
}

// KnownLayout returns canonical labels only for explicitly known layouts.
func KnownLayout(layout string, channels int, known bool) ([]string, bool) {
	if !known || layout == "" {
		return nil, false
	}
	labels, ok := layoutLabels[layout]
	if !ok || len(labels) != channels {
		return nil, false
	}
	return append([]string(nil), labels...), true
}

// DownmixToStereo applies the fixed 5.1 matrix. Center and surround channels
// are mixed at -3 dB; LFE is omitted unless explicitly enabled.
func DownmixToStereo(trackID uint32, layout string, inputLabels []string, mixLFE bool) (ChannelMapping, error) {
	m := ChannelMapping{
		TrackID:      trackID,
		InputLayout:  layout,
		OutputLayout: "stereo",
		InputLabels:  append([]string(nil), inputLabels...),
		OutputLabels: []string{"FL", "FR"},
		LFEMixed:     mixLFE,
	}
	switch layout {
	case "stereo":
		m.Matrix = []MatrixRow{
			{Output: "FL", Coefficients: map[string]float64{"FL": 1}},
			{Output: "FR", Coefficients: map[string]float64{"FR": 1}},
		}
		m.Reason = "stereo input uses identity channel matrix"
	case "mono":
		m.Matrix = []MatrixRow{
			{Output: "FL", Coefficients: map[string]float64{"FC": 1}},
			{Output: "FR", Coefficients: map[string]float64{"FC": 1}},
		}
		m.Reason = "explicit mono layout is duplicated to stereo"
	case "5.1":
		left := map[string]float64{"FL": 1.0, "FC": Minus3dB, "SL": Minus3dB}
		right := map[string]float64{"FR": 1.0, "FC": Minus3dB, "SR": Minus3dB}
		if mixLFE {
			left["LFE"] = Minus3dB
			right["LFE"] = Minus3dB
			m.Reason = "fixed 5.1-to-stereo matrix; center/surround/LFE at -3 dB because LFE mix was explicitly enabled"
		} else {
			m.Reason = "fixed 5.1-to-stereo matrix; center and surround at -3 dB; LFE excluded by default"
		}
		m.Matrix = []MatrixRow{{Output: "FL", Coefficients: left}, {Output: "FR", Coefficients: right}}
	default:
		return m, media.NewError(media.StageAudio, -1, "audio", media.ErrUnknownLayout).WithTrack(trackID)
	}
	return m, nil
}

// PlanSampleRate records the resampling decision and its rationale.
func PlanSampleRate(trackID uint32, input, target int) SampleRateDecision {
	d := SampleRateDecision{TrackID: trackID, InputHz: input, TargetHz: target, DeltaHz: target - input}
	if input == target {
		d.Reason = fmt.Sprintf("input sample rate %d Hz already matches unified target", input)
		return d
	}
	d.Resample = true
	d.Reason = fmt.Sprintf("resample %d Hz to policy target %d Hz to align all audio clocks", input, target)
	return d
}
