package audio

import (
	"errors"
)

// Channel identifies a speaker position in an interleaved PCM layout.
type Channel uint8

const (
	ChL   Channel = iota // front left
	ChR                  // front right
	ChC                  // centre
	ChLFE                // low frequency effects
	ChLs                 // surround left
	ChRs                 // surround right
)

// Layout is the ordered list of speaker positions in one interleaved frame.
type Layout struct {
	name     string
	channels []Channel
}

// Channels returns the ordered channel positions.
func (l Layout) Channels() []Channel { return l.channels }

// Count returns the number of channels.
func (l Layout) Count() int { return len(l.channels) }

// Name returns the human-readable layout name.
func (l Layout) Name() string { return l.name }

// Standard layouts use the SMPTE channel order.
var (
	Mono       = Layout{"mono", []Channel{ChC}}
	Stereo     = Layout{"stereo", []Channel{ChL, ChR}}
	Surround51 = Layout{"5.1", []Channel{ChL, ChR, ChC, ChLFE, ChLs, ChRs}}
)

// MissingPolicy selects how a target channel absent from the source is built.
type MissingPolicy uint8

const (
	// FillZero inserts digital silence for every missing channel. This is the
	// default, fully deterministic and energy-preserving for present channels.
	FillZero MissingPolicy = iota
	// Downmix synthesises missing channels from present ones using the fixed
	// integer fold-down rules documented on foldDown.
	Downmix
)

// NormalizeResult reports the normalised frames and bookkeeping needed to
// audit the mapping.
type NormalizeResult struct {
	Out        []int16 // interleaved frames in the target layout
	Frames     int
	ZeroFilled int // target channels filled with silence
	Dropped    int // source channels with no target position (clipped away)
	Downmixed  int // target channels produced by fold-down
	Saturated  int // samples clamped to the int16 range
}

// indexMap maps each target channel position to its source index, or -1.
func sourceIndex(src Layout, ch Channel) int {
	for i, c := range src.channels {
		if c == ch {
			return i
		}
	}
	return -1
}

// Normalize converts interleaved PCM16 from src to dst. The same deterministic
// rule is applied per frame so output hashes are stable across runs.
func Normalize(in []int16, frames int, src, dst Layout, policy MissingPolicy) (*NormalizeResult, error) {
	if frames < 0 {
		return nil, errors.New("audio: negative frame count")
	}
	if len(in) != frames*src.Count() {
		return nil, errors.New("audio: input length does not match source layout/frames")
	}
	res := &NormalizeResult{Frames: frames}
	out := make([]int16, 0, frames*dst.Count())

	for f := 0; f < frames; f++ {
		frame := in[f*src.Count() : (f+1)*src.Count()]
		for _, target := range dst.channels {
			if idx := sourceIndex(src, target); idx >= 0 {
				out = append(out, frame[idx])
				continue
			}
			// target channel absent in the source
			switch policy {
			case FillZero:
				out = append(out, 0)
				res.ZeroFilled++
			case Downmix:
				v, used, sat := foldDown(target, frame, src)
				if used {
					out = append(out, v)
					res.Downmixed++
					if sat {
						res.Saturated++
					}
				} else {
					out = append(out, 0)
					res.ZeroFilled++
				}
			}
		}
	}

	// Dropped channels: source positions that the target layout cannot place.
	for _, ch := range src.channels {
		if sourceIndex(dst, ch) < 0 && !downmixConsumes(ch, dst) {
			res.Dropped += frames
		}
	}
	res.Out = out
	return res, nil
}

// downmixConsumes reports whether a fold-down into dst uses the given source
// channel (so it is not counted as silently dropped).
func downmixConsumes(ch Channel, dst Layout) bool {
	if dst.Count() == 0 {
		return false
	}
	switch ch {
	case ChL, ChR, ChC, ChLs, ChRs:
		return true
	case ChLFE:
		// LFE is intentionally discarded in fold-down to full-range layouts.
		return false
	default:
		return false
	}
}

// saturate16 clamps an int32 accumulator to the int16 range, counting a clamp.
func saturate16(v int32) (int16, bool) {
	if v > 32767 {
		return 32767, true
	}
	if v < -32768 {
		return -32768, true
	}
	return int16(v), false
}

// foldDown builds one missing target channel using fixed integer weights.
// Rules (all shifts are exact; the LFE channel is never folded up):
//
//	L = L + C/2 + Ls/2
//	R = R + C/2 + Rs/2
//	C = (L + R)/2 + C
//	Ls = L/2, Rs = R/2, LFE = 0 (silence when absent)
func foldDown(target Channel, frame []int16, src Layout) (int16, bool, bool) {
	get := func(ch Channel) (int32, bool) {
		idx := sourceIndex(src, ch)
		if idx < 0 {
			return 0, false
		}
		return int32(frame[idx]), true
	}
	var acc int32
	used := false
	switch target {
	case ChL:
		if v, ok := get(ChL); ok {
			acc, used = acc+v, true
		}
		if v, ok := get(ChC); ok {
			acc, used = acc+v/2, true
		}
		if v, ok := get(ChLs); ok {
			acc, used = acc+v/2, true
		}
	case ChR:
		if v, ok := get(ChR); ok {
			acc, used = acc+v, true
		}
		if v, ok := get(ChC); ok {
			acc, used = acc+v/2, true
		}
		if v, ok := get(ChRs); ok {
			acc, used = acc+v/2, true
		}
	case ChC:
		l, lok := get(ChL)
		r, rok := get(ChR)
		switch {
		case lok || rok:
			// C = (L+R)/2 + C; absent sides contribute zero
			acc, used = (l+r)/2, true
			if v, ok := get(ChC); ok {
				acc += v
			}
		default:
			// standalone centre (mono source) folds onto itself unchanged
			if v, ok := get(ChC); ok {
				acc, used = v, true
			}
		}
	case ChLs:
		if v, ok := get(ChL); ok {
			acc, used = v/2, true
		}
	case ChRs:
		if v, ok := get(ChR); ok {
			acc, used = v/2, true
		}
	case ChLFE:
		return 0, false, false // never synthesise a subwoofer channel
	}
	if !used {
		return 0, false, false
	}
	out, sat := saturate16(acc)
	return out, true, sat
}
