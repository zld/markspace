package stream

import (
	perr "gamepack/internal/errors"
)

// reconcileIndex validates every index entry against the scanned units and
// returns boundary events. It enforces offset-range, truncation and index
// completeness rules before any unit may enter a fragment.
func reconcileIndex(raw []byte, units []Unit, idx *Index) ([]BoundaryEvent, error) {
	byStart := make(map[int64]Unit, len(units))
	for _, u := range units {
		byStart[u.Start] = u
	}
	seen := make(map[int64]bool, len(idx.Entries))
	out := make([]BoundaryEvent, 0, len(idx.Entries))
	for _, e := range idx.Entries {
		if e.Offset < 0 || e.Offset >= int64(len(raw)) {
			return nil, perr.At(subsystem, perr.CodeOffsetOutOfRange, e.Offset,
				"index offset %d outside stream of %d bytes", e.Offset, len(raw))
		}
		if e.Length <= 0 || e.Offset+e.Length > int64(len(raw)) {
			return nil, perr.At(subsystem, perr.CodeTruncatedUnit, e.Offset,
				"index entry at %d length %d exceeds stream bounds", e.Offset, e.Length)
		}
		if seen[e.Offset] {
			return nil, perr.At(subsystem, perr.CodeIndexMissing, e.Offset,
				"duplicate index entry at byte %d", e.Offset)
		}
		seen[e.Offset] = true
		u, ok := byStart[e.Offset]
		if !ok {
			return nil, perr.At(subsystem, perr.CodeIndexMissing, e.Offset,
				"index entry at byte %d does not point at a unit boundary", e.Offset)
		}
		if e.Length != u.End-u.Start {
			return nil, perr.At(subsystem, perr.CodeInvalidEncoding, e.Offset,
				"index length %d disagrees with scanned unit length %d at byte %d",
				e.Length, u.End-u.Start, e.Offset)
		}
		if !u.Key {
			return nil, perr.At(subsystem, perr.CodeIndexMissing, e.Offset,
				"index entry at byte %d is not a keyframe unit", e.Offset)
		}
		out = append(out, BoundaryEvent{
			Ordinal: u.Ordinal, Offset: u.Start, Length: u.End - u.Start, Key: true,
		})
	}
	// Every keyframe the scanner discovered must appear in the index.
	for _, u := range units {
		if u.Key && !seen[u.Start] {
			return nil, perr.At(subsystem, perr.CodeIndexMissing, u.Start,
				"keyframe at byte %d missing from index", u.Start)
		}
	}
	if len(out) == 0 {
		return nil, perr.New(subsystem, perr.CodeIndexMissing,
			"index contains no usable keyframe boundary")
	}
	return out, nil
}
