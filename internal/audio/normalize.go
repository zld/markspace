// Package audio performs deterministic sample-rate conversion, channel
// layout mapping and loudness normalization on interleaved float64 PCM.
// Resampling keeps its phase state across segments so a stream chopped into
// segments produces the same output as one-shot processing.
package audio

import (
	"fmt"
	"math"

	"mediapipe/internal/clock"
)

// Layout is a channel count with a fixed speaker assignment.
type Layout int

const (
	Mono   Layout = 1
	Stereo Layout = 2
)

func (l Layout) Channels() int { return int(l) }

// Resampler is a streaming linear-interpolation sample-rate converter. The
// global output position is computed from the emitted-frame counter, so
// chunk boundaries never affect the result and the total frame count tracks
// the theoretical value with sub-frame error.
type Resampler struct {
	InRate   int
	OutRate  int
	Channels int

	acc      *clock.Accumulator
	emitted  int64
	consumed int64
	last     []float64 // final input frame of the previous chunk
}

// NewResampler builds a Resampler; rates must be positive.
func NewResampler(inRate, outRate, channels int) (*Resampler, error) {
	if inRate <= 0 || outRate <= 0 || channels <= 0 {
		return nil, fmt.Errorf("resampler: bad rates %d->%d ch=%d", inRate, outRate, channels)
	}
	return &Resampler{
		InRate:   inRate,
		OutRate:  outRate,
		Channels: channels,
		acc:      clock.NewAccumulator(int64(inRate), int64(outRate)),
	}, nil
}

// ExpectedOut returns the theoretical output frame count for inFrames.
func ExpectedOut(inFrames int64, inRate, outRate int) int64 {
	return inFrames * int64(outRate) / int64(inRate)
}

// Process converts one chunk of interleaved input and returns interleaved
// output. Frames beyond the chunk end read as zero (fixed tail rule), which
// only affects the final interpolation interval of the stream.
func (r *Resampler) Process(in []float64) []float64 {
	frames := int64(len(in) / r.Channels)
	n := r.acc.Next(frames)
	out := make([]float64, 0, n*int64(r.Channels))
	step := float64(r.InRate) / float64(r.OutRate)
	for k := int64(0); k < n; k++ {
		pos := float64(r.emitted+k)*step - float64(r.consumed)
		i0 := int64(math.Floor(pos))
		frac := pos - float64(i0)
		for ch := 0; ch < r.Channels; ch++ {
			a := r.sampleAt(in, frames, i0, ch)
			b := r.sampleAt(in, frames, i0+1, ch)
			out = append(out, a+(b-a)*frac)
		}
	}
	r.emitted += n
	r.consumed += frames
	if frames > 0 {
		r.last = append(r.last[:0], in[(frames-1)*int64(r.Channels):frames*int64(r.Channels)]...)
	}
	return out
}

// sampleAt reads frame i (relative to chunk start) with boundary rules:
// -1 is the previous chunk's last frame, >=frames is zero (tail pad).
func (r *Resampler) sampleAt(in []float64, frames, i int64, ch int) float64 {
	switch {
	case i < 0:
		if r.last == nil {
			return 0
		}
		return r.last[ch]
	case i >= frames:
		return 0
	default:
		return in[i*int64(r.Channels)+int64(ch)]
	}
}

// Mapping describes one channel-layout conversion.
type Mapping struct {
	From   Layout
	To     Layout
	Matrix [][]float64 // [out][in]
	Filled []int       // output channels silently filled with zero
	Note   string
}

// ChannelMatrix returns the fixed conversion matrix between layouts.
func ChannelMatrix(from, to Layout) (Mapping, error) {
	m := Mapping{From: from, To: to}
	switch {
	case from == to:
		n := from.Channels()
		m.Matrix = make([][]float64, n)
		for i := range m.Matrix {
			m.Matrix[i] = make([]float64, n)
			m.Matrix[i][i] = 1
		}
		m.Note = "identity"
	case from == Mono && to == Stereo:
		m.Matrix = [][]float64{{1}, {1}}
		m.Note = "mono duplicated to stereo"
	case from == Stereo && to == Mono:
		m.Matrix = [][]float64{{0.5, 0.5}}
		m.Note = "stereo downmixed to mono"
	default:
		return m, fmt.Errorf("unsupported layout conversion %d -> %d", from, to)
	}
	return m, nil
}

