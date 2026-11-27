package clock

import "fmt"

// Reason explains why a sample timestamp was rewritten.
type Reason string

const (
	ReasonNone      Reason = ""
	ReasonMissing   Reason = "missing_timestamp"
	ReasonDuplicate Reason = "duplicate_timestamp"
	ReasonBackward  Reason = "backward_timestamp"
	ReasonOverflow  Reason = "timestamp_overflow"
	ReasonOffset    Reason = "source_offset"
)

// Mode selects how non-increasing timestamps are handled.
type Mode int

const (
	// ModeStrict rejects duplicate and backward timestamps.
	ModeStrict Mode = iota
	// ModeMonotonic clamps them forward and records the original value.
	ModeMonotonic
)

// Policy configures the monotonic repair rules.
type Policy struct {
	Mode Mode
	// DefaultStep is the per-sample duration assumed when neither the sample
	// nor any earlier sample carries a usable duration.
	DefaultStep Rational
	// Offset is applied to every declared sample timestamp. It is used when a
	// programme switches from a primary to a backup source: both sources keep
	// their own RTP/host timestamps and the offset restores programme
	// continuity. It may be negative.
	Offset Shift
}

// DefaultPolicy repairs forward from a 1/25 s step.
func DefaultPolicy() Policy {
	return Policy{Mode: ModeMonotonic, DefaultStep: MustMake(1, 25)}
}

// Sample is one demultiplexed sample feeding the repair pass. Declared is the
// raw container timestamp; it is absent when Present is false, in which case
// the timestamp is reconstructed from the previous sample.
type Sample struct {
	Declared Rational
	Present  bool
	// Duration is the container-declared sample duration. Zero means unknown.
	Duration Rational
}

// Repaired is the outcome for one sample.
type Repaired struct {
	Original Rational
	Value    Rational
	Reason   Reason
}

// RepairTrack applies p to a track's samples in decode order. It returns one
// result per input sample and never mutates the input slice.
func RepairTrack(p Policy, samples []Sample) ([]Repaired, error) {
	out := make([]Repaired, len(samples))
	step := p.DefaultStep
	if step.N == 0 || step.D == 0 {
		step = MustMake(1, 25)
	}

	var prev Rational
	var havePrev bool
	lastGoodStep := step

	for i := range samples {
		s := samples[i]
		var prog Rational // programme time, before monotonic clamping
		var reason Reason
		missing := !s.Present

		if !missing && s.Duration.N == 0 && havePrev {
			// A zero duration paired with a non-advancing stamp cannot tell us
			// where this sample belongs, so treat it as missing.
			shifted, err := p.Offset.Apply(s.Declared)
			if err != nil {
				return nil, fmt.Errorf("%w: sample %d: %v", ErrOverflow, i, err)
			}
			if shifted.Cmp(prev) <= 0 {
				missing = true
			}
		}

		switch {
		case missing:
			reason = ReasonMissing
			if !havePrev {
				prog = Zero()
			} else {
				c, err := prev.Add(lastGoodStep)
				if err != nil {
					return nil, fmt.Errorf("%w: sample %d: %v", ErrOverflow, i, err)
				}
				prog = c
			}
		default:
			c, err := p.Offset.Apply(s.Declared)
			if err != nil {
				return nil, fmt.Errorf("%w: sample %d: %v", ErrOverflow, i, err)
			}
			prog = c
			if havePrev {
				switch prog.Cmp(prev) {
				case -1:
					reason = ReasonBackward
				case 0:
					reason = ReasonDuplicate
				}
				if reason != ReasonNone {
					if p.Mode == ModeStrict {
						return nil, fmt.Errorf("%w: sample %d: %s (%s after %s)",
							ErrCorrupt, i, reason, prog, prev)
					}
					next, err := prev.Add(lastGoodStep)
					if err != nil {
						return nil, fmt.Errorf("%w: sample %d: %v", ErrOverflow, i, err)
					}
					prog = next
				}
			}
		}

		if havePrev {
			if d, err := prog.Sub(prev); err == nil && d.N > 0 {
				lastGoodStep = d
			}
		}
		if !missing && s.Duration.N > 0 && reason == ReasonNone {
			lastGoodStep = s.Duration
		}

		if havePrev && prog.Cmp(prev) <= 0 {
			return nil, fmt.Errorf("%w: sample %d: repaired time did not advance", ErrCorrupt, i)
		}

		out[i] = Repaired{Original: s.Declared, Value: prog, Reason: reason}
		prev = prog
		havePrev = true
	}

	return out, nil
}
