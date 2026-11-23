// Package audio normalizes channel layout, sample rate, and loudness.
// Samples are interleaved float32. Unknown layouts are located errors,
// never silent remixes.
package audio

import "mediacore/media"

// Layout is a declared channel layout.
type Layout int

const (
	LayoutUnknown Layout = iota
	LayoutMono
	LayoutStereo
	LayoutSurround51
)

// ParseLayout maps a declared name to a Layout; anything else is a
// locatable error, not a guess.
func ParseLayout(s string) (Layout, error) {
	switch s {
	case "mono":
		return LayoutMono, nil
	case "stereo":
		return LayoutStereo, nil
	case "5.1":
		return LayoutSurround51, nil
	}
	return LayoutUnknown, media.Fault(media.CodeLayoutUnknown, "audio.ParseLayout", -1,
		"unknown layout %q", s)
}

// Channels returns the channel count; 0 for LayoutUnknown.
func (l Layout) Channels() int {
	switch l {
	case LayoutMono:
		return 1
	case LayoutStereo:
		return 2
	case LayoutSurround51:
		return 6
	}
	return 0
}

func (l Layout) String() string {
	switch l {
	case LayoutMono:
		return "mono"
	case LayoutStereo:
		return "stereo"
	case LayoutSurround51:
		return "5.1"
	}
	return "unknown"
}

// Normalize maps interleaved samples from one declared layout to another.
// Supported: identity, mono↔stereo, 5.1→stereo, 5.1→mono. Any unknown
// layout or unsupported pair is CodeLayoutUnknown — the kernel refuses
// to invent a mix it cannot name.
func Normalize(in []float32, from, to Layout) ([]float32, error) {
	const op = "audio.Normalize"
	fc, tc := from.Channels(), to.Channels()
	if fc == 0 || tc == 0 {
		return nil, media.Fault(media.CodeLayoutUnknown, op, -1,
			"cannot map layout %s to %s", from, to)
	}
	if len(in)%fc != 0 {
		return nil, media.Fault(media.CodeLayoutUnknown, op, int64(len(in)),
			"sample count %d not a multiple of %d channels", len(in), fc)
	}
	if from == to {
		out := make([]float32, len(in))
		copy(out, in)
		return out, nil
	}
	frames := len(in) / fc
	out := make([]float32, frames*tc)
	switch {
	case from == LayoutMono && to == LayoutStereo:
		for f := 0; f < frames; f++ {
			out[2*f], out[2*f+1] = in[f], in[f]
		}
	case from == LayoutStereo && to == LayoutMono:
		for f := 0; f < frames; f++ {
			out[f] = 0.5 * (in[2*f] + in[2*f+1])
		}
	case from == LayoutSurround51 && to == LayoutStereo:
		// ITU-style downmix: L' = L + C/√2 + Ls/√2 (LFE dropped).
		const s = 0.7071
		for f := 0; f < frames; f++ {
			b := in[6*f:]
			out[2*f] = b[0] + s*b[2] + s*b[4]
			out[2*f+1] = b[1] + s*b[2] + s*b[5]
		}
	case from == LayoutSurround51 && to == LayoutMono:
		st, _ := Normalize(in, from, LayoutStereo)
		return Normalize(st, LayoutStereo, LayoutMono)
	default:
		return nil, media.Fault(media.CodeLayoutUnknown, op, -1,
			"unsupported layout mapping %s -> %s", from, to)
	}
	return out, nil
}
