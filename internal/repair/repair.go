// Package repair applies the clock package's monotonic rules to demultiplexed
// tracks. It keeps the pure arithmetic in clock and adds the container-facing
// orchestration: per-track validation, audit records carrying the original and
// rewritten timestamps, and a repaired sample table the index builder consumes.
package repair

import (
	"fmt"

	"github.com/zld/continuity/internal/clock"
	"github.com/zld/continuity/internal/demux"
)

// AuditEntry records one timestamp rewrite for later reporting.
type AuditEntry struct {
	Track    uint32
	Sample   uint32
	Reason   clock.Reason
	Original clock.Rational
	Repaired clock.Rational
}

// RepairedTrack is one track after repair. Samples keep their file locations;
// only timing is rewritten.
type RepairedTrack struct {
	ID        uint32
	Timescale uint32
	Video     bool
	Samples   []clock.Repaired
	// Locations mirrors demux sample offsets/sizes/sync flags by index.
	Locations []Location
}

// Location is the on-disk position of one sample, carried through repair.
type Location struct {
	FileOffset uint64
	Size       uint32
	Sync       bool
}

// Result is the full repair output for a container.
type Result struct {
	Tracks []*RepairedTrack
	Audit  []AuditEntry
}

// RepairedCount returns how many samples had their timestamp rewritten.
func (r *Result) RepairedCount() int {
	n := 0
	for _, a := range r.Audit {
		if a.Reason != clock.ReasonNone {
			n++
		}
	}
	return n
}

// One repairs a single already-parsed track. It is the file-free entry point
// used by tests and by pipeline stages that materialize tracks on demand.
func One(tr *demux.Track, policy clock.Policy) (*RepairedTrack, error) {
	return repairOne(0, tr, policy)
}

// Track applies the policy to every track of an opened demux file. The input
// sample table is never mutated; all results are freshly allocated.
func Track(in *demux.File, policy clock.Policy) (*Result, error) {
	res := &Result{}
	for ti, tr := range in.Tracks {
		rt, err := repairOne(uint32(ti), tr, policy)
		if err != nil {
			return nil, err
		}
		res.Tracks = append(res.Tracks, rt)
		for i, s := range rt.Samples {
			if s.Reason != clock.ReasonNone {
				res.Audit = append(res.Audit, AuditEntry{
					Track:    rt.ID,
					Sample:   uint32(i),
					Reason:   s.Reason,
					Original: s.Original,
					Repaired: s.Value,
				})
			}
		}
	}
	return res, nil
}

func repairOne(trackIdx uint32, tr *demux.Track, policy clock.Policy) (*RepairedTrack, error) {
	if tr.Timescale == 0 {
		return nil, clock.NewError(clock.StageRepair, "timescale",
			fmt.Errorf("%w: track %d has no time base", clock.ErrCorrupt, trackIdx)).
			WithTrack(trackIdx)
	}
	if len(tr.Samples) == 0 {
		return nil, clock.NewError(clock.StageRepair, "empty-track",
			fmt.Errorf("%w: track %d has no samples", clock.ErrCorrupt, trackIdx)).
			WithTrack(trackIdx)
	}

	in := make([]clock.Sample, len(tr.Samples))
	locs := make([]Location, len(tr.Samples))
	for i, s := range tr.Samples {
		if s.Index != uint32(i) {
			return nil, clock.NewError(clock.StageRepair, "sample-order",
				fmt.Errorf("%w: sample row %d out of order", clock.ErrCorrupt, i)).
				WithTrack(trackIdx).WithSample(uint32(i))
		}
		in[i] = clock.Sample{
			Declared: s.DTS,
			Present:  s.Present,
			Duration: s.Duration,
		}
		locs[i] = Location{FileOffset: s.FileOffset, Size: s.Size, Sync: s.Sync}
	}

	repaired, err := clock.RepairTrack(policy, in)
	if err != nil {
		return nil, clock.NewError(clock.StageRepair, "monotonic", err).
			WithTrack(trackIdx)
	}
	if len(repaired) != len(tr.Samples) {
		return nil, clock.NewError(clock.StageRepair, "row-count",
			fmt.Errorf("%w: repaired %d rows from %d samples",
				clock.ErrCorrupt, len(repaired), len(tr.Samples))).
			WithTrack(trackIdx)
	}

	return &RepairedTrack{
		ID:        tr.ID,
		Timescale: tr.Timescale,
		Video:     tr.Video,
		Samples:   repaired,
		Locations: locs,
	}, nil
}

// VerifyMonotonic re-checks that every repaired track is strictly increasing
// and fully present. It is used after resume to guard a rebuilt in-memory table.
func VerifyMonotonic(rt *RepairedTrack) error {
	var prev clock.Rational
	have := false
	for i, s := range rt.Samples {
		if have && s.Value.Cmp(prev) <= 0 {
			return clock.NewError(clock.StageRepair, "verify",
				fmt.Errorf("%w: sample %d not after previous", clock.ErrCorrupt, i)).
				WithTrack(rt.ID).WithSample(uint32(i))
		}
		prev = s.Value
		have = true
	}
	return nil
}
