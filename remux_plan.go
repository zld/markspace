package mediacore

import (
	"io"
	"os"
)

// validateTrackMapAgainstSource checks mapping consistency against the
// actual sample tables before any output is created.
func validateTrackMapAgainstSource(src *Container, tm *TrackMap) error {
	if len(tm.Tracks) != len(src.Tracks) {
		return fail(KindTrackMap, "remux", "mapping covers %d of %d tracks", len(tm.Tracks), len(src.Tracks))
	}
	for _, mt := range tm.Tracks {
		srcTrack := src.TrackByID(mt.SourceID)
		if srcTrack == nil {
			return fail(KindTrackMap, "remux", "mapped source track %d does not exist", mt.SourceID)
		}
		if t := tm.ByTarget[mt.TargetID]; t < 0 {
			return fail(KindTrackMap, "remux", "target %d has no index", mt.TargetID)
		}
	}
	return nil
}

// resolveRanges expands a per-track range selection into concrete sample
// indices and rejects overlapping, reversed or out-of-bounds ranges.
func resolveRanges(src *Container, ranges map[uint32][]SampleRange) (map[uint32][]int, error) {
	out := map[uint32][]int{}
	if ranges == nil {
		return out, nil
	}
	for id, rs := range ranges {
		tr := src.TrackByID(id)
		if tr == nil {
			return nil, fail(KindIndex, "remux", "range refers to unknown track %d", id)
		}
		seen := make([]bool, len(tr.Samples))
		var idx []int
		for _, r := range rs {
			if r.Start < 0 || r.End > len(tr.Samples) || r.Start >= r.End {
				return nil, fail(KindIndex, "remux", "track %d range [%d,%d) is invalid for %d samples", id, r.Start, r.End, len(tr.Samples))
			}
			for i := r.Start; i < r.End; i++ {
				if seen[i] {
					return nil, fail(KindIndex, "remux", "track %d range overlaps at sample %d", id, i)
				}
				seen[i] = true
				idx = append(idx, i)
			}
		}
		out[id] = idx
	}
	return out, nil
}

// buildMuxTracks converts demuxed tracks into writer-friendly tracks under
// the target numbering, preserving everything codec-specific.
func buildMuxTracks(src *Container, tm *TrackMap, selection map[uint32][]int) ([]muxTrack, error) {
	var out []muxTrack
	for _, mt := range tm.Tracks {
		srcTrack := src.TrackByID(mt.SourceID)
		if srcTrack == nil {
			return nil, fail(KindTrackMap, "remux", "mapped source track %d does not exist", mt.SourceID)
		}
		m := muxTrack{
			SourceID:  mt.SourceID,
			ID:        mt.TargetID,
			Kind:      srcTrack.Kind,
			Handler:   srcTrack.HandlerType,
			Timescale: srcTrack.Timescale,
			Width:     srcTrack.Width,
			Height:    srcTrack.Height,
			Channels:  srcTrack.Channels,
		}
		if mt.Preserved {
			m.RawEntry = rawSampleEntry(src, srcTrack)
			if m.Handler == "" {
				m.Handler = "meta"
			}
		}
		indices := selection[mt.SourceID]
		if indices == nil {
			for i := range srcTrack.Samples {
				indices = append(indices, i)
			}
		}
		for _, i := range indices {
			s := srcTrack.Samples[i]
			m.Samples = append(m.Samples, muxSample{
				Size: s.Size, DTS: s.DTS, Dur: s.Duration, CTS: s.CTS, Sync: s.Sync,
			})
		}
		out = append(out, m)
	}
	return out, nil
}

func rawSampleEntry(src *Container, t *Track) []byte {
	f, err := os.Open(src.Path)
	if err != nil {
		return nil
	}
	defer f.Close()
	if _, err := f.Seek(t.SampleEntry.Offset, 0); err != nil {
		return nil
	}
	buf := make([]byte, t.SampleEntry.Size)
	if _, err := io.ReadFull(f, buf); err != nil {
		return nil
	}
	return buf
}

// remuxPayloadPlan is the mapping needed to copy source samples into the
// destination mdat in the same order the writer expects.
