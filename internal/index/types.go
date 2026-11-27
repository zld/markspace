// Package index scans MP4/MOV containers into a compact, hashable summary
// index and persists it atomically with integrity verification.
package index

import "mediasync/internal/model"

// SampleBoundary marks one sample inside a track: its file byte range and
// start time on the shared nanosecond timeline.
type SampleBoundary struct {
	Index     int         `json:"index"`
	Offset    int64       `json:"offset"`
	Size      int64       `json:"size"`
	StartNS   model.Ticks `json:"start_ns"`
	EndNS     model.Ticks `json:"end_ns"`
	SyncPoint bool        `json:"sync_point"`
}

// TrackIndex is the normalized description of one trak box.
type TrackIndex struct {
	TrackID    uint32           `json:"track_id"`
	Handler    string           `json:"handler"`
	Timescale  uint32           `json:"timescale"`
	StartNS    model.Ticks      `json:"start_ns"`
	EndNS      model.Ticks      `json:"end_ns"`
	SampleCount int             `json:"sample_count"`
	Samples    []SampleBoundary `json:"samples"`
}

// ScanResult is everything learned from one container file. It is fully
// deterministic for identical input bytes and is the payload of the index.
type ScanResult struct {
	InputPath   string         `json:"input_path"`
	InputSHA256 string         `json:"input_sha256"`
	InputSize   int64          `json:"input_size"`
	Timescale   uint32         `json:"movie_timescale"`
	StartNS     model.Ticks    `json:"start_ns"`
	EndNS       model.Ticks    `json:"end_ns"`
	Tracks      []TrackIndex   `json:"tracks"`
	Anomalies   []model.MediaError `json:"anomalies,omitempty"`
}

// FindTrack returns the first track whose handler equals h ("vide", "soun",
// "subt"/"text") or nil.
func (s *ScanResult) FindTrack(h string) *TrackIndex {
	for i := range s.Tracks {
		if s.Tracks[i].Handler == h {
			return &s.Tracks[i]
		}
	}
	return nil
}
