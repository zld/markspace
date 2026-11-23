// Package manifest emits a deterministic JSON timeline that shares the
// audio sample clock with the muxer and quality report.
package manifest

import (
	"encoding/json"
	"os"

	"mediaforge/internal/mediaerr"
)

// SegmentStatus classifies one contiguous region of the timeline.
type SegmentStatus string

const (
	// StatusOK marks a fully decoded region.
	StatusOK SegmentStatus = "ok"
	// StatusMissing marks a sequence gap that was filled with silence.
	StatusMissing SegmentStatus = "missing"
	// StatusTruncated marks a packet cut short by the capture end.
	StatusTruncated SegmentStatus = "truncated"
	// StatusCorrupt marks a payload that failed integrity checks.
	StatusCorrupt SegmentStatus = "corrupt"
	// StatusReplayed marks a region recomputed after a late packet arrived.
	StatusReplayed SegmentStatus = "replayed"
)

// Segment is a half-open sample interval with its timeline position.
type Segment struct {
	Status       SegmentStatus `json:"status"`
	StartSample  int64         `json:"start_sample"`
	EndSample    int64         `json:"end_sample"`
	StartSeconds float64       `json:"start_seconds"`
	EndSeconds   float64       `json:"end_seconds"`
	SeqStart     uint16        `json:"seq_start,omitempty"`
	SeqEnd       uint16        `json:"seq_end,omitempty"`
}

// Timeline is the manifest document.
type Timeline struct {
	Version        int       `json:"version"`
	SampleRate     int       `json:"sample_rate"`
	Channels       int       `json:"channels"`
	RTPClockRate   int64     `json:"rtp_clock_rate"`
	DurationSample int64     `json:"duration_samples"`
	MediaFile      string    `json:"media_file"`
	MediaSHA256    string    `json:"media_sha256,omitempty"`
	Segments       []Segment `json:"segments"`
	// Missing lists unrecoverable sample ranges.
	Missing []Range `json:"missing_ranges,omitempty"`
	// Alignment is the absolute difference between the manifest end and the
	// muxed sample count.
	AlignmentSamples int64 `json:"alignment_samples"`
}

// Range is a half-open sample interval.
type Range struct {
	StartSample int64 `json:"start_sample"`
	EndSample   int64 `json:"end_sample"`
}

// MakeSegments converts ordered events into timeline segments, merging
// adjacent events that share a status.
func MakeSegments(rate int, events []Event) []Segment {
	segs := make([]Segment, 0, len(events))
	for _, e := range events {
		seg := Segment{
			Status: StatusOK, StartSample: e.StartSample, EndSample: e.EndSample,
			SeqStart: e.Seq, SeqEnd: e.Seq,
		}
		if e.Status != "" {
			seg.Status = e.Status
		}
		if n := len(segs); n > 0 && segs[n-1].Status == seg.Status &&
			segs[n-1].EndSample == seg.StartSample && seg.EndSample > seg.StartSample {
			segs[n-1].EndSample = seg.EndSample
			segs[n-1].SeqEnd = e.Seq
			continue
		}
		segs = append(segs, seg)
	}
	for i := range segs {
		segs[i].StartSeconds = float64(segs[i].StartSample) / float64(rate)
		segs[i].EndSeconds = float64(segs[i].EndSample) / float64(rate)
	}
	return segs
}

// Event is the minimal timeline entry the manifest needs.
type Event struct {
	Seq         uint16
	Status      SegmentStatus
	StartSample int64
	EndSample   int64
}

// Write serializes the timeline atomically to path with stable key order and
// no timestamps, so identical input always yields identical bytes.
func Write(path string, tl Timeline) error {
	tl.Version = 1
	if tl.Segments == nil {
		tl.Segments = []Segment{}
	}
	data, err := json.MarshalIndent(tl, "", "  ")
	if err != nil {
		return mediaerr.New(mediaerr.CodeIO, "marshal manifest: %v", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return mediaerr.New(mediaerr.CodeIO, "write manifest: %v", err)
	}
	return nil
}

// Read parses a manifest previously written by Write.
func Read(path string) (Timeline, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Timeline{}, mediaerr.New(mediaerr.CodeIO, "read manifest: %v", err)
	}
	var tl Timeline
	if err := json.Unmarshal(data, &tl); err != nil {
		return Timeline{}, mediaerr.New(mediaerr.CodeVerify, "parse manifest: %v", err)
	}
	return tl, nil
}
