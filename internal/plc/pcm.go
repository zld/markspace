// Package plc generates packet-loss-concealment PCM frames and measures
// boundary continuity between consecutive output frames.
package plc

import (
	"encoding/binary"
	"math"
)

const (
	// SampleRate is the PCM sample rate in Hz.
	SampleRate = 48000
	// FrameSamples is the number of mono 16-bit samples per 20 ms frame.
	FrameSamples = 960
	// FrameBytes is the PCM payload size of one frame.
	FrameBytes = FrameSamples * 2
)

// SamplesFromPCM decodes little-endian int16 PCM. Odd trailing bytes are
// ignored; short payloads are zero-padded to a full frame.
func SamplesFromPCM(pcm []byte) []int16 {
	out := make([]int16, FrameSamples)
	n := len(pcm) / 2
	if n > FrameSamples {
		n = FrameSamples
	}
	for i := 0; i < n; i++ {
		out[i] = int16(binary.LittleEndian.Uint16(pcm[2*i:]))
	}
	return out
}

// PCMFromSamples encodes int16 samples as little-endian PCM bytes.
func PCMFromSamples(s []int16) []byte {
	out := make([]byte, len(s)*2)
	for i, v := range s {
		binary.LittleEndian.PutUint16(out[2*i:], uint16(v))
	}
	return out
}

// Concealer produces concealment frames. It repeats the last good frame
// with a decay, and after MaxRun consecutive misses fades to silence.
type Concealer struct {
	// MaxRun is the configured consecutive-miss limit before full mute.
	MaxRun int

	last     []int16 // last good frame, nil before the first
	haveLast bool
	run      int // current consecutive miss count
}

func NewConcealer(maxRun int) *Concealer {
	if maxRun < 1 {
		maxRun = 1
	}
	return &Concealer{MaxRun: maxRun}
}

// NoteGood registers a successfully decoded frame.
func (c *Concealer) NoteGood(pcm []byte) {
	c.last = SamplesFromPCM(pcm)
	c.haveLast = true
	c.run = 0
}

// Conceal returns the next concealment frame: a decaying repeat of the
// last good frame, fading fully to silence once the miss run exceeds
// MaxRun.
func (c *Concealer) Conceal() []byte {
	c.run++
	out := make([]int16, FrameSamples)
	if !c.haveLast {
		return PCMFromSamples(out) // silence before any good frame
	}
	// Gain: gentle decay per miss; hard fade to 0 across the last
	// MaxRun misses.
	gain := math.Pow(0.9, float64(c.run))
	if c.run > c.MaxRun {
		gain = 0
	} else if fade := float64(c.MaxRun-c.run+1) / float64(c.MaxRun+1); fade < gain {
		gain = fade
	}
	for i, v := range c.last {
		out[i] = int16(float64(v) * gain)
	}
	return PCMFromSamples(out)
}

// BoundaryTracker measures continuity across output-frame boundaries.
// For every boundary it records the absolute jump between the last
// sample of the previous frame and the first sample of the next.
type BoundaryTracker struct {
	jumps []int
	// PLC-segment boundaries only: boundaries where at least one side
	// was a concealment frame.
	plcJumps     []int
	plcGood      int
	jumpOKThresh int
}

func NewBoundaryTracker() *BoundaryTracker {
	return &BoundaryTracker{jumpOKThresh: 2000}
}

// Observe records one boundary. prev/cur are the raw PCM of adjacent
// output frames; plcInvolved marks boundaries touching a concealed frame.
func (t *BoundaryTracker) Observe(prev, cur []byte, plcInvolved bool) {
	ps, cs := SamplesFromPCM(prev), SamplesFromPCM(cur)
	jump := int(math.Abs(float64(int(cs[0]) - int(ps[FrameSamples-1]))))
	t.jumps = append(t.jumps, jump)
	if plcInvolved {
		t.plcJumps = append(t.plcJumps, jump)
		if jump <= t.jumpOKThresh {
			t.plcGood++
		}
	}
}

// ContinuityPct returns the percentage of PLC-involved boundaries whose
// jump stayed under the threshold. 0 when no PLC boundary occurred.
func (t *BoundaryTracker) ContinuityPct() float64 {
	if len(t.plcJumps) == 0 {
		return 0
	}
	return 100 * float64(t.plcGood) / float64(len(t.plcJumps))
}

// JumpP95 returns the 95th percentile boundary jump across all frames.
func (t *BoundaryTracker) JumpP95() int {
	return percentile(t.jumps, 0.95)
}

func percentile(v []int, p float64) int {
	if len(v) == 0 {
		return 0
	}
	s := append([]int(nil), v...)
	for i := 1; i < len(s); i++ { // insertion sort; small n
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
	idx := int(math.Ceil(p*float64(len(s)))) - 1
	if idx < 0 {
		idx = 0
	}
	return s[idx]
}
