// Package segment aligns cut points across representations and emits a
// deterministic shard manifest. A group is only valid when every
// representation has a boundary within one 90 kHz tick of every other one.
package segment

import (
	"sort"

	"tracecheck/internal/clock"
	"tracecheck/internal/probe"
)

// MaxBoundaryDelta is the permitted start-time difference between
// representations of the same segment, measured in shared 90 kHz ticks.
const MaxBoundaryDelta clock.Tick = 1

// Boundary is one aligned cut point across all representations.
type Boundary struct {
	Index       int
	Start       clock.Tick
	MemberStarts map[uint32]clock.Tick
	MaxDelta    clock.Tick
}

// Alignment is the validated cut-point set for one container.
type Alignment struct {
	Boundaries []Boundary
	// ReferenceTrackID is the representation whose sync samples define groups.
	ReferenceTrackID uint32
}

// Align groups sync samples across representations. The lowest-track-ID
// representation is the reference; for each of its sync samples every other
// representation must contribute a sync sample within MaxBoundaryDelta.
// A missing or distant boundary fails the whole group rather than being filled
// in silently.
func Align(recovered map[uint32][]probe.RecoveredSample) (*Alignment, error) {
	ids := sortedTrackIDs(recovered)
	if len(ids) == 0 {
		return nil, &clock.ErrUnrecoverable{Reason: "no recovered representations", Offset: 0}
	}
	ref := ids[0]
	refSamples := recovered[ref]

	syncByTrack := make(map[uint32][]probe.RecoveredSample, len(ids))
	for _, id := range ids {
		for _, s := range recovered[id] {
			if s.Sync {
				syncByTrack[id] = append(syncByTrack[id], s)
			}
		}
	}

	var out []Boundary
	group := 0
	for _, rs := range refSamples {
		if !rs.Sync {
			continue
		}
		members := map[uint32]clock.Tick{ref: rs.Start}
		minStart, maxStart := rs.Start, rs.Start
		ok := true
		for _, id := range ids[1:] {
			match, found := nearestSync(syncByTrack[id], rs.Start)
			if !found {
				return nil, clock.NewOffsetError(rs.Offset, clock.CategoryMissingIndex,
					"representation "+itoa(id)+" has no boundary near tick "+itoa64(int64(rs.Start)), nil)
			}
			delta := absTick(match.Start - rs.Start)
			if delta > MaxBoundaryDelta {
				return nil, clock.NewOffsetError(match.Offset, clock.CategoryBounds,
					"representation "+itoa(id)+" boundary delta "+itoa64(int64(delta))+
						" exceeds one tick", nil)
			}
			members[id] = match.Start
			if match.Start < minStart {
				minStart = match.Start
			}
			if match.Start > maxStart {
				maxStart = match.Start
			}
			_ = ok
		}
		out = append(out, Boundary{
			Index: group, Start: rs.Start, MemberStarts: members,
			MaxDelta: maxStart - minStart,
		})
		group++
	}
	return &Alignment{Boundaries: out, ReferenceTrackID: ref}, nil
}

// nearestSync returns the sync sample whose start is closest to target, or
// false when none exist.
func nearestSync(syncs []probe.RecoveredSample, target clock.Tick) (probe.RecoveredSample, bool) {
	if len(syncs) == 0 {
		return probe.RecoveredSample{}, false
	}
	idx := sort.Search(len(syncs), func(i int) bool {
		return syncs[i].Start >= target
	})
	if idx == 0 {
		return syncs[0], true
	}
	if idx == len(syncs) {
		return syncs[len(syncs)-1], true
	}
	prev, next := syncs[idx-1], syncs[idx]
	if absTick(target-prev.Start) <= absTick(next.Start-target) {
		return prev, true
	}
	return next, true
}

func sortedTrackIDs(m map[uint32][]probe.RecoveredSample) []uint32 {
	ids := make([]uint32, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func absTick(t clock.Tick) clock.Tick {
	if t < 0 {
		return -t
	}
	return t
}

func itoa(v uint32) string { return itoa64(int64(v)) }

func itoa64(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
