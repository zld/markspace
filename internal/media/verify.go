package media

import (
	"bytes"
	"io"

	mp4 "github.com/abema/go-mp4"
)

// TrackSummary is the parsed media-track information used by acceptance tests.
type TrackSummary struct {
	TrackID   uint32 `json:"track_id"`
	Timescale uint32 `json:"timescale"`
	Duration  uint64 `json:"duration"`
	Samples   int    `json:"samples"`
}

// Probe parses an MP4 seeker through go-mp4 v1.3.0.
func Probe(r io.ReadSeeker) (*mp4.ProbeInfo, error) { return mp4.Probe(r) }

// ProbeBytes is a convenience wrapper for hashed in-memory artifacts.
func ProbeBytes(b []byte) ([]TrackSummary, error) {
	info, err := mp4.Probe(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	out := make([]TrackSummary, 0, len(info.Tracks))
	for _, tr := range info.Tracks {
		out = append(out, TrackSummary{TrackID: tr.TrackID, Timescale: tr.Timescale,
			Duration: tr.Duration, Samples: len(tr.Samples)})
	}
	return out, nil
}
