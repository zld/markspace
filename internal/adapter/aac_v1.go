package adapter

import (
	"math"

	"medcheck/internal/clock"
)

// AudioSpec is the versioned audio layout and rate written to MP4.
type AudioSpec struct {
	Version    string   `json:"version"`
	Codec      string   `json:"codec"`
	Channels   int      `json:"channels"`
	Layout     string   `json:"layout"`
	SampleRate int      `json:"sample_rate"`
	ChannelMap []string `json:"channel_map"`
}

// AudioInput is PCM plus the declarations observed from RTP/container data.
type AudioInput struct {
	Channels            int
	Layout              string
	SampleRate          int
	ContainerSampleRate int
	Target              AudioSpec
	Samples             []int16
}

// AudioOutput contains converted PCM and the spec preserved in output.
type AudioOutput struct {
	Spec         AudioSpec          `json:"spec"`
	Samples      []int16            `json:"-"`
	InputFrames  int                `json:"input_frames"`
	OutputFrames int                `json:"output_frames"`
	Downmixed    bool               `json:"downmixed"`
	Upmixed      bool               `json:"upmixed"`
	Resampled    bool               `json:"resampled"`
	Errors       []clock.MediaError `json:"errors,omitempty"`
}

// AACV1 is the first versioned AAC/audio configuration adapter.
type AACV1 struct{}

// NewAACV1 creates the adapter.
func NewAACV1() *AACV1 { return &AACV1{} }

// Version returns the exact registry key.
func (a *AACV1) Version() string { return VersionAACV1 }

// ChannelMap returns the explicit label map for a supported layout.
func ChannelMap(channels int, layout string) ([]string, string, bool) {
	switch {
	case channels == 1 || layout == "mono":
		return []string{"FC"}, "mono", true
	case channels == 2 || layout == "stereo":
		return []string{"FL", "FR"}, "stereo", true
	case channels == 6 || layout == "5.1":
		return []string{"FL", "FR", "FC", "LFE", "Ls", "Rs"}, "5.1", true
	case channels == 8 || layout == "7.1":
		return []string{"FL", "FR", "FC", "LFE", "BL", "BR", "SL", "SR"}, "7.1", true
	default:
		return nil, layout, false
	}
}

// Adapt validates/maps channels and sample rate, then converts deterministic PCM.
func (a *AACV1) Adapt(in AudioInput) (AudioOutput, []clock.MediaError) {
	var errs []clock.MediaError
	inMap, layout, ok := ChannelMap(in.Channels, in.Layout)
	if !ok || in.Channels <= 0 {
		err := clock.MediaError{Code: clock.CodeAudioLayoutUnsupported, Stream: clock.StreamAudio,
			Message: "unsupported channel layout"}
		return AudioOutput{Errors: append(errs, err)}, append(errs, err)
	}

	target := in.Target
	if target.Channels == 0 {
		target.Channels = 2
	}
	if target.SampleRate == 0 {
		target.SampleRate = 48000
	}
	target.Version = VersionAACV1
	target.Codec = "AAC"
	outMap, outLayout, ok := ChannelMap(target.Channels, target.Layout)
	if !ok {
		err := clock.MediaError{Code: clock.CodeAudioLayoutUnsupported, Stream: clock.StreamAudio,
			Message: "unsupported target channel layout"}
		errs = append(errs, err)
		return AudioOutput{Errors: errs}, errs
	}
	target.Layout = outLayout
	target.ChannelMap = outMap

	sourceRate := in.SampleRate
	if sourceRate == 0 {
		sourceRate = target.SampleRate
	}
	if in.ContainerSampleRate > 0 && in.ContainerSampleRate != in.SampleRate && in.SampleRate > 0 {
		errs = append(errs, clock.MediaError{
			Code: clock.CodeSampleRateConflict, Stream: clock.StreamAudio,
			Message: "RTP and container sample rates disagree; using explicit target-rate policy",
			Source:  "policy:sample-rate-target-v1",
		})
	}

	framesIn := 0
	if in.Channels > 0 {
		framesIn = len(in.Samples) / in.Channels
	}
	mapped := mapChannels(in.Samples, inMap, in.Channels, outMap, target.Channels)
	out := AudioOutput{Spec: target, InputFrames: framesIn, Errors: errs}
	out.Downmixed = in.Channels > target.Channels
	out.Upmixed = in.Channels < target.Channels
	errs = append(errs, clock.MediaError{
		Code: clock.CodeAudioChannelMapped, Stream: clock.StreamAudio,
		Message: layout + " -> " + outLayout, Source: "channel-map-v1",
	})

	if sourceRate != target.SampleRate && framesIn > 0 {
		out.Samples = resampleLinear(mapped, framesIn, target.Channels, sourceRate, target.SampleRate)
		out.Resampled = true
	} else {
		out.Samples = mapped
	}
	out.OutputFrames = len(out.Samples) / target.Channels
	out.Errors = errs
	return out, errs
}

