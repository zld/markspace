package timeline

import (
	"mediapack/internal/model"
)

// Ingest accepts one event. A "late" event arrives out of Seq order (an
// earlier Seq delivered after later ones were committed) inside the stable
// region; it triggers a deterministic replay from the nearest checkpoint
// rather than a silent in-place edit. A normal in-order arrival with a low
// timestamp is only a wrap/reorder repaired by process, never a replay.
// Duplicate inputs never produce duplicate samples.
func (t *Timeline) Ingest(e Event) (*model.Boundary, error) {
	// Deterministic dedup: same source/track/sample index is the same sample,
	// regardless of how many times it is re-delivered.
	key := e.key()
	if firstSeq, dup := t.seen[key]; dup {
		return nil, model.NewError(model.CodeOverlap,
			"duplicate event suppressed").
			WithFile(e.Source).WithTrack(e.TrackID).
			WithTick(e.OriginalTick).WithEvent(firstSeq)
	}

	if t.isLate(e) {
		return t.replayWith(e)
	}

	b, err := t.process(e)
	if err != nil {
		return nil, err
	}
	t.seen[key] = e.Seq
	t.events = append(t.events, e)
	t.boundaries = append(t.boundaries, *b)
	t.advanceWatermark(b.RepairedTick)
	return b, nil
}

// isLate reports an event the producer explicitly marked as delivered past the
// watermark. These must be integrated by replaying the ordered prefix rather
// than appending, since their edge predates committed stable boundaries.
func (t *Timeline) isLate(e Event) bool {
	return e.Late && len(t.events) > 0 && e.OriginalTick <= t.watermark
}

// process applies clock expansion and adjudication to a single new event
// without touching the committed event log.
func (t *Timeline) process(e Event) (*model.Boundary, error) {
	s := t.state(e.TrackID)
	original := e.OriginalTick
	repaired := original
	repair := RepairNone

	// 1) RTP clock wrap expansion, preserving the raw 32-bit value.
	if e.HasRTP {
		ext, wrapped := ExpandRTP(s.rtpRaw, s.rtpExt, e.RTPRaw)
		s.rtpExt = ext
		s.rtpRaw = e.RTPRaw
		s.hasRTP = true
		if wrapped {
			t.WrapCount++
			repair = RepairWrap
		}
	}

	// 2) Source-switch handling when the source name changes on this track.
	if s.lastSrc != "" && s.lastSrc != e.Source {
		switch {
		case original < s.lastEnd:
			// Overlap: the new source starts before the old one ended.
			if t.rule.RejectOverlap {
				return nil, model.NewError(model.CodeOverlap,
					"source-switch overlap rejected by rule").
					WithFile(e.Source).WithTrack(e.TrackID).
					WithTick(original).WithEvent(e.Seq)
			}
			// Seq adjudication: keep the already committed edge, drop this one
			// by clamping onto the prior end.
			repaired = s.lastEnd
			repair = RepairOverlap
			t.OverlapCount++
		case original-s.lastEnd > t.rule.MaxGapTick && t.rule.MaxGapTick >= 0:
			return nil, model.NewError(model.CodeGap,
				"source-switch gap exceeds tolerance").
				WithFile(e.Source).WithTrack(e.TrackID).
				WithTick(original).WithEvent(e.Seq)
		case original > s.lastEnd && t.rule.MaxGapTick >= 0:
			t.GapCount++
			if repair == RepairNone {
				repair = RepairGap
			}
		}
	}

	// 3) Monotonicity guarantee: repaired edges must never move backwards on
	// the same track, even absent a detected RTP wrap.
	if s.lastRep != 0 && repaired <= s.lastRep {
		repaired = s.lastRep + 1
		if repair == RepairNone {
			repair = RepairReorder
		}
	}

	b := &model.Boundary{
		EventSeq:     e.Seq,
		Source:       e.Source,
		TrackID:      e.TrackID,
		SampleIndex:  e.SampleIndex,
		OriginalTick: original,
		RepairedTick: repaired,
		Delta:        repaired - original,
		Repair:       repair,
	}
	s.lastRep = repaired
	s.lastEnd = repaired + e.Duration
	s.lastSrc = e.Source
	return b, nil
}

// StableCount is the number of committed, watermark-stable boundaries.
func (t *Timeline) StableCount() int { return len(t.boundaries) }
