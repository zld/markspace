package timeline

import "sort"

// Event is one media block or network occurrence placed on the shared
// nanosecond base. TrackID is the Matroska track number; BlockSeq is the
// per-track block ordinal, used as the final stable tie breaker.
type Event struct {
	TimeNs   Timestamp
	TrackID  uint64
	BlockSeq uint64
	Kind     string // e.g. "video", "audio", "rtp"
	// Payload is optional subsystem-specific data (block bytes, seq numbers).
	Payload any
}

// MergeStable returns every event ordered by (time, track, block sequence).
// sort.SliceStable preserves input order for fully-equal keys, making the
// output deterministic regardless of which track source emitted first.
func MergeStable(events []Event) []Event {
	out := make([]Event, len(events))
	copy(out, events)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].TimeNs != out[j].TimeNs {
			return out[i].TimeNs < out[j].TimeNs
		}
		if out[i].TrackID != out[j].TrackID {
			return out[i].TrackID < out[j].TrackID
		}
		return out[i].BlockSeq < out[j].BlockSeq
	})
	return out
}

// MergeTracks flattens per-track event streams (each assumed already in
// ascending time order) into one stable global timeline.
func MergeTracks(tracks map[uint64][]Event) []Event {
	total := 0
	for _, evs := range tracks {
		total += len(evs)
	}
	all := make([]Event, 0, total)
	for _, evs := range tracks {
		all = append(all, evs...)
	}
	return MergeStable(all)
}

// StreamMerger performs a bounded-memory k-way merge of per-track streams:
// at most one head event per stream is held at any time. Each source must
// yield events in ascending (time, blockseq) order; the zero Event and false
// signal end of stream.
type StreamMerger struct {
	next []func() (Event, bool, error)
}

// NewStreamMerger builds a merger over the given ordered source iterators.
func NewStreamMerger(sources ...func() (Event, bool, error)) *StreamMerger {
	return &StreamMerger{next: sources}
}

// less defines the global ordering shared with MergeStable.
func less(a, b Event) bool {
	if a.TimeNs != b.TimeNs {
		return a.TimeNs < b.TimeNs
	}
	if a.TrackID != b.TrackID {
		return a.TrackID < b.TrackID
	}
	return a.BlockSeq < b.BlockSeq
}

// sameKey reports whether two events tie on every ordering key.
func sameKey(a, b Event) bool {
	return a.TimeNs == b.TimeNs && a.TrackID == b.TrackID && a.BlockSeq == b.BlockSeq
}

// Drain runs the merge to completion, invoking accept for every event in
// global order. Source index is the tie breaker for fully-equal events.
func (m *StreamMerger) Drain(accept func(Event) error) error {
	heads := make([]Event, len(m.next))
	alive := make([]bool, len(m.next))
	for i, src := range m.next {
		ev, ok, err := src()
		if err != nil {
			return err
		}
		heads[i], alive[i] = ev, ok
	}
	for {
		best := -1
		for i := range m.next {
			if !alive[i] {
				continue
			}
			if best < 0 || less(heads[i], heads[best]) ||
				(sameKey(heads[i], heads[best]) && i < best) {
				best = i
			}
		}
		if best < 0 {
			return nil
		}
		if err := accept(heads[best]); err != nil {
			return err
		}
		ev, ok, err := m.next[best]()
		if err != nil {
			return err
		}
		heads[best], alive[best] = ev, ok
	}
}
