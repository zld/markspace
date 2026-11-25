// Package audio decides channel-layout and sample-rate normalization.
package audio

import (
	"fmt"

	"replay/internal/model"
)

// Delivery defaults chosen by the planner.
const (
	TargetSampleRate = 48000
	TargetChannels   = 2
)

// Plan is the normalization decision for one audio track.
type Plan struct {
	Input      model.AudioInfo
	Layout     string // output layout name: "mono" or "stereo"
	Channels   int    // output channels
	SampleRate uint32 // output sample rate
	Resample   bool   // sample rate changes
}

// LayoutName maps a channel count to a supported layout name.
func LayoutName(ch int) (string, bool) {
	switch ch {
	case 1:
		return "mono", true
	case 2:
		return "stereo", true
	}
	return "", false
}

// PlanAudio chooses normalization parameters for the given input. Supported
// layouts (mono, stereo) are kept; the sample rate is normalized to
// TargetSampleRate. Unsupported layouts fail explicitly with an error
// wrapping model.ErrUnsupportedLayout; the input is never silently
// downmixed.
func PlanAudio(in model.AudioInfo) (Plan, error) {
	name, ok := LayoutName(in.Channels)
	if !ok {
		return Plan{}, fmt.Errorf("audio: %d channels: %w", in.Channels, model.ErrUnsupportedLayout)
	}
	if in.SampleRate == 0 {
		return Plan{}, fmt.Errorf("audio: zero sample rate: %w", model.ErrUnsupportedLayout)
	}
	p := Plan{
		Input:      in,
		Layout:     name,
		Channels:   in.Channels,
		SampleRate: in.SampleRate,
	}
	if in.SampleRate != TargetSampleRate {
		p.SampleRate = TargetSampleRate
		p.Resample = true
	}
	return p, nil
}
