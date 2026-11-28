// Package track validates container track mappings and shared-tick
// timestamp monotonicity.
package track

import (
	"mediacheck/internal/clock"
	"mediacheck/internal/codec"
	"mediacheck/internal/fault"
)

// Mapping binds one container track to one source stream.
type Mapping struct {
	TrackID  uint32
	Kind     codec.Kind
	SrcIndex int // index of the source stream this track maps from
}

// ValidateMappings rejects missing tracks, duplicate mappings and
// track-count overflows, in that priority order.
func ValidateMappings(ms []Mapping, required []codec.Kind, maxTracks int) error {
	if len(ms) > maxTracks {
		return fault.New(fault.ErrLimitExceeded, int64(maxTracks),
			"%d tracks exceed limit %d", len(ms), maxTracks)
	}
	seen := make(map[codec.Kind]bool, len(ms))
	seenID := make(map[uint32]bool, len(ms))
	seenSrc := make(map[int]bool, len(ms))
	for _, m := range ms {
		if seenID[m.TrackID] {
			return fault.New(fault.ErrDuplicateMap, int64(m.TrackID),
				"track id %d mapped twice", m.TrackID)
		}
		if seenSrc[m.SrcIndex] {
			return fault.New(fault.ErrDuplicateMap, int64(m.SrcIndex),
				"source stream %d mapped twice", m.SrcIndex)
		}
		seenID[m.TrackID] = true
		seenSrc[m.SrcIndex] = true
		seen[m.Kind] = true
	}
	for _, k := range required {
		if !seen[k] {
			return fault.New(fault.ErrMissingTrack, fault.NoOffset,
				"no track for codec %s", k)
		}
	}
	return nil
}

// CheckMonotonic rejects time regression on the shared tick line.
// The reported offset is the index of the first regressing stamp.
func CheckMonotonic(ts []clock.Ticks) error {
	for i := 1; i < len(ts); i++ {
		if ts[i] < ts[i-1] {
			return fault.New(fault.ErrTimeRegression, int64(i),
				"tick %d goes back to %d from %d", i, ts[i], ts[i-1])
		}
	}
	return nil
}
