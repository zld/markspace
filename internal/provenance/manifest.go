// Package provenance writes the reviewable manifest that ties every output
// segment back to the ingest bytes that produced it.
//
// The manifest is deliberately free of wall-clock timestamps and map iteration
// order: two runs over identical inputs and configuration must serialize to
// byte-identical JSON, otherwise idempotency claims cannot be checked. Every
// volatile fact is expressed in media ticks instead.
package provenance

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"

	"mediarun/internal/fault"
)

// Version is the manifest schema version.
const Version = 1

// Input describes one ingest artifact.
type Input struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

// FaultRecord is the serializable form of fault.Error.
type FaultRecord struct {
	Stage       string `json:"stage"`
	Kind        string `json:"kind"`
	Track       uint8  `json:"track,omitempty"`
	Offset      int64  `json:"offset"`
	Record      int    `json:"record"`
	Detail      string `json:"detail,omitempty"`
	Recoverable bool   `json:"recoverable"`
}

// FromFault converts a typed fault into a record.
func FromFault(e *fault.Error) FaultRecord {
	return FaultRecord{
		Stage:       string(e.Stage),
		Kind:        string(e.Kind),
		Track:       e.Track,
		Offset:      e.Offset,
		Record:      e.Record,
		Detail:      e.Detail,
		Recoverable: e.Recoverable,
	}
}

// StageSummary is the checkpointable summary of one stage.
type StageSummary struct {
	Name       string         `json:"name"`
	InputHash  string         `json:"input_hash"`
	ConfigHash string         `json:"config_hash"`
	OutputHash string         `json:"output_hash"`
	Detail     string         `json:"detail,omitempty"`
	Counts     map[string]int `json:"counts,omitempty"`
	Faults     []FaultRecord  `json:"faults,omitempty"`
}

// Segment describes one remuxed output.
type Segment struct {
	Index        int           `json:"index"`
	Path         string        `json:"path"`
	Bytes        int64         `json:"bytes"`
	SHA256       string        `json:"sha256"`
	Samples      int           `json:"samples"`
	StartPTS     uint64        `json:"start_pts"`
	EndPTS       uint64        `json:"end_pts"`
	Keyframes    int           `json:"keyframes"`
	KeyframeHits int           `json:"keyframe_hits"`
	RoundtripHit int           `json:"roundtrip_hit"`
	Synced       bool          `json:"synced"`
	Incomplete   bool          `json:"incomplete"`
	AudioFrames  int           `json:"audio_frames"`
	AudioBytes   int           `json:"audio_bytes"`
	Faults       []FaultRecord `json:"faults,omitempty"`
}

// Metrics are the acceptance counters printed by the CLI.
type Metrics struct {
	Frames             int `json:"frames"`
	AudioPackets       int `json:"audio_packets"`
	ParseErrors        int `json:"parse_errors"`
	RecoveredAudio     int `json:"recovered_audio"`
	RecoveredVideo     int `json:"recovered_video"`
	DroppedAudio       int `json:"dropped_audio"`
	Segments           int `json:"segments"`
	KeyframeTotal      int `json:"keyframe_total"`
	KeyframeHits       int `json:"keyframe_hits"`
	RoundtripTotal     int `json:"roundtrip_total"`
	RoundtripHits      int `json:"roundtrip_hits"`
	IncompleteSegments int `json:"incomplete_segments"`
	UnsyncedSegments   int `json:"unsynced_segments"`
}

// Manifest is the full provenance document for one run.
type Manifest struct {
	Version     int            `json:"version"`
	Tool        string         `json:"tool"`
	ConfigHash  string         `json:"config_hash"`
	Timescale   uint32         `json:"timescale"`
	Inputs      []Input        `json:"inputs"`
	Stages      []StageSummary `json:"stages"`
	Segments    []Segment      `json:"segments"`
	Faults      []FaultRecord  `json:"faults"`
	Metrics     Metrics        `json:"metrics"`
	TimelineEnd int64          `json:"timeline_end"`
}

// HashBytesHex returns the hex SHA-256 of b.
func HashBytesHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// SortFaults orders fault records by offset, stage and kind for stable output.
// Records without a source offset sort last so byte-anchored faults lead.
func SortFaults(fs []FaultRecord) {
	sort.SliceStable(fs, func(i, j int) bool {
		ai, aj := fs[i].Offset, fs[j].Offset
		if (ai < 0) != (aj < 0) {
			return ai >= 0
		}
		if ai != aj {
			return ai < aj
		}
		if fs[i].Stage != fs[j].Stage {
			return fs[i].Stage < fs[j].Stage
		}
		return fs[i].Kind < fs[j].Kind
	})
}
