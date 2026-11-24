// Package audio performs the deterministic PCM transforms: resampling,
// channel-layout normalization and loudness/peak control. All processing is
// in-memory and pure so identical inputs always produce identical hashes.
package audio

// Layout names accepted on the command line.
const (
	LayoutMono   = "mono"
	LayoutStereo = "stereo"
)

// ChannelsFor maps a layout name to its channel count; 0 means unknown.
func ChannelsFor(layout string) int {
	switch layout {
	case LayoutMono:
		return 1
	case LayoutStereo:
		return 2
	default:
		return 0
	}
}

// NormalizeLayout converts interleaved int16 PCM from srcCh channels to the
// target layout (1 or 2 channels). Down-mix uses the standard L+R average;
// up-mix duplicates the mono channel. No input sample is read past its end.
func NormalizeLayout(in []int16, srcCh, dstCh int) []int16 {
	if srcCh < 1 || dstCh < 1 {
		return nil
	}
	if srcCh == dstCh {
		out := make([]int16, len(in))
		copy(out, in)
		return out
	}
	frames := len(in) / srcCh
	if dstCh == 1 { // any multichannel source -> mono average
		out := make([]int16, frames)
		for f := 0; f < frames; f++ {
			var sum int32
			for c := 0; c < srcCh; c++ {
				sum += int32(in[f*srcCh+c])
			}
			out[f] = int16(sum / int32(srcCh))
		}
		return out
	}
	// mono -> stereo (or mono -> wider layouts by duplication)
	out := make([]int16, frames*dstCh)
	for f := 0; f < frames; f++ {
		for c := 0; c < dstCh; c++ {
			out[f*dstCh+c] = in[f*srcCh]
		}
	}
	return out
}
