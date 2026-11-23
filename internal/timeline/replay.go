package timeline

import (
	"mediaverify/internal/clock"
	"mediaverify/internal/errs"
)

// Boundary is one recomputable cut point derived from the event stream.
type Boundary struct {
	Index int
	Time  clock.Ticks
	// Generation starts at 1 and increments every time the suffix that
	// contains this boundary is recomputed.
	Generation int
}

// Builder derives boundaries from a canonical event slice. Implementations
// must be pure: identical inputs always produce identical outputs, which is
// what makes repeated runs bit-for-bit reproducible.
type Builder func(events []Event) ([]Boundary, error)

// Replayer keeps the previously derived boundaries and, on a replay,
// recomputes only the suffix from the first boundary whose time is at or
// after the late event. Sealed boundaries are never recomputed.
type Replayer struct {
	wm         *Watermark
	build      Builder
	boundaries []Boundary
	generation int
	lastReplay int
}

// NewReplayer wires a builder to a watermark.
func NewReplayer(wm *Watermark, build Builder) *Replayer {
	return &Replayer{wm: wm, build: build}
}

// Rebuild recomputes all boundaries from scratch (initial run or after an
// accepted replay). It refuses to alter any boundary inside the sealed
// prefix; a late event that would do so is rejected up-front by the
// watermark, so reaching that state here is an internal invariant failure.
func (r *Replayer) Rebuild() ([]Boundary, error) {
	fresh, err := r.build(r.wm.Events())
	if err != nil {
		return nil, err
	}
	if r.generation == 0 {
		r.generation = 1
		r.lastReplay = r.wm.ReplayCount()
		for i := range fresh {
			fresh[i].Generation = 1
		}
		r.boundaries = fresh
		return append([]Boundary(nil), fresh...), nil
	}
	// No accepted replay since the previous rebuild: output is unchanged,
	// so repeated runs stay bit-for-bit identical and generations stable.
	if r.wm.ReplayCount() == r.lastReplay {
		return r.Boundaries(), nil
	}
	r.lastReplay = r.wm.ReplayCount()
	sealed := r.wm.SealedUntil()
	// The watermark guarantees no accepted event precedes the sealed
	// prefix; a recomputed boundary appearing inside it that did not exist
	// before would break the seal and is an internal invariant failure.
	archived := map[int64]Boundary{}
	for _, b := range r.boundaries {
		if b.Time <= sealed {
			archived[b.Time.Ns()] = b
		}
	}
	r.generation++
	out := make([]Boundary, 0, len(fresh))
	for _, b := range fresh {
		if b.Time <= sealed {
			prev, ok := archived[b.Time.Ns()]
			if !ok {
				return nil, errs.New(errs.KindInternal, "timeline",
					"replay would create boundary at %d inside sealed prefix", b.Time.Ns())
			}
			out = append(out, prev)
			continue
		}
		b.Generation = r.generation
		out = append(out, b)
	}
	r.boundaries = out
	return append([]Boundary(nil), out...), nil
}

// Boundaries returns the current derived boundaries.
func (r *Replayer) Boundaries() []Boundary {
	out := make([]Boundary, len(r.boundaries))
	copy(out, r.boundaries)
	return out
}

// Generation is the current recomputation generation.
func (r *Replayer) Generation() int { return r.generation }

// ReplayCount mirrors the watermark counter for convenience.
func (r *Replayer) ReplayCount() int { return r.wm.ReplayCount() }
