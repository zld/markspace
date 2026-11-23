// Package testdata generates deterministic, fully local synthetic media
// fixtures. No external files are referenced: every byte is derived from a
// numeric seed so repeated runs produce byte-identical material.
package testdata

import (
	"math/rand"
)

// AudioContent is a generated mono PCM tone track with known silence windows.
type AudioContent struct {
	PCM  []int16
	Rate uint32
	// Silence lists [start,end) sample-index ranges forced to digital zero.
	Silence [][2]int
}

// FrameContent is one generated luma frame.
type FrameContent struct {
	Index   int
	Key     bool
	Width   int
	Height  int
	Luma    []byte // Width*Height deterministic luma samples
}

// NewRand returns the canonical seeded RNG used by every fixture builder.
func NewRand(seed int64) *rand.Rand {
	// rand.New is deterministic for a given seed and needs no runtime entropy.
	return rand.New(rand.NewSource(seed))
}

// NewAudio synthesizes a 440 Hz mono tone at the given rate, with two
// deterministic silence windows so silence detection has known ground truth.
func NewAudio(seed int64, rate uint32, durationNanos int64) AudioContent {
	total := int(durationNanos) * int(rate) / 1_000_000_000
	pcm := make([]int16, total)
	const amplitude = 12000
	const twoPiF = 2.0 * 3.141592653589793 * 440.0
	dt := 1.0 / float64(rate)
	for i := 0; i < total; i++ {
		v := amplitude * sinApprox(twoPiF*dt*float64(i))
		// Tiny deterministic harmonic keeps the signal non-trivial.
		v += amplitude * 0.05 * sinApprox(twoPiF*2.0*dt*float64(i)+1.0)
		if v > 32767 {
			v = 32767
		} else if v < -32768 {
			v = -32768
		}
		pcm[i] = int16(v)
	}
	// Two silence windows: [20%,26%) and [60%,64%) of the clip.
	silence := [][2]int{
		{total / 5, total / 5 + (total * 6 / 100)},
		{total * 3 / 5, total*3/5 + (total * 4 / 100)},
	}
	for _, w := range silence {
		for i := w[0]; i < w[1] && i < total; i++ {
			pcm[i] = 0
		}
	}
	return AudioContent{PCM: pcm, Rate: rate, Silence: silence}
}

// NewFrames generates n deterministic luma frames with a fixed GOP pattern.
func NewFrames(seed int64, n, width, height, keyEvery int) []FrameContent {
	if keyEvery <= 0 {
		keyEvery = 12
	}
	out := make([]FrameContent, n)
	plen := width * height
	for f := 0; f < n; f++ {
		r := rand.New(rand.NewSource(seed*1_000_003 + int64(f)*7919))
		luma := make([]byte, plen)
		// Base gradient plus per-frame seeded noise: identical frame indices
		// across runs hash identically, which gives fingerprint ground truth.
		base := byte(f * 17)
		for i := range luma {
			luma[i] = base + byte(i%47) + byte(r.Intn(1<<12)%31)
		}
		out[f] = FrameContent{
			Index:  f,
			Key:    f%keyEvery == 0,
			Width:  width,
			Height: height,
			Luma:   luma,
		}
	}
	return out
}

// VFRTiming builds variable frame-rate presentation durations in nanoseconds.
// When dupPTSIndex >= 0 that frame repeats its predecessor's PTS. The clip
// always starts at startNanos (which may be non-zero).
func VFRTiming(r *rand.Rand, n int, avgDurNanos, startNanos int64, dupPTSIndex int) (pts, dts, dur []int64) {
	pts = make([]int64, n)
	dts = make([]int64, n)
	dur = make([]int64, n)
	cur := startNanos
	for i := 0; i < n; i++ {
		// Jitter within +/-25% of the average frame duration.
		jitter := int64(r.Intn(int(avgDurNanos/2) + 1)) - avgDurNanos/4
		d := avgDurNanos + jitter
		if d <= 0 {
			d = avgDurNanos
		}
		dur[i] = d
		dts[i] = cur
		if i == dupPTSIndex && i > 0 {
			pts[i] = pts[i-1] // deliberate duplicate PTS
		} else {
			pts[i] = cur
		}
		cur += d
	}
	return pts, dts, dur
}

// CFRDurations returns constant frame durations for the simple cases.
func CFRDurations(n int, durNanos int64) []int64 {
	out := make([]int64, n)
	for i := range out {
		out[i] = durNanos
	}
	return out
}

// sinApprox avoids pulling in math for a stable, deterministic sine. The
// fixtures only need a smooth periodic wave, not transcendental accuracy.
func sinApprox(x float64) float64 {
	// Range-reduce to [-pi, pi].
	const pi = 3.141592653589793
	const twoPi = 2 * pi
	x -= twoPi * float64(int64(x/twoPi))
	if x > pi {
		x -= twoPi
	} else if x < -pi {
		x += twoPi
	}
	// Bhaskara I approximation, max error ~1.9e-3: ample for PCM fixtures.
	abs := x
	if abs < 0 {
		abs = -abs
	}
	num := 16.0 * x * (pi - abs)
	den := 5.0*pi*pi - 4.0*abs*(pi-abs)
	return num / den
}