func mapChannels(in []int16, inLabels []string, inCh int, outLabels []string, outCh int) []int16 {
	frames := 0
	if inCh > 0 {
		frames = len(in) / inCh
	}
	out := make([]int16, frames*outCh)
	for f := 0; f < frames; f++ {
		for oi, label := range outLabels {
			out[f*outCh+oi] = mixLabel(label, in, f*inCh, inLabels, inCh)
		}
	}
	return out
}

func mixLabel(target string, in []int16, base int, labels []string, ch int) int16 {
	find := func(want ...string) (int, bool) {
		for i, label := range labels {
			for _, w := range want {
				if label == w {
					return i, true
				}
			}
		}
		return 0, false
	}
	read := func(label string) float64 {
		if idx, ok := find(label); ok && base+idx < len(in) {
			return float64(in[base+idx])
		}
		return 0
	}
	switch target {
	case "FL":
		if idx, ok := find("FL"); ok {
			return in[base+idx]
		}
		return clamp16(0.707*read("FC") + 0.707*read("Ls"))
	case "FR":
		if idx, ok := find("FR"); ok {
			return in[base+idx]
		}
		return clamp16(0.707*read("FC") + 0.707*read("Rs"))
	case "FC":
		if idx, ok := find("FC"); ok {
			return in[base+idx]
		}
		return clamp16((read("FL") + read("FR")) / 2)
	case "LFE":
		if idx, ok := find("LFE"); ok {
			return in[base+idx]
		}
		return 0
	default: // surround labels: use same source or a quiet stereo mix
		if idx, ok := find(target); ok {
			return in[base+idx]
		}
		return clamp16(0.5*read("FL") - 0.5*read("FR"))
	}
}

func resampleLinear(in []int16, frames, ch, inRate, outRate int) []int16 {
	if frames == 0 || inRate <= 0 || outRate <= 0 {
		return nil
	}
	outFrames := int(math.Round(float64(frames) * float64(outRate) / float64(inRate)))
	out := make([]int16, outFrames*ch)
	for n := 0; n < outFrames; n++ {
		pos := float64(n) * float64(inRate) / float64(outRate)
		i := int(pos)
		frac := pos - float64(i)
		for c := 0; c < ch; c++ {
			a := sampleAt(in, i, ch, c, frames)
			b := sampleAt(in, i+1, ch, c, frames)
			out[n*ch+c] = clamp16((1-frac)*float64(a) + frac*float64(b))
		}
	}
	return out
}

func sampleAt(in []int16, frame, ch, c, frames int) float64 {
	if frame < 0 {
		frame = 0
	}
	if frame >= frames {
		frame = frames - 1
	}
	idx := frame*ch + c
	if idx >= len(in) {
		return 0
	}
	return float64(in[idx])
}

func clamp16(v float64) int16 {
	if v > math.MaxInt16 {
		return math.MaxInt16
	}
	if v < math.MinInt16 {
		return math.MinInt16
	}
	return int16(math.Round(v))
}

func equalLabels(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
