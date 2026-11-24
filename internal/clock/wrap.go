package clock

// WrapEvent records one accepted modulus-boundary wraparound.
type WrapEvent struct {
	// Raw is the small finite-width counter value observed after wrap.
	Raw int64 `json:"raw"`
	// Unwrapped is the monotonic value after adding whole moduli.
	Unwrapped int64 `json:"unwrapped"`
	// Cycles is the number of modulus periods accumulated.
	Cycles int64 `json:"cycles"`
}

// Unwrapper expands finite-width timestamps (RTP 32-bit, tfdt version 0)
// onto a monotonic int64 axis. A backward jump is treated as a wraparound
// ONLY when modulus-boundary evidence holds: the previous value must sit
// within BoundaryTolerance of the upper modulus edge and the new value must
// sit within the same tolerance of zero.
type Unwrapper struct {
	Modulus           int64
	BoundaryTolerance int64

	hasPrev bool
	prevRaw int64
	cycles  int64
	last    int64

	Events           []WrapEvent
	RejectedBackward int
}

// NewUnwrapper builds an Unwrapper. tolerance <= 0 selects modulus/1000.
func NewUnwrapper(modulus, tolerance int64) *Unwrapper {
	if tolerance <= 0 {
		tolerance = modulus / 1000
	}
	if tolerance > modulus/2 {
		tolerance = modulus / 2
	}
	return &Unwrapper{Modulus: modulus, BoundaryTolerance: tolerance}
}

// Unwrap maps raw onto the monotonic axis. ev is non-nil exactly when a
// boundary-evidenced wrap was accepted. ok is false when raw moved backward
// without boundary evidence; in that case the raw value is returned
// unchanged and the caller should report an evidence fault.
func (u *Unwrapper) Unwrap(raw int64) (value int64, ev *WrapEvent, ok bool) {
	if !u.hasPrev {
		u.hasPrev = true
		u.prevRaw = raw
		u.last = raw
		return raw, nil, true
	}

	if raw >= u.prevRaw {
		u.prevRaw = raw
		u.last = raw + u.cycles*u.Modulus
		return u.last, nil, true
	}

	// Backward movement: require evidence that the counter crossed zero.
	nearTop := u.prevRaw >= u.Modulus-u.BoundaryTolerance
	nearZero := raw <= u.BoundaryTolerance
	if nearTop && nearZero {
		u.cycles++
		wrapped := raw + u.cycles*u.Modulus
		e := WrapEvent{Raw: raw, Unwrapped: wrapped, Cycles: u.cycles}
		u.Events = append(u.Events, e)
		u.prevRaw = raw
		u.last = wrapped
		return wrapped, &e, true
	}

	u.RejectedBackward++
	return raw, nil, false
}

// UnwrapSequence is a convenience wrapper over Unwrap for a whole sequence.
func UnwrapSequence(modulus, tolerance int64, raws []int64) (out []int64, events []WrapEvent, rejected int) {
	u := NewUnwrapper(modulus, tolerance)
	out = make([]int64, 0, len(raws))
	for _, r := range raws {
		v, ev, ok := u.Unwrap(r)
		if !ok {
			rejected++
		}
		out = append(out, v)
		if ev != nil {
			events = append(events, *ev)
		}
	}
	return out, u.Events, rejected
}