// ApplyMap converts interleaved samples between layouts using the matrix.
func ApplyMap(m Mapping, in []float64) []float64 {
	inCh := m.From.Channels()
	outCh := m.To.Channels()
	frames := len(in) / inCh
	out := make([]float64, frames*outCh)
	for f := 0; f < frames; f++ {
		for oc := 0; oc < outCh; oc++ {
			var v float64
			for ic := 0; ic < inCh; ic++ {
				v += m.Matrix[oc][ic] * in[f*inCh+ic]
			}
			out[f*outCh+oc] = v
		}
	}
	return out
}

// SilenceFill builds a mapping that places the input at channel 0 and fills
// every other target channel with silence, recording the fill.
func SilenceFill(from, to Layout) Mapping {
	m := Mapping{From: from, To: to, Note: "missing target channels filled with silence"}
	n := to.Channels()
	m.Matrix = make([][]float64, n)
	for oc := 0; oc < n; oc++ {
		m.Matrix[oc] = make([]float64, from.Channels())
		if oc < from.Channels() {
			m.Matrix[oc][oc] = 1
		} else {
			m.Filled = append(m.Filled, oc)
		}
	}
	return m
}

// LoudnessLUFS approximates integrated loudness as RMS level with the
// K-weighting offset (-0.691 dB). Silence yields -Inf.
func LoudnessLUFS(samples []float64) float64 {
	if len(samples) == 0 {
		return math.Inf(-1)
	}
	var sum float64
	for _, s := range samples {
		sum += s * s
	}
	ms := sum / float64(len(samples))
	if ms == 0 {
		return math.Inf(-1)
	}
	return -0.691 + 10*math.Log10(ms)
}

// ApplyGain scales samples by a dB gain.
func ApplyGain(samples []float64, gainDB float64) {
	g := math.Pow(10, gainDB/20)
	for i := range samples {
		samples[i] *= g
	}
}

// TruePeak estimates the inter-sample peak with 4x linear oversampling and
// returns it as a linear amplitude.
func TruePeak(samples []float64, channels int) float64 {
	frames := len(samples) / channels
	var peak float64
	for f := 0; f < frames; f++ {
		for c := 0; c < channels; c++ {
			cur := math.Abs(samples[f*channels+c])
			if cur > peak {
				peak = cur
			}
			if f+1 < frames {
				next := math.Abs(samples[(f+1)*channels+c])
				for k := 1; k < 4; k++ {
					v := cur + (next-cur)*float64(k)/4
					if v > peak {
						peak = v
					}
				}
			}
		}
	}
	return peak
}

// LimitTruePeak scales samples down so the true peak does not exceed
// ceilDBTP. It returns the attenuation applied in dB (0 when none).
func LimitTruePeak(samples []float64, channels int, ceilDBTP float64) float64 {
	peak := TruePeak(samples, channels)
	if peak == 0 {
		return 0
	}
	ceil := math.Pow(10, ceilDBTP/20)
	if peak <= ceil {
		return 0
	}
	att := 20 * math.Log10(ceil/peak)
	ApplyGain(samples, att)
	return att
}

// S16LEToFloat converts interleaved little-endian int16 PCM to float64.
func S16LEToFloat(b []byte) []float64 {
	out := make([]float64, len(b)/2)
	for i := range out {
		v := int16(b[2*i]) | int16(b[2*i+1])<<8
		out[i] = float64(v) / 32768
	}
	return out
}

// FloatToS16LE converts float64 samples to interleaved little-endian int16,
// clamping to full scale.
func FloatToS16LE(in []float64) []byte {
	out := make([]byte, len(in)*2)
	for i, s := range in {
		if s > 1 {
			s = 1
		} else if s < -1 {
			s = -1
		}
		v := int16(math.Round(s * 32767))
		out[2*i] = byte(v)
		out[2*i+1] = byte(v >> 8)
	}
	return out
}
