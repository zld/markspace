// Package plc implements packet loss concealment for 48 kHz PCM16 audio:
// missing frames are synthesized by decaying the previous frame, and after
// a run of losses the output switches to deterministic low-amplitude noise.
package plc

import (
	"math"
	"math/rand"
)

const FrameSamples = 480

// Concealer generates concealment frames and measures the boundary jump
// when real audio resumes after a concealed run.
type Concealer struct {
	prev       [FrameSamples]int16
	havePrev   bool
	run        int
	rng        *rand.Rand
	NoiseAfter int // consecutive conceals before switching to noise
}

// New returns a Concealer whose noise floor is deterministic for seed.
func New(seed int64) *Concealer {
	return &Concealer{
		rng:        rand.New(rand.NewSource(seed ^ 0x5eed)),
		NoiseAfter: 5,
	}
}

// Conceal fills out with a concealment frame. The first NoiseAfter frames
// of a run decay the previous frame by 0.8 per step; beyond that the output
// is deterministic low-amplitude noise (|s| <= 200).
func (c *Concealer) Conceal(out []int16) {
	if c.run >= c.NoiseAfter {
		for i := range out {
			out[i] = int16(c.rng.Intn(401) - 200)
		}
	} else {
		gain := math.Pow(0.8, float64(c.run+1))
		for i := range out {
			out[i] = int16(math.Round(float64(c.prev[i]) * gain))
		}
	}
	n := copy(c.prev[:], out)
	for i := n; i < FrameSamples; i++ {
		c.prev[i] = 0
	}
	c.havePrev = true
	c.run++
}

// Real registers a decoded frame. When resuming after a concealed run it
// reports resumed=true and the boundary jump between the last output
// sample and the first sample of pcm.
func (c *Concealer) Real(pcm []int16) (jump int, resumed bool) {
	if c.run > 0 && c.havePrev && len(pcm) > 0 {
		jump = abs(int(pcm[0]) - int(c.prev[FrameSamples-1]))
		resumed = true
	}
	n := copy(c.prev[:], pcm)
	for i := n; i < FrameSamples; i++ {
		c.prev[i] = 0
	}
	c.havePrev = true
	c.run = 0
	return jump, resumed
}

// Run reports the current consecutive concealment count.
func (c *Concealer) Run() int { return c.run }

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
