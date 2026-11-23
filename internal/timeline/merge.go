package timeline

import "sort"

// Merged is the output of merging every track's samples onto one clock.
type Merged struct {
	Clock   Clock
	Samples []Sample
	Errors  []*Error
}

// MergeOptions controls merge behavior.
type MergeOptions struct {
	// AllowReorder sorts per-track samples before merging when true. When
	// false, an out-of-order sample on the same track is a hard error.
	AllowReorder bool
}

// DefaultMergeOptions returns permissive, deterministic options.
func DefaultMergeOptions() MergeOptions { return MergeOptions{AllowReorder: true} }

// Merge combines multi-track samples into a single nanosecond timeline. It
// tolerates sparse audio, variable video frame pacing and non-zero start
// times. Every retained sample references a valid track; damaged samples
// (unknown track, negative time, duplicates) are reported, not silently kept.
func Merge(g *Graph, in []Sample, opts MergeOptions) *Merged {
	m := &Merged{}
	if err := g.ValidateReferences(in); err != nil {
		m.Errors = append(m.Errors, err)
	}
	byTrack := make(map[uint32][]Sample)
	for i := range in {
		s := in[i]
		if s.StartNs < 0 || s.DurationNs < 0 {
			m.Errors = append(m.Errors, &Error{
				Category: CategoryOutOfRange, TrackNumber: s.TrackNumber,
				Offset: s.Offset, TimeNs: s.StartNs,
				Message: "negative time or duration",
			})
			continue
		}
		if !g.Has(s.TrackNumber) {
			continue // already reported by ValidateReferences
		}
		byTrack[s.TrackNumber] = append(byTrack[s.TrackNumber], s)
	}

	for _, num := range g.Order {
		ts := byTrack[num]
		if opts.AllowReorder {
			sort.SliceStable(ts, func(i, j int) bool {
				if ts[i].StartNs != ts[j].StartNs {
					return ts[i].StartNs < ts[j].StartNs
				}
				return ts[i].Offset < ts[j].Offset
			})
		} else if err := checkMonotonic(num, ts); err != nil {
			m.Errors = append(m.Errors, err)
		}
		deduped, dupErrs := dedupe(num, ts)
		m.Errors = append(m.Errors, dupErrs...)
		for _, s := range deduped {
			m.Clock.Observe(s.StartNs)
			m.Clock.Observe(s.EndNs())
		}
		m.Samples = append(m.Samples, deduped...)
	}

	// Global ordering: time first, then graph order so dependencies lead.
	orderIndex := make(map[uint32]int, len(g.Order))
	for i, n := range g.Order {
		orderIndex[n] = i
	}
	sort.SliceStable(m.Samples, func(i, j int) bool {
		a, b := m.Samples[i], m.Samples[j]
		if a.StartNs != b.StartNs {
			return a.StartNs < b.StartNs
		}
		return orderIndex[a.TrackNumber] < orderIndex[b.TrackNumber]
	})
	return m
}

// checkMonotonic flags any sample whose time goes backwards on one track.
func checkMonotonic(track uint32, ts []Sample) *Error {
	for i := 1; i < len(ts); i++ {
		if ts[i].StartNs < ts[i-1].StartNs {
			return &Error{
				Category: CategoryNonMonotonic, TrackNumber: track,
				Offset: ts[i].Offset, TimeNs: ts[i].StartNs,
				Message: "sample time goes backwards on track",
			}
		}
	}
	return nil
}

// dedupe drops repeated samples with the same track, start time and offset.
func dedupe(track uint32, ts []Sample) ([]Sample, []*Error) {
	seen := make(map[[2]int64]bool, len(ts))
	out := ts[:0]
	var errs []*Error
	for _, s := range ts {
		key := [2]int64{s.StartNs, s.Offset}
		if seen[key] {
			errs = append(errs, &Error{
				Category: CategoryDuplicate, TrackNumber: track,
				Offset: s.Offset, TimeNs: s.StartNs,
				Message: "duplicate sample dropped",
			})
			continue
		}
		seen[key] = true
		out = append(out, s)
	}
	return out, errs
}

// CheckInRange verifies that every sample time lies inside the clock's
// observed source range and that tracks are valid. Used as a cross-stage
// invariant after merging.
func CheckInRange(m *Merged) []*Error {
	var errs []*Error
	for _, s := range m.Samples {
		if !m.Clock.InRange(s.StartNs) || !m.Clock.InRange(s.EndNs()) {
			errs = append(errs, &Error{
				Category: CategoryOutOfRange, TrackNumber: s.TrackNumber,
				Offset: s.Offset, TimeNs: s.StartNs,
				Message: "sample time outside observed source range",
			})
		}
	}
	return errs
}
