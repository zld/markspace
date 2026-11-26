// Package mp4ref reads the self-generated MP4 timeline comparison sample
// with github.com/abema/go-mp4.
package mp4ref

import (
	"os"

	"github.com/abema/go-mp4"
)

// TrackSummary is the timeline information pwflow uses from an MP4 track.
type TrackSummary struct {
	ID              uint32 `json:"id"`
	Type            string `json:"type"`
	Timescale       uint32 `json:"timescale"`
	Duration        uint64 `json:"duration"`
	DurationNS      int64  `json:"duration_ns"`
	SampleCount     int    `json:"sample_count"`
}

// Summary is the stable MP4 probe result.
type Summary struct {
	Path       string         `json:"path"`
	Timescale  uint32         `json:"timescale"`
	Duration   uint64         `json:"duration"`
	DurationNS int64          `json:"duration_ns"`
	Tracks     []TrackSummary `json:"tracks"`
}

// ProbeFile opens path and uses go-mp4 to read movie/track durations.
func ProbeFile(path string) (*Summary, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := mp4.Probe(f)
	if err != nil {
		return nil, err
	}
	s := &Summary{Path: path, Timescale: info.Timescale, Duration: info.Duration}
	if info.Timescale > 0 {
		s.DurationNS = int64(info.Duration) * 1_000_000_000 / int64(info.Timescale)
	}
	for _, t := range info.Tracks {
		ts := TrackSummary{ID: t.TrackID, Timescale: t.Timescale, Duration: t.Duration}
		if t.Timescale > 0 {
			ts.DurationNS = int64(t.Duration) * 1_000_000_000 / int64(t.Timescale)
		}
		switch {
		case t.AVC != nil:
			ts.Type = "video"
		case t.MP4A != nil:
			ts.Type = "audio"
		default:
			ts.Type = "unknown"
		}
		ts.SampleCount = len(t.Samples)
		s.Tracks = append(s.Tracks, ts)
	}
	return s, nil
}
