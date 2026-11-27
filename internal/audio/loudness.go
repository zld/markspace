// Package audio: loudness measurement (ITU-R BS.1770-4).
package audio

import (
	"math"
	"sort"
)

// --- loudness (ITU-R BS.1770-4) ---

type biquad struct{ b0, b1, b2, a1, a2 float64 }

func (q biquad) process(x float64, z1, z2 *float64) float64 {
	y := q.b0*x + *z1
	*z1 = q.b1*x - q.a1*y + *z2
	*z2 = q.b2*x - q.a2*y
	return y
}

// kWeighting returns the two BS.1770 K-weighting stages for an arbitrary
// rate.  The coefficients are designed from the standard corner frequencies
// (a +4 dB high shelf at 1681.97 Hz and a 38.13 Hz high-pass) with the
// Audio-EQ-Cookbook formulation, which reproduces the tabulated 48 kHz
// coefficients to within rounding.
func kWeighting(rate int) (highShelf, highPass biquad) {
	fs := float64(rate)
	// Stage 1: high shelf, f0 = 1681.97 Hz, gain +3.999 dB, S = 1.
	{
		f0 := 1681.974450955533
		gainDB := 3.999843853973347
		a := math.Pow(10, gainDB/40)
		w0 := 2 * math.Pi * f0 / fs
		alpha := math.Sin(w0) / 2 * math.Sqrt((a+1/a)*(1/1.0-1)+2)
		cosw := math.Cos(w0)
		sq := 2 * math.Sqrt(a) * alpha
		b0 := a * ((a + 1) + (a-1)*cosw + sq)
		b1 := -2 * a * ((a - 1) + (a+1)*cosw)
		b2 := a * ((a + 1) + (a-1)*cosw - sq)
		a0 := (a + 1) - (a-1)*cosw + sq
		a1 := 2 * ((a - 1) - (a+1)*cosw)
		a2 := (a + 1) - (a-1)*cosw - sq
		highShelf = biquad{b0: b0 / a0, b1: b1 / a0, b2: b2 / a0, a1: a1 / a0, a2: a2 / a0}
	}
	// Stage 2: high-pass, f0 = 38.13547087602444 Hz, Q = 0.5.
	{
		f0 := 38.13547087602444
		q := 0.5
		w0 := 2 * math.Pi * f0 / fs
		alpha := math.Sin(w0) / (2 * q)
		cosw := math.Cos(w0)
		b0 := (1 + cosw) / 2
		b1 := -(1 + cosw)
		b2 := (1 + cosw) / 2
		a0 := 1 + alpha
		a1 := -2 * cosw
		a2 := 1 - alpha
		highPass = biquad{b0: b0 / a0, b1: b1 / a0, b2: b2 / a0, a1: a1 / a0, a2: a2 / a0}
	}
	return highShelf, highPass
}

func kWeight(samples []float64, rate int) []float64 {
	shelf, hp := kWeighting(rate)
	out := make([]float64, len(samples))
	var s1, s2, h1, h2 float64
	for i, x := range samples {
		y := shelf.process(x, &s1, &s2)
		out[i] = hp.process(y, &h1, &h2)
	}
	return out
}

// channelWeight returns the BS.1770 weight for a canonical channel name.
func channelWeight(name string) float64 {
	switch name {
	case "L", "R", "C":
		return 1.0
	case "Ls", "Rs":
		return 1.41
	case "LFE":
		return 0.0
	}
	return 1.0
}

// Loudness returns the gated integrated loudness in LUFS, or -Inf when the
// programme is silent.
func Loudness(p PCM) float64 {
	rate := p.Format.Rate
	if rate <= 0 || p.Frames() == 0 {
		return math.Inf(-1)
	}
	names, err := p.Format.Layout.Channels()
	if err != nil {
		return math.Inf(-1)
	}
	block := rate / 5 // 200 ms
	if block < 1 {
		block = 1
	}
	step := block / 4 // 75 % overlap
	if step < 1 {
		step = 1
	}
	weighted := make([][]float64, len(p.Channels))
	for i, ch := range p.Channels {
		weighted[i] = kWeight(ch, rate)
	}
	type measurement struct{ energy, weight float64 }
	var blocks []measurement
	for start := 0; start+block <= p.Frames(); start += step {
		var sum float64
		for c := range weighted {
			w := channelWeight(names[c])
			if w == 0 {
				continue
			}
			var e float64
			for i := start; i < start+block; i++ {
				v := weighted[c][i]
				e += v * v
			}
			sum += w * e / float64(block)
		}
		blocks = append(blocks, measurement{energy: sum, weight: 1})
	}
	if len(blocks) == 0 {
		return math.Inf(-1)
	}
	// Absolute gate at -70 LUFS.
	const absoluteGate = 1.172465303e-7 // 10^((-70 + 0.691)/10)
	var gated []measurement
	for _, b := range blocks {
		if b.energy > absoluteGate {
			gated = append(gated, b)
		}
	}
	if len(gated) == 0 {
		return math.Inf(-1)
	}
	var mean float64
	for _, b := range gated {
		mean += b.energy
	}
	mean /= float64(len(gated))
	// Relative gate 10 LU below the ungated-absolute mean.
	relativeGate := mean * 0.1
	var final float64
	n := 0
	for _, b := range gated {
		if b.energy > relativeGate {
			final += b.energy
			n++
		}
	}
	if n == 0 || final <= 0 {
		return math.Inf(-1)
	}
	final /= float64(n)
	return -0.691 + 10*math.Log10(final)
}

// SortGaps orders gap records by sample position and merges overlaps so the
// report is canonical regardless of discovery order.
func SortGaps(gaps []Gap) []Gap {
	out := append([]Gap(nil), gaps...)
	sort.Slice(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	merged := out[:0]
	for _, g := range out {
		if g.Length <= 0 {
			continue
		}
		if n := len(merged); n > 0 {
			prev := &merged[n-1]
			if g.Start <= prev.Start+prev.Length {
				end := g.Start + g.Length
				if pe := prev.Start + prev.Length; end > pe {
					prev.Length = end - prev.Start
				}
				continue
			}
		}
		merged = append(merged, g)
	}
	return merged
}

func round3(v float64) float64 {
	if math.IsInf(v, 0) || math.IsNaN(v) {
		return v
	}
	return math.Round(v*1000) / 1000
}

func round6(v float64) float64 {
	if math.IsInf(v, 0) || math.IsNaN(v) {
		return v
	}
	return math.Round(v*1e6) / 1e6
}
