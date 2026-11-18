// Package conceal implements the playout-side packet loss concealment
// policy used when the playout clock finds the next frame missing.
//
// The first missing frame is replaced by an extension of the last real
// frame. Further gaps are progressively attenuated by a fixed decay factor
// so the extension does not sound like a frozen sample. Once the
// configured maximum run is exceeded the concealer emits silence markers
// and keeps counting until real audio resumes.
package conceal

import (
	"fmt"

	"jitterlab/internal/packet"
)

// Kind describes what the concealer produced for one playout slot.
type Kind int

const (
	// KindReal is a real frame that arrived in time.
	KindReal Kind = iota
	// KindExtend repeats the previous frame at full gain.
	KindExtend
	// KindDecay repeats the previous frame with progressive attenuation.
	KindDecay
	// KindSilence marks that the conceal limit was exceeded.
	KindSilence
)

func (k Kind) String() string {
	switch k {
	case KindReal:
		return "real"
	case KindExtend:
		return "extend"
	case KindDecay:
		return "decay"
	case KindSilence:
		return "silence"
	default:
		return "unknown"
	}
}

// Output is the payload handed to the encoder/player for one slot.
type Output struct {
	Kind        Kind
	Payload     [packet.PayloadSize]byte
	Run         int     // consecutive concealed slots, 0 for real audio
	Attenuation float64 // gain applied to the extended frame
}

// Concealer holds the last real frame and the current conceal run state.
// It is not safe for concurrent use.
type Concealer struct {
	maxRun int
	decay  float64

	last        [packet.PayloadSize]byte
	hasLast     bool
	run         int
	lastConceal [packet.PayloadSize]byte
}

// New creates a concealer. maxRun is the number of concealed slots before
// silence is emitted; decay is applied per slot after the first extension
// and must be in (0, 1].
func New(maxRun int, decay float64) (*Concealer, error) {
	if maxRun < 1 {
		return nil, fmt.Errorf("conceal: max run %d must be positive", maxRun)
	}
	if decay <= 0 || decay > 1 {
		return nil, fmt.Errorf("conceal: decay %g must be in (0, 1]", decay)
	}
	return &Concealer{maxRun: maxRun, decay: decay}, nil
}

// MaxRun returns the configured conceal limit.
func (c *Concealer) MaxRun() int { return c.maxRun }

// Run returns the current number of consecutive concealed slots.
func (c *Concealer) Run() int { return c.run }

// Present records a real frame and returns it unchanged. When the frame
// follows a conceal run it also reports recovered=true and delta, the mean
// absolute sample jump between the last concealed payload and the real
// frame, expressed in sample units (0..255). delta is 0 otherwise.
func (c *Concealer) Present(f packet.Frame) (out Output, delta float64, recovered bool) {
	if c.run > 0 {
		recovered = true
		delta = jump(c.lastConceal[:], f.Payload[:])
	}
	c.last = f.Payload
	c.hasLast = true
	c.run = 0
	return Output{Kind: KindReal, Payload: f.Payload}, delta, recovered
}

// Conceal produces the payload for one missing playout slot. With no real
// frame to extend it emits silence immediately. The mute path keeps run
// incrementing, so max consecutive conceal length is measurable even when
// the run outlives the conceal limit.
func (c *Concealer) Conceal() Output {
	c.run++
	if !c.hasLast || c.run > c.maxRun {
		out := Output{Kind: KindSilence, Run: c.run}
		c.lastConceal = out.Payload
		return out
	}

	atten := 1.0
	kind := KindExtend
	if c.run > 1 {
		kind = KindDecay
		atten = 1.0
		for i := 1; i < c.run; i++ {
			atten *= c.decay
		}
	}
	var payload [packet.PayloadSize]byte
	for i, b := range c.last {
		payload[i] = attenuate(b, atten)
	}
	c.lastConceal = payload
	return Output{Kind: kind, Payload: payload, Run: c.run, Attenuation: atten}
}

// HasFrame reports whether a real frame has been seen, i.e. whether an
// extension is possible.
func (c *Concealer) HasFrame() bool { return c.hasLast }

// attenuate scales a signed 8-bit sample by gain without wrapping.
func attenuate(sample byte, gain float64) byte {
	if gain >= 1 {
		return sample
	}
	v := float64(int8(sample)) * gain
	if v > 127 {
		v = 127
	}
	if v < -128 {
		v = -128
	}
	return byte(int8(round(v)))
}

func round(v float64) int {
	if v < 0 {
		return -int(-v + 0.5)
	}
	return int(v + 0.5)
}

// jump is the mean absolute difference between two payloads' samples.
func jump(a, b []byte) float64 {
	if len(a) == 0 {
		return 0
	}
	total := 0
	for i := range a {
		d := int(int8(a[i])) - int(int8(b[i]))
		if d < 0 {
			d = -d
		}
		total += d
	}
	return float64(total) / float64(len(a))
}
