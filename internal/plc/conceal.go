// Package plc implements packet loss concealment: missing frames are
// replaced by a decaying copy of the most recent real PCM waveform, for at
// most MaxConsecutive frames, after which silence is emitted. It also
// tracks boundary continuity between consecutive output frames.
package plc

// DefaultThreshold is the boundary-jump threshold (absolute PCM16 sample
// delta) below which a frame transition counts as continuous.
const DefaultThreshold = 8000

// MaxConsecutiveFrames is the maximum run of concealed frames before
// falling back to silence.
const MaxConsecutiveFrames = 3

// decay is the per-frame gain applied to the concealed waveform.
const decay = 0.75

type Concealer struct {
	frameSize int
	threshold int

	prev      []int16 // last emitted frame
	lastReal  []int16 // most recent real frame
	missRun   int     // current run of concealed frames
	gain      float64 // current concealment gain
	Concealed int     // total concealed frames

	transitions int
	ok          int
	MaxJump     int
}

func New(frameSize int) *Concealer {
	return &Concealer{frameSize: frameSize, threshold: DefaultThreshold, gain: 1}
}

// Output returns the frame to play. With ok=true, real is emitted and
// becomes the concealment source; with ok=false a concealed (or silent)
// frame is synthesized.
func (c *Concealer) Output(real []int16, ok bool) []int16 {
	var out []int16
	if ok {
		out = real
		c.lastReal = append(c.lastReal[:0], real...)
		c.missRun = 0
		c.gain = 1
	} else {
		c.Concealed++
		c.missRun++
		out = make([]int16, c.frameSize)
		if c.missRun <= MaxConsecutiveFrames && len(c.lastReal) == c.frameSize {
			c.gain *= decay
			for i, s := range c.lastReal {
				out[i] = int16(float64(s) * c.gain)
			}
		}
		// else: silence after MaxConsecutiveFrames concealed frames
	}
	c.recordBoundary(out)
	c.prev = append(c.prev[:0], out...)
	return out
}

// recordBoundary measures the sample jump across the frame boundary.
func (c *Concealer) recordBoundary(out []int16) {
	if len(c.prev) == 0 || len(out) == 0 {
		return
	}
	jump := int(out[0]) - int(c.prev[len(c.prev)-1])
	if jump < 0 {
		jump = -jump
	}
	c.transitions++
	if jump <= c.threshold {
		c.ok++
	}
	if jump > c.MaxJump {
		c.MaxJump = jump
	}
}

// ContinuityRatio is the fraction of frame boundaries whose jump is within
// the threshold. It is 1 when no transitions were observed.
func (c *Concealer) ContinuityRatio() float64 {
	if c.transitions == 0 {
		return 1
	}
	return float64(c.ok) / float64(c.transitions)
}
