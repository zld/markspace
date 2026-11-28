// Package clock provides the single integer-tick timebase shared by
// container, track and segment validation. All conversions round down
// (floor) and the dropped fraction is recorded as alignment error.
package clock

// Ticks is a position on an integer timeline.
type Ticks int64

// Timebase is expressed in ticks per second.
type Timebase uint32

// Timebases used across the pipeline.
const (
	TBMedia Timebase = 90000 // RTP / MPEG style media clock
	TBMilli Timebase = 1000  // manifest / wall-clock aligned view
)

// Convert maps t from one timebase to another, rounding down. The
// returned error is the dropped fraction of one target tick, always
// in [0,1): the true position is out+err target ticks.
func Convert(t Ticks, from, to Timebase) (out Ticks, errFrac float64) {
	if from == 0 || to == 0 {
		return 0, 0
	}
	num := int64(t) * int64(to)
	den := int64(from)
	q := num / den
	r := num % den
	if r < 0 { // floor semantics for negative inputs
		q--
		r += den
	}
	return Ticks(q), float64(r) / float64(den)
}

// Aligner accumulates the maximum alignment error observed while
// converting boundaries between timebases.
type Aligner struct {
	max   float64
	count int
}

// Observe records the fractional error of one conversion.
func (a *Aligner) Observe(errFrac float64) {
	if errFrac > a.max {
		a.max = errFrac
	}
	a.count++
}

// Max returns the largest dropped fraction seen, in target ticks.
// It is always < 1 tick by construction.
func (a *Aligner) Max() float64 { return a.max }

// Count returns how many conversions were observed.
func (a *Aligner) Count() int { return a.count }
