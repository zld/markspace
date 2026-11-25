// Package manifest defines the reproducible material manifest and MP4 reader.
package manifest

import (
	"time"

	"rtptrace/internal/timeline"
)

// TrackKind distinguishes media track processing rules.
type TrackKind string

const (
	TrackVideo TrackKind = "video"
	TrackAudio TrackKind = "audio"
)

// ColorMetadata retains source color labels. Empty strings mean absent.
type ColorMetadata struct {
	Primaries    string   `json:"primaries,omitempty"`
	Transfer     string   `json:"transfer,omitempty"`
	Matrix       string   `json:"matrix,omitempty"`
	Source       string   `json:"source,omitempty"`
	Alternatives []string `json:"alternatives,omitempty"`
	Conflict     bool     `json:"conflict,omitempty"`
}

// AudioMetadata records channel layout and sampling rate explicitly.
type AudioMetadata struct {
	Channels   int    `json:"channels"`
	Layout     string `json:"layout"`
	SampleRate int    `json:"sample_rate"`
}

// Sample is one indexed MP4 sample-table entry in source order.
type Sample struct {
	Index              uint32 `json:"index"`
	DecodeTicks        int64  `json:"decode_ticks"`
	PresentationTicks  int64  `json:"presentation_ticks"`
	SizeBytes          int64  `json:"size_bytes"`
	Sync               bool   `json:"sync"`
	RTPTimestampOffset uint32 `json:"rtp_timestamp_offset"`
}

// Track is a normalized manifest track with source time base and edits.
type Track struct {
	ID                  uint32             `json:"track_id"`
	Kind                TrackKind          `json:"kind"`
	SourceTimescale     uint32             `json:"source_timescale"`
	StartTimeTicks      int64              `json:"start_time_ticks"`
	MovieTimescale      uint32             `json:"movie_timescale,omitempty"`
	EditList            *timeline.EditList `json:"edit_list,omitempty"`
	Samples             []Sample           `json:"samples"`
	Audio               *AudioMetadata     `json:"audio,omitempty"`
	Color               *ColorMetadata     `json:"color,omitempty"`
	DeclaredSampleCount uint32             `json:"declared_sample_count,omitempty"`
	// FaultSampleHint lets a deliberately truncated container report the exact
	// first missing sample even when the declared sample count is damaged.
	FaultSampleHint uint32 `json:"-"`
}

// Dependency is a directed processing edge: TrackID requires DependsOn.
type Dependency struct {
	TrackID   uint32 `json:"track_id"`
	DependsOn uint32 `json:"depends_on"`
}

// RepresentationTarget is one target bitrate ladder entry.
type RepresentationTarget struct {
	ID      string `json:"id"`
	Bitrate int    `json:"bitrate_bps"`
}

// FaultSpec tells the fixture generator which deterministic fault to inject.
type FaultSpec struct {
	Kind        string `json:"kind"`
	TrackID     uint32 `json:"track_id,omitempty"`
	Sample      uint32 `json:"sample,omitempty"`
	Recoverable bool   `json:"recoverable,omitempty"`
}

// Manifest is the complete input contract.
type Manifest struct {
	SchemaVersion   string                 `json:"schema_version"`
	Seed            int64                  `json:"seed"`
	Duration        time.Duration          `json:"duration_ns"`
	SegmentDuration time.Duration          `json:"segment_duration_ns"`
	Bitrates        []int                  `json:"bitrates"`
	Representations []RepresentationTarget `json:"representations"`
	Dependencies    []Dependency           `json:"dependencies,omitempty"`
	Tracks          []Track                `json:"tracks"`
	Fault           *FaultSpec             `json:"fault,omitempty"`
}

// CorruptionError identifies a concrete bad track and source sample.
type CorruptionError struct {
	Code        string `json:"code"`
	TrackID     uint32 `json:"track_id"`
	Sample      uint32 `json:"sample"`
	Message     string `json:"message"`
	Recoverable bool   `json:"recoverable"`
}

func (e *CorruptionError) Error() string {
	if e == nil {
		return ""
	}
	return e.Code + ": track " + uint32String(e.TrackID) + " sample " + uint32String(e.Sample) + ": " + e.Message
}

func uint32String(v uint32) string {
	if v == 0 {
		return "0"
	}
	var b [16]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}
