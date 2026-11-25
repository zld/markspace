// Package timeline merges per-track frames onto one nanosecond timeline.
package timeline

import (
	"fmt"
	"sort"

	"medpipe/internal/fault"
	"medpipe/internal/ingest"
)

// Frame is a frame positioned on the shared nanosecond timeline.
type Frame struct {
	*ingest.Frame
	TimestampNS int64 `json:"timestamp_ns"`
}

// Result contains the stable merged timeline and duplicate accounting.
type Result struct {
	Frames     []Frame
	Duplicates int
}

type key struct {
	track uint16
	ns    int64
}

// Merge converts timestamps, deduplicates exact track/time collisions, and
// stably sorts the result. Ingest has already established checksum validity;
// among valid records the smallest original input offset wins.
func Merge(frames []*ingest.Frame) (*Result, error) {
	type winner struct {
		frame *ingest.Frame
		ns    int64
	}
	best := make(map[key]winner)
	firstSeen := make([]key, 0)
	inputCount := 0

	for _, f := range frames {
		if f == nil {
			continue
		}
		inputCount++
		ns, err := f.Timebase.ToNanoseconds(f.PTS)
		if err != nil {
			return nil, fault.New(fault.StageTimeline, "invalid_timestamp", f.Offset, int(f.TrackID), err)
		}
		k := key{track: f.TrackID, ns: ns}
		cur, ok := best[k]
		if !ok {
			best[k] = winner{frame: f, ns: ns}
			firstSeen = append(firstSeen, k)
			continue
		}
		// Lower offset is the deterministic tie-break. Equal offsets retain
		// the first observed record.
		if f.Offset < cur.frame.Offset {
			best[k] = winner{frame: f, ns: ns}
		}
	}

	out := &Result{Frames: make([]Frame, 0, len(firstSeen))}
	for _, k := range firstSeen {
		w := best[k]
		out.Frames = append(out.Frames, Frame{Frame: w.frame, TimestampNS: w.ns})
	}
	out.Duplicates = inputCount - len(out.Frames)
	if out.Duplicates < 0 {
		return nil, fault.New(fault.StageTimeline, "duplicate_accounting", -1, -1,
			fmt.Errorf("duplicate count %d is negative", out.Duplicates))
	}

	sort.SliceStable(out.Frames, func(i, j int) bool {
		return out.Frames[i].TimestampNS < out.Frames[j].TimestampNS
	})
	return out, nil
}

// Tracks returns the set of track IDs present after merging.
func (r *Result) Tracks() []uint16 {
	if r == nil {
		return nil
	}
	seen := make(map[uint16]struct{})
	var tracks []uint16
	for _, f := range r.Frames {
		if _, ok := seen[f.TrackID]; !ok {
			seen[f.TrackID] = struct{}{}
			tracks = append(tracks, f.TrackID)
		}
	}
	sort.Slice(tracks, func(i, j int) bool { return tracks[i] < tracks[j] })
	return tracks
}
