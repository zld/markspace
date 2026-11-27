// Package index builds a random-access keyframe index over the unified
// timeline and serves exact closed-open interval queries.
package index

import (
	"sort"

	"mediad/internal/ebml"
)

// TimedEvent is a parsed block event mapped onto the unified ns timeline.
type TimedEvent struct {
	ebml.Event
	TimeNs int64
}

// Keyframe is one index entry. Entries are only ever created from events
// produced by a fully successful parse; a corrupt block aborts parsing
// upstream, so the index can never reference a fabricated position.
type Keyframe struct {
	TimeNs int64
	Offset int64
	Size   int64
	Track  uint64
}

// Index is a sorted keyframe table plus the media extent.
type Index struct {
	Keys       []Keyframe
	DurationNs int64
	SrcLength  int64
}

// Build constructs the index from mapped events. srcLen is the total source
// length in bytes; durationNs is the end of the last event on the unified
// timeline.
func Build(events []TimedEvent, srcLen, durationNs int64) *Index {
	ix := &Index{DurationNs: durationNs, SrcLength: srcLen}
	for _, ev := range events {
		if !ev.Keyframe {
			continue
		}
		ix.Keys = append(ix.Keys, Keyframe{
			TimeNs: ev.TimeNs,
			Offset: ev.Offset,
			Size:   ev.Size,
			Track:  ev.Track,
		})
	}
	sort.Slice(ix.Keys, func(i, j int) bool {
		if ix.Keys[i].TimeNs != ix.Keys[j].TimeNs {
			return ix.Keys[i].TimeNs < ix.Keys[j].TimeNs
		}
		return ix.Keys[i].Offset < ix.Keys[j].Offset
	})
	return ix
}

// keyAtOrBefore returns the last keyframe at or before t, or nil.
func (ix *Index) keyAtOrBefore(t int64) *Keyframe {
	var best *Keyframe
	for i := range ix.Keys {
		if ix.Keys[i].TimeNs <= t {
			best = &ix.Keys[i]
		} else {
			break
		}
	}
	return best
}

// keyAtOrAfter returns the first keyframe at or after t, or nil.
func (ix *Index) keyAtOrAfter(t int64) *Keyframe {
	for i := range ix.Keys {
		if ix.Keys[i].TimeNs >= t {
			return &ix.Keys[i]
		}
	}
	return nil
}

// HasKeyframeAt reports whether a keyframe starts exactly at t.
func (ix *Index) HasKeyframeAt(t int64) bool {
	for _, k := range ix.Keys {
		if k.TimeNs == t {
			return true
		}
	}
	return false
}
