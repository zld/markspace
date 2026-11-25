package pipeline

import (
	"fmt"

	"mediapack/ingest"
	"mediapack/model"
	"mediapack/sample"
	"mediapack/segment"
)

func segmentPath(repID string, index int) string {
	return fmt.Sprintf("%s/segment-%03d.m4s", repID, index)
}

// buildSegmentFiles creates deterministic MP4 segment bytes and content refs.
func buildSegmentFiles(s *sample.Sample, intervals []segment.Interval) ([]segment.RepresentationManifest, map[string][]byte, error) {
	files := make(map[string][]byte)
	reps := make([]segment.RepresentationManifest, 0, len(s.Reps))

	for r, rep := range s.Reps {
		rm := segment.RepresentationManifest{
			ID: rep.ID, Bitrate: rep.Bitrate, Width: rep.Width, Height: rep.Height,
			Segments: make([]segment.SegmentRef, 0, len(intervals)),
		}
		for _, iv := range intervals {
			samples := make([]ingest.MP4Sample, 0)
			for _, u := range s.Video[r] {
				if u.PTSNanos >= iv.StartNanos && u.PTSNanos < iv.EndNanos {
					samples = append(samples, ingest.MP4Sample{
						Index: u.Index, PTSNanos: u.PTSNanos, DurationNanos: u.DurationNanos,
						Keyframe: u.Keyframe, Payload: u.Payload,
					})
				}
			}
			if len(samples) == 0 {
				return nil, nil, model.NewError(rep.Track, segmentPath(rep.ID, iv.Index), iv.StartNanos,
					model.CategoryInvalidArgument, "segment interval contains no samples", nil)
			}
			data, err := ingest.BuildFixtureMP4(samples)
			if err != nil {
				return nil, nil, model.NewError(rep.Track, segmentPath(rep.ID, iv.Index), iv.StartNanos,
					model.CategoryOutputWrite, "cannot encode segment", err)
			}
			path := segmentPath(rep.ID, iv.Index)
			files[path] = data
			rm.Segments = append(rm.Segments, segment.SegmentRef{
				ID: fmt.Sprintf("%s-seg-%03d", rep.ID, iv.Index), Track: string(rep.Track),
				Rep: rep.ID, Bitrate: rep.Bitrate, Index: iv.Index,
				StartNanos: iv.StartNanos, EndNanos: iv.EndNanos,
				StartSeconds: float64(iv.StartNanos) / 1e9,
				EndSeconds:   float64(iv.EndNanos) / 1e9,
				Path:         path, SizeBytes: len(data), SHA256: segment.SHA256Bytes(data),
			})
		}
		reps = append(reps, rm)
	}
	return reps, files, nil
}

func eventIntervals(ev model.Event, intervals []segment.Interval) []int {
	var out []int
	for _, iv := range intervals {
		end := ev.EndNanos
		if end <= ev.StartNanos {
			end = ev.StartNanos + 1
		}
		if ev.StartNanos < iv.EndNanos && end > iv.StartNanos {
			out = append(out, iv.Index)
		}
	}
	return out
}

func buildManifestEvents(events []model.Event, intervals []segment.Interval, reps []segment.RepresentationManifest) []segment.ManifestEvent {
	out := make([]segment.ManifestEvent, 0, len(events))
	for _, ev := range events {
		indices := eventIntervals(ev, intervals)
		refs := make([]string, 0, len(indices)*len(reps))
		for _, idx := range indices {
			for _, rep := range reps {
				refs = append(refs, segmentPath(rep.ID, idx))
			}
		}
		out = append(out, segment.ManifestEvent{
			Track: ev.Track, Kind: ev.Kind, Source: ev.Source,
			StartNanos: ev.StartNanos, EndNanos: ev.EndNanos,
			Text: ev.Text, Key: ev.Key, Value: ev.Value,
			SegmentIndices: indices, SegmentRefs: refs,
		})
	}
	return out
}
