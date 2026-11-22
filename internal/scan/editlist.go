package scan

import (
	"io"

	mp4 "github.com/abema/go-mp4"

	"mediacore/internal/clock"
)

// Edit is one validated edit-list entry expressed in unified nanoseconds.
type Edit struct {
	// SegmentStart/SegmentEnd: window occupied on the unified timeline.
	SegmentStart clock.Nanos
	SegmentEnd   clock.Nanos
	// MediaStart: mapped start inside the track's own media timeline.
	MediaStart clock.Nanos
	// Empty is true for an empty edit (ISO media_time == -1): silence /
	// blank output with no corresponding media samples.
	Empty bool
}

// EditMap is the per-track result of edit-list resolution. A missing edit
// list yields a single identity edit covering the full media duration.
type EditMap struct {
	TrackID uint32
	Basis   clock.TimeBase
	Edits   []Edit
	// MediaOrigin is the non-zero media-time origin that has been mapped to
	// timeline zero of the first non-empty edit.
	MediaOrigin clock.Nanos
}

// SegmentEndNanos returns the end of the last edit, i.e. the track's total
// presentation duration on the unified timeline.
func (m *EditMap) SegmentEndNanos() clock.Nanos {
	if len(m.Edits) == 0 {
		return 0
	}
	return m.Edits[len(m.Edits)-1].SegmentEnd
}

// ResolveEdits extracts and validates the edts/elst box (if present) and
// converts every entry to unified nanoseconds using integer tick math.
//
// Rejected as recoverable corruption: non-unit media rates, entries whose
// media_time runs past the track duration, and entries that overflow the
// unified timeline.
func ResolveEdits(r io.ReadSeeker, basis clock.TimeBase, mediaDurationTicks int64) (*EditMap, error) {
	op := "scan.elst"
	dur, err := basis.ToNanos(mediaDurationTicks)
	if err != nil {
		return nil, clock.WithTrack(err, basis.TrackID, -1)
	}

	payloads, err := mp4.ExtractBoxesWithPayload(r, nil, []mp4.BoxPath{
		{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeEdts(), mp4.BoxTypeElst()},
	})
	if err != nil {
		return nil, clock.WithTrack(clock.ErrRecoverablef(op, "extract elst: %v", err), basis.TrackID, -1)
	}

	out := &EditMap{TrackID: basis.TrackID, Basis: basis}
	if len(payloads) == 0 {
		// Identity edit: full media, zero origin.
		out.Edits = []Edit{{SegmentStart: 0, SegmentEnd: dur, MediaStart: 0}}
		return out, nil
	}
	elst, ok := payloads[0].Payload.(*mp4.Elst)
	if !ok {
		return nil, clock.ErrInvariantf(op, "elst payload has unexpected type %T", payloads[0].Payload)
	}
	if int(elst.EntryCount) != len(elst.Entries) {
		return nil, clock.WithTrack(clock.ErrRecoverablef(op, "elst entry_count %d != entries %d",
			elst.EntryCount, len(elst.Entries)), basis.TrackID, -1)
	}

	var cursor clock.Nanos
	for i, e := range elst.Entries {
		if e.MediaRateInteger != 1 {
			return nil, clock.WithTrack(clock.ErrRecoverablef(op, "edit %d uses rate %d, only rate 1 supported",
				i, e.MediaRateInteger), basis.TrackID, -1)
		}
		var segDur, mediaTime int64
		if elst.GetVersion() == 1 {
			segDur, mediaTime = int64(e.SegmentDurationV1), e.MediaTimeV1
		} else {
			segDur, mediaTime = int64(e.SegmentDurationV0), int64(e.MediaTimeV0)
		}
		segNanos, err := basis.ToNanos(segDur)
		if err != nil {
			return nil, clock.WithTrack(err, basis.TrackID, int64(i))
		}
		ed := Edit{SegmentStart: cursor, SegmentEnd: cursor + segNanos}
		if segNanos <= 0 || ed.SegmentEnd <= cursor {
			return nil, clock.WithTrack(clock.ErrRecoverablef(op, "edit %d has non-positive/overflowing duration", i),
				basis.TrackID, int64(i))
		}
		if mediaTime == -1 {
			ed.Empty = true
		} else {
			if mediaTime < 0 {
				return nil, clock.WithTrack(clock.ErrRecoverablef(op, "edit %d negative media_time %d", i, mediaTime),
					basis.TrackID, int64(i))
			}
			ed.MediaStart, err = basis.ToNanos(mediaTime)
			if err != nil {
				return nil, clock.WithTrack(err, basis.TrackID, int64(i))
			}
			// Out-of-bounds: media window must lie inside the track duration.
			if ed.MediaStart < 0 || ed.MediaStart+segNanos > dur || ed.MediaStart+segNanos < ed.MediaStart {
				return nil, clock.WithTrack(clock.ErrRecoverablef(op,
					"edit %d media window [%d,+%d] exceeds media duration %d ns",
					i, ed.MediaStart, segNanos, dur), basis.TrackID, int64(i))
			}
			if i == 0 || out.MediaOrigin == 0 {
				out.MediaOrigin = ed.MediaStart
			}
		}
		out.Edits = append(out.Edits, ed)
		cursor = ed.SegmentEnd
	}
	return out, nil
}

// MapMediaToSegment maps a sample's media-time nanos to presentation time on
// the unified timeline through the edit map. Only rate-1 non-empty edits are
// present, so the mapping is a simple offset per covered window.
func (m *EditMap) MapMediaToSegment(mediaNanos clock.Nanos) (clock.Nanos, error) {
	for _, ed := range m.Edits {
		if ed.Empty {
			continue
		}
		if mediaNanos >= ed.MediaStart &&
			mediaNanos < ed.MediaStart+(ed.SegmentEnd-ed.SegmentStart) {
			return ed.SegmentStart + (mediaNanos - ed.MediaStart), nil
		}
	}
	return 0, clock.ErrRecoverablef("scan.elst", "media time %d not covered by any edit", mediaNanos)
}
