package manifest

import (
	"archivecheck/internal/model"
)

// Accumulator walks a segment timeline while preserving fractional ticks.
//
// Each Next converts a duration to ticks, folds the carried remainder in, and
// emits the integral duration for the segment. The leftover fraction is
// carried to the following step, so a long playlist neither loses the
// fractions nor drifts away from the exact total.
type Accumulator struct {
	// Base is the shared timebase durations are converted into.
	Base int64
	// Ticks is the running integral position.
	Ticks int64
	// Rem is the carried fractional remainder in [0,1).
	Rem model.Rational
}

// NewAccumulator starts at position zero.
func NewAccumulator(base int64) *Accumulator {
	return &Accumulator{Base: base, Rem: model.Rational{Den: 1}}
}

// Next advances by one exact duration. It returns the integral ticks assigned
// to this step, the remainder carried into it, and the remainder carried out.
func (a *Accumulator) Next(d model.Time) (integral int64, carriedIn, carriedOut model.Rational, err error) {
	if d.Ticks < 0 || d.Rem.Num < 0 {
		return 0, model.Rational{}, model.Rational{}, model.ErrTimeOverflow
	}
	total, err := model.NewTime(a.Ticks, a.Rem)
	if err != nil {
		return 0, model.Rational{}, model.Rational{}, err
	}
	end, err := total.Add(d)
	if err != nil {
		return 0, model.Rational{}, model.Rational{}, err
	}
	integral = end.Ticks - total.Ticks
	a.Ticks = end.Ticks
	a.Rem = end.Rem
	return integral, total.Rem, end.Rem, nil
}

// Position returns the exact current position.
func (a *Accumulator) Position() model.Time {
	return model.Time{Ticks: a.Ticks, Rem: a.Rem}
}
