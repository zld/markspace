package receive

import "math"

// Energy returns the mean-square energy of a frame.
func Energy(samples []int16) float64 {
	if len(samples) == 0 {
		return 0
	}
	var acc float64
	for _, s := range samples {
		v := float64(s)
		acc += v * v
	}
	return acc / float64(len(samples))
}

// Concealer generates bounded substitution frames for gaps and tracks
// continuity metrics: run lengths and boundary energy jumps.
type Concealer struct {
	maxRun  int // frames with decayed replay; beyond that, silence
	last    []int16
	run     int // current consecutive concealed count
	total   int
	maxSeen int
	single  int // runs of exactly one frame
	multi   int // runs of two or more frames
	jumps   []float64
	lastE   float64
	lastCon bool
	hasE    bool
}

// NewConcealer bounds concealment replay to maxRun frames per gap.
func NewConcealer(maxRun int) *Concealer {
	if maxRun < 1 {
		maxRun = 1
	}
	return &Concealer{maxRun: maxRun}
}

// Real registers a received frame, closing any open concealment run.
func (c *Concealer) Real(samples []int16) {
	c.endRun()
	e := Energy(samples)
	if c.hasE && c.lastCon { // conceal -> real boundary
		c.jumps = append(c.jumps, math.Abs(e-c.lastE))
	}
	c.lastE, c.hasE, c.lastCon = e, true, false
	c.last = append(c.last[:0], samples...)
}

// Conceal returns a substitution frame: the last good frame replayed
// with exponential decay, bounded to maxRun frames, then silence.
func (c *Concealer) Conceal(n int) []int16 {
	c.run++
	c.total++
	if c.run > c.maxSeen {
		c.maxSeen = c.run
	}
	out := make([]int16, n)
	if c.run <= c.maxRun && len(c.last) == n {
		decay := math.Pow(0.6, float64(c.run))
		for i, s := range c.last {
			out[i] = int16(float64(s) * decay)
		}
	}
	e := Energy(out)
	if c.hasE && !c.lastCon { // real -> conceal boundary
		c.jumps = append(c.jumps, math.Abs(e-c.lastE))
	}
	c.lastE, c.hasE, c.lastCon = e, true, true
	return out
}

func (c *Concealer) endRun() {
	if c.run == 1 {
		c.single++
	} else if c.run > 1 {
		c.multi++
	}
	c.run = 0
}

// Flush closes a trailing concealment run at end of stream.
func (c *Concealer) Flush() { c.endRun() }

// Metrics exposes the accumulated continuity counters.
func (c *Concealer) Metrics() (total, maxRun, single, multi int, jumps []float64) {
	return c.total, c.maxSeen, c.single, c.multi, c.jumps
}
