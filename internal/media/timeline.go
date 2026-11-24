package media

import "sort"

// Event is the unified media event emitted by every adapter: one compressed
// sample with its normalized timestamps. Payload bytes are kept verbatim —
// nothing is decoded or re-encoded anywhere in the pipeline.
type Event struct {
	TrackID  int
	Seq      int    // decode-order index within the source track
	MediaTime int64 // ns on the source media timeline (wrap already expanded)
	Arrival  int64  // ns local capture clock, 0 for file sources
	Duration int64  // ns
	Keyframe bool
	Damaged  bool   // payload could not be read in full; bytes may be partial
	Payload  []byte
}

// Sample is a timestamped unit on a track's media timeline.
type Sample struct {
	Index    int
	CTS      int64 // ns, media timeline (may start non-zero, may be VFR)
	Duration int64 // ns
	Keyframe bool
}

// Edit is one edit-list entry: present Duration ns of media starting at
// MediaTime, played at Rate. MediaTime == -1 is an empty edit (a gap).
type Edit struct {
	MediaTime int64
	Duration  int64
	Rate      float64
}

// ValidateEdits rejects edit lists that cannot be resolved against the
// media. Any violation is unrecoverable: guessing would silently shift the
// output timeline, which is worse than refusing the delivery.
func ValidateEdits(edits []Edit, mediaDuration int64) error {
	for i, e := range edits {
		switch {
		case e.MediaTime < -1:
			return New(PhaseTimeline, -1, -1, "edit media_time below -1", nil).
				WithTimes(e.MediaTime, -1).Fatal()
		case e.Duration <= 0:
			return New(PhaseTimeline, -1, -1, "edit with non-positive duration", nil).
				WithTimes(e.MediaTime, -1).Fatal()
		case e.MediaTime > mediaDuration:
			return New(PhaseTimeline, -1, -1, "edit media_time beyond media duration", nil).
				WithTimes(e.MediaTime, mediaDuration).Fatal()
		case e.Rate != 0 && e.Rate != 1:
			return New(PhaseTimeline, -1, -1, "unsupported edit rate", nil).
				WithTimes(e.MediaTime, -1).Fatal()
		}
		_ = i
	}
	return nil
}

// BuildTimeline merges the edit list, a non-zero start offset and VFR
// sample times into output presentation timestamps (ns). The result is
// strictly monotonic and a pure function of its inputs, so identical inputs
// always reproduce the identical timeline.
//
// Samples not covered by any edit are dropped (their output slot is -1 in
// the returned keep mask). Empty edits contribute a gap: output time still
// advances, no sample maps into it.
func BuildTimeline(samples []Sample, edits []Edit, startOffset int64) (out []int64, keep []bool, err error) {
	out = make([]int64, len(samples))
	keep = make([]bool, len(samples))
	if len(samples) == 0 {
		return out, keep, nil
	}
	mediaDur := samples[len(samples)-1].CTS + samples[len(samples)-1].Duration
	if err := ValidateEdits(edits, mediaDur); err != nil {
		return nil, nil, err
	}

	if len(edits) == 0 {
		// No edit list: normalize the non-zero start so the first sample
		// lands exactly at startOffset.
		base := samples[0].CTS
		prev := int64(-1)
		for i, s := range samples {
			t := s.CTS - base + startOffset
			t = monotonic(t, prev)
			out[i], keep[i], prev = t, true, t
		}
		return out, keep, nil
	}

	// Walk edits in order; for each, emit the samples whose CTS falls in
	// [mediaTime, mediaTime+duration). Rate is validated to 0/1 above;
	// rate 0 edits (dwell) are treated as empty.
	prev := int64(-1)
	outCursor := startOffset
	for _, e := range edits {
		if e.MediaTime < 0 || e.Rate == 0 {
			outCursor += e.Duration // empty edit: gap in output
			continue
		}
		end := e.MediaTime + e.Duration
		for i, s := range samples {
			if keep[i] || s.CTS < e.MediaTime || s.CTS >= end {
				continue
			}
			t := outCursor + (s.CTS - e.MediaTime)
			t = monotonic(t, prev)
			out[i], keep[i], prev = t, true, t
		}
		outCursor += e.Duration
	}
	return out, keep, nil
}

// monotonic nudges t past prev by the minimum representable step when the
// source timestamps collide or go backwards, guaranteeing strict increase.
func monotonic(t, prev int64) int64 {
	if t <= prev {
		return prev + 1
	}
	return t
}

// SortEventsByTime returns events ordered by (MediaTime, Seq), the
// canonical deterministic order after wrap expansion and reordering.
func SortEventsByTime(events []Event) {
	sort.SliceStable(events, func(i, j int) bool {
		if events[i].MediaTime != events[j].MediaTime {
			return events[i].MediaTime < events[j].MediaTime
		}
		return events[i].Seq < events[j].Seq
	})
}
