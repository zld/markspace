package audio

import (
	"github.com/example/mediad/internal/model"
)

// Layout names the supported target channel layouts. Channel ordering follows
// the common WAV/PCM convention: mono = [C], stereo = [L, R].
type Layout string

const (
	LayoutMono   Layout = "mono"
	LayoutStereo Layout = "stereo"
)

// Channels returns the frame channel count for a layout.
func (l Layout) Channels() int {
	switch l {
	case LayoutMono:
		return 1
	case LayoutStereo:
		return 2
	default:
		return 0
	}
}

// Valid reports whether l is a supported target layout.
func (l Layout) Valid() bool { return l.Channels() > 0 }

// downmix coefficients are fixed powers of two so every reduction is
// deterministic and reproducible without floating-point tuning.
var stereoToMono = [2]float64{0.5, 0.5}

// Normalize rewrites interleaved PCM to the target layout. Mono->stereo
// duplicates the center channel; stereo->mono applies the fixed equal-power
// downmix; matching layouts pass through unchanged.
func Normalize(in Buffer, target Layout) (Buffer, error) {
	if !target.Valid() {
		return Buffer{}, model.NewError(model.CodeIllegalPacket, model.StageLayout,
			0, -1, "layout: unsupported target "+string(target))
	}
	want := target.Channels()
	if in.Ch == want {
		return in, nil
	}
	out := NewBuffer(in.Frames, want)
	switch {
	case in.Ch == 1 && want == 2:
		for f := 0; f < in.Frames; f++ {
			c := in.Samples[f]
			out.Samples[f*2] = c
			out.Samples[f*2+1] = c
		}
	case in.Ch == 2 && want == 1:
		for f := 0; f < in.Frames; f++ {
			l := in.Samples[f*2]
			r := in.Samples[f*2+1]
			out.Samples[f] = l*stereoToMono[0] + r*stereoToMono[1]
		}
	default:
		return Buffer{}, model.NewError(model.CodeIllegalPacket, model.StageLayout,
			0, -1, "layout: no deterministic mapping for this channel count")
	}
	return out, nil
}
