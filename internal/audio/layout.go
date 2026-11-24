// Package audio implements the deterministic PCM audio pipeline: channel
// layout normalization, rational sample-rate conversion and loudness control.
// All math uses fixed rounding rules so identical inputs always produce
// identical samples and statistics, run after run.
package audio

import (
	"mediad/internal/model"
)

// Frame is one interleaved little-endian-style PCM s16 block. Samples are
// stored in native int16; channel N of sample period K lives at
// Samples[K*Channels+N].
type Frame struct {
	SampleRate int
	Channels   int
	Samples    []int16
}

// CanonicalChannels is the kernel-wide normalized layout: stereo.
const CanonicalChannels = 2

// Supported input layouts. Anything else is rejected rather than guessed.
const (
	LayoutMono   = 1
	LayoutStereo = 2
	Layout51     = 6 // L R C LFE BL BR
	Layout71     = 8 // L R C LFE BL BR SL SR
)

// downmix coefficients are the fixed ITU-R BS.775-style constants used for
// 5.1/7.1 -> stereo. Stored as exact fractions so the rule is reproducible
// without floating-point surprise: 0.70710678 ~= 7071/10000, 0.5 exact.
const (
	coefCenterNum   = 7071
	coefSurroundNum = 7071
	coefHalfNum     = 5000
	coefDen         = 10000
)

// NormalizeLayout converts any supported layout to CanonicalChannels.
//
// Rules (all deterministic):
//   - mono: the single channel is duplicated to L and R;
//   - stereo: passed through unchanged;
//   - 5.1/7.1: fixed-coefficient downmix, halves rounded away from zero;
//   - an incomplete trailing sample group (short frame) is dropped and its
//     length is reported in the stats;
//   - zero samples is ErrEmptyFrame; unknown layouts are ErrUnsupportedLayout.
func NormalizeLayout(in Frame) (Frame, LayoutStats, error) {
	if in.Channels <= 0 || in.SampleRate <= 0 {
		return Frame{}, LayoutStats{}, model.New(model.ErrUnsupportedLayout,
			"invalid channels=%d rate=%d", in.Channels, in.SampleRate)
	}
	if len(in.Samples) == 0 {
		return Frame{}, LayoutStats{}, model.New(model.ErrEmptyFrame,
			"frame has %d samples", len(in.Samples))
	}
	if !isSupported(in.Channels) {
		return Frame{}, LayoutStats{}, model.New(model.ErrUnsupportedLayout,
			"no normalization rule for %d-channel layout", in.Channels)
	}

	complete := (len(in.Samples) / in.Channels) * in.Channels
	dropped := len(in.Samples) - complete
	out := Frame{SampleRate: in.SampleRate, Channels: CanonicalChannels,
		Samples: make([]int16, 0, (complete/in.Channels)*CanonicalChannels)}

	for pos := 0; pos < complete; pos += in.Channels {
		var l, r int64
		switch in.Channels {
		case LayoutMono:
			l, r = int64(in.Samples[pos]), int64(in.Samples[pos])
		case LayoutStereo:
			l, r = int64(in.Samples[pos]), int64(in.Samples[pos+1])
		case Layout51:
			ch := in.Samples[pos : pos+6 : pos+6]
			l = int64(ch[0]) + mix(ch[2], coefCenterNum) + mix(ch[4], coefSurroundNum)
			r = int64(ch[1]) + mix(ch[2], coefCenterNum) + mix(ch[5], coefSurroundNum)
		case Layout71:
			ch := in.Samples[pos : pos+8 : pos+8]
			l = int64(ch[0]) + mix(ch[2], coefCenterNum) +
				mix(ch[4], coefHalfNum) + mix(ch[6], coefSurroundNum)
			r = int64(ch[1]) + mix(ch[2], coefCenterNum) +
				mix(ch[5], coefHalfNum) + mix(ch[7], coefSurroundNum)
		}
		out.Samples = append(out.Samples, clamp16(l), clamp16(r))
	}
	return out, LayoutStats{
		InputChannels: in.Channels, OutputChannels: CanonicalChannels,
		DroppedSamples: dropped,
	}, nil
}

// LayoutStats reports what normalization did.
type LayoutStats struct {
	InputChannels  int `json:"input_channels"`
	OutputChannels int `json:"output_channels"`
	DroppedSamples int `json:"dropped_samples"` // incomplete trailing group
}

// mix applies sample * num/den with half-away-from-zero rounding. LFE is
// intentionally omitted from the stereo downmix per standard practice.
func mix(sample int16, num int64) int64 {
	v := int64(sample) * num
	if v >= 0 {
		return (v + coefDen/2) / coefDen
	}
	return -((-v + coefDen/2) / coefDen)
}

func clamp16(v int64) int16 {
	switch {
	case v > 32767:
		return 32767
	case v < -32768:
		return -32768
	default:
		return int16(v)
	}
}

func isSupported(channels int) bool {
	switch channels {
	case LayoutMono, LayoutStereo, Layout51, Layout71:
		return true
	}
	return false
}
