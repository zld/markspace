package timebase

// This file holds the shared-clock conversion layer: mapping between the
// representation-local track timescale and the common millisecond clock. The
// arithmetic lives here so time.go stays focused on the Rat primitive.

// Scale converts an integer amount measured in ticks at srcHz to the amount
// expressed in ticks at dstHz as an exact rational.
func Scale(amount int64, srcHz, dstHz int64) (Rat, error) {
	if srcHz <= 0 || dstHz <= 0 {
		return Rat{}, errZeroDenominator
	}
	r, err := NewRat(amount, 1)
	if err != nil {
		return Rat{}, err
	}
	f, err := NewRat(dstHz, srcHz)
	if err != nil {
		return Rat{}, err
	}
	return r.Mul(f)
}

// ToSharedMs maps a media-timeline value at the given timescale to whole
// milliseconds on the shared clock, rounded half away from zero.
func ToSharedMs(mediaTicks uint64, timescale uint32) (int64, error) {
	if timescale == 0 {
		return 0, errZeroDenominator
	}
	r, err := NewRat(int64(mediaTicks), int64(timescale))
	if err != nil {
		return 0, err
	}
	ms, err := r.Mul(MustNewRat(SharedClockHz, 1))
	if err != nil {
		return 0, err
	}
	return ms.Round()
}

// FromSharedMs maps a shared-clock millisecond value back to media ticks at
// the given timescale, rounded half away from zero.
func FromSharedMs(sharedMs int64, timescale uint32) (uint64, error) {
	if timescale == 0 {
		return 0, errZeroDenominator
	}
	r, err := NewRat(sharedMs, SharedClockHz)
	if err != nil {
		return 0, err
	}
	t, err := r.Mul(MustNewRat(int64(timescale), 1))
	if err != nil {
		return 0, err
	}
	v, err := t.Round()
	if err != nil {
		return 0, err
	}
	if v < 0 {
		return 0, errOverflow
	}
	return uint64(v), nil
}

// AbsDiff returns |r-o| as a non-negative rational.
func AbsDiff(r, o Rat) (Rat, error) {
	d, err := r.Sub(o)
	if err != nil {
		return Rat{}, err
	}
	if d.num < 0 {
		d.num = -d.num
	}
	return d, nil
}

// WithinOneTick reports whether |r-o| does not exceed one shared-clock tick
// (1 ms), the maximum tolerated divergence between representations.
func WithinOneTick(r, o Rat) (bool, error) {
	d, err := AbsDiff(r, o)
	if err != nil {
		return false, err
	}
	oneMs := MustNewRat(Millisecond, SharedClockHz)
	return d.Cmp(oneMs) <= 0, nil
}
