// Package audio converts interleaved PCM16 audio to a target sample rate,
// channel layout and loudness. It performs no codec work: callers decode to
// PCM16 first. Underruns, clipping and undecodable input are counted, never
// silently hidden.
package audio

import (
	"math"

	merr "mediatool/internal/errors"
)

// Layout names the supported channel layouts.
type Layout string

const (
	LayoutMono   Layout = "mono"
	LayoutStereo Layout = "stereo"
)

// Channels returns the channel count for a layout.
func (l Layout) Channels() (int, error) {
	switch l {
	case LayoutMono:
		return 1, nil
	case LayoutStereo:
		return 2, nil
	default:
		return 0, merr.New(merr.CodeLayoutMismatch, "unsupported layout %q", l)
	}
}

// Stats collects per-conversion error and quality counters.
type Stats struct {
	FramesIn     uint64  `json:"frames_in"`
	FramesOut    uint64  `json:"frames_out"`
	Underruns    uint64  `json:"insufficient_data"`
	Undecodable  uint64  `json:"undecodable_samples"`
	Clips        uint64  `json:"clipped_samples"`
	Peak         int32   `json:"peak_amplitude"`
	MeasuredLUFS float64 `json:"measured_lufs"`
}

// Config describes a conversion target.
type Config struct {
	SourceRate   int
	SourceLayout Layout
	TargetRate   int
	TargetLayout Layout
	// TargetLUFS is the integrated loudness goal; 0 disables normalization.
	TargetLUFS float64
	// MinFrames rejects conversions whose input holds fewer frames.
	MinFrames int
}

// Converter is stateless across chunks except for its stats.
type Converter struct {
	cfg   Config
	srcCh int
	dstCh int
	stats Stats
	// Resampler carry: one trailing source frame per source channel.
	carry    []int16
	hasCarry bool
}

// New validates the config and builds a converter.
func New(cfg Config) (*Converter, error) {
	if cfg.SourceRate <= 0 || cfg.TargetRate <= 0 {
		return nil, merr.New(merr.CodeLayoutMismatch, "sample rates must be positive")
	}
	sc, err := cfg.SourceLayout.Channels()
	if err != nil {
		return nil, err
	}
	dc, err := cfg.TargetLayout.Channels()
	if err != nil {
		return nil, err
	}
	return &Converter{cfg: cfg, srcCh: sc, dstCh: dc, carry: make([]int16, sc)}, nil
}

// Stats returns a copy of the running counters.
func (c *Converter) Stats() Stats { return c.stats }

// Convert processes one interleaved PCM16 buffer and returns interleaved
// PCM16 in the target format.
func (c *Converter) Convert(pcm []byte) ([]byte, error) {
	if len(pcm)%2 != 0 {
		c.stats.Undecodable++
		return nil, merr.New(merr.CodeUndecodable, "PCM byte length %d is not 16-bit aligned", len(pcm))
	}
	samples := bytesToInt16(pcm)
	if len(samples)%c.srcCh != 0 {
		c.stats.Undecodable++
		return nil, merr.New(merr.CodeUndecodable,
			"%d samples not divisible by %d source channels", len(samples), c.srcCh)
	}
	framesIn := len(samples) / c.srcCh
	c.stats.FramesIn += uint64(framesIn)
	if framesIn < c.cfg.MinFrames {
		c.stats.Underruns++
		return nil, merr.New(merr.CodeInsufficient,
			"%d frames below minimum %d", framesIn, c.cfg.MinFrames)
	}

	mapped := c.mapChannels(samples, framesIn)
	resampled := c.resample(mapped)
	gain := c.loudnessGain(resampled)
	out := c.applyGain(resampled, gain)

	c.stats.FramesOut += uint64(len(out) / c.dstCh)
	c.stats.MeasuredLUFS = measureLUFS(out, c.dstCh)
	return int16ToBytes(out), nil
}

// mapChannels converts between mono and stereo without inventing content.
func (c *Converter) mapChannels(in []int16, frames int) []int16 {
	out := make([]int16, frames*c.dstCh)
	switch {
	case c.srcCh == c.dstCh:
		copy(out, in)
	case c.srcCh == 2 && c.dstCh == 1: // stereo -> mono midpoint
		for f := 0; f < frames; f++ {
			m := (int32(in[2*f]) + int32(in[2*f+1])) / 2
			out[f] = int16(m)
		}
	case c.srcCh == 1 && c.dstCh == 2: // mono -> stereo, duplicated
		for f := 0; f < frames; f++ {
			out[2*f], out[2*f+1] = in[f], in[f]
		}
	}
	return out
}

// resample performs linear-interpolation rate conversion with a one-frame
// carry across calls so chunk boundaries do not click or drop samples.
func (c *Converter) resample(in []int16) []int16 {
	if c.cfg.SourceRate == c.cfg.TargetRate {
		return append([]int16(nil), in...)
	}
	srcFrames := len(in) / c.dstCh
	ratio := float64(c.cfg.SourceRate) / float64(c.cfg.TargetRate)
	estOut := int(float64(srcFrames) / ratio)
	out := make([]int16, 0, estOut*c.dstCh)

	get := func(f float64) (frame []int16, ok bool) {
		idx := int(math.Floor(f))
		frac := f - float64(idx)
		var a, b []int16
		if idx < 0 {
			if !c.hasCarry {
				return nil, false
			}
			a = c.carry
		} else if idx >= srcFrames {
			return nil, false
		} else {
			a = in[idx*c.dstCh : (idx+1)*c.dstCh]
		}
		if idx+1 < 0 || idx+1 >= srcFrames {
			b = a // hold last value at the trailing edge
		} else {
			b = in[(idx+1)*c.dstCh : (idx+2)*c.dstCh]
		}
		frame = make([]int16, c.dstCh)
		for ch := 0; ch < c.dstCh; ch++ {
			v := float64(a[ch]) + frac*(float64(b[ch])-float64(a[ch]))
			frame[ch] = int16(math.Round(v))
		}
		return frame, true
	}

	// Position in source frames; negative start consumes the carry frame.
	pos := -ratio
	if !c.hasCarry {
		pos = 0
	}
	for {
		frame, ok := get(pos)
		if !ok {
			break
		}
		out = append(out, frame...)
		pos += ratio
		if pos >= float64(srcFrames) {
			break
		}
	}
	if srcFrames > 0 {
		c.carry = append(c.carry[:0], in[(srcFrames-1)*c.dstCh:]...)
		c.hasCarry = true
	}
	return out
}
