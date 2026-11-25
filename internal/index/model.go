package index

// MappingRuleVersion is bumped whenever tick/source mapping semantics change.
const MappingRuleVersion = "timeline-rational-v1"

// Sample is one randomly addressable media sample.
type Sample struct {
	Index            int    `json:"index"`
	StableKey        string `json:"stable_key"`
	SourceEntryKey   string `json:"source_entry_key"`
	Manifest         string `json:"manifest"`
	Representation   string `json:"representation"`
	OriginalSequence int    `json:"original_sequence"`

	// File and byte position for random access.
	FilePath   string `json:"file_path"`
	ByteOffset int64  `json:"byte_offset"`
	ByteSize   int64  `json:"byte_size"`

	// Unified half-open interval [StartU, EndU).
	StartU int64 `json:"start_u"`
	EndU   int64 `json:"end_u"`

	// Original MP4/RTP timing, retained for audit.
	SourceTimescale int64 `json:"source_timescale"`
	SourceStart     int64 `json:"source_start"`
	SourceDuration  int64 `json:"source_duration"`

	RandomAccess bool `json:"random_access"`
}

// EntryIndex is the random-access index for one manifest source entry.
type EntryIndex struct {
	StableKey        string   `json:"stable_key"`
	Manifest         string   `json:"manifest"`
	Representation   string   `json:"representation"`
	EntryIndex       int      `json:"entry_index"`
	OriginalSequence int      `json:"original_sequence"`
	InputPath        string   `json:"input_path"`
	InputSHA256      string   `json:"input_sha256"`
	InitSHA256       string   `json:"init_sha256,omitempty"`
	MappingVersion   string   `json:"mapping_version"`
	ReferenceStartU  int64    `json:"reference_start_u"`
	DeviationU       int64    `json:"deviation_u"`
	Aligned          bool     `json:"aligned"`
	Timescale        int64    `json:"timescale"`
	Samples          []Sample `json:"samples"`
	Digest           string   `json:"digest"`
}

// RangeReport describes a half-open interval read and boundary deviations.
type RangeReport struct {
	RequestedStartU    int64    `json:"requested_start_u"`
	RequestedEndU      int64    `json:"requested_end_u"`
	SampleCount        int      `json:"sample_count"`
	FirstTick          int64    `json:"first_tick"`
	LastTick           int64    `json:"last_tick"`
	FirstByteOffset    int64    `json:"first_byte_offset"`
	LastByteEnd        int64    `json:"last_byte_end"`
	FirstTickDeviation int64    `json:"first_tick_deviation"`
	LastTickDeviation  int64    `json:"last_tick_deviation"`
	FirstByteDeviation int64    `json:"first_byte_deviation"`
	LastByteDeviation  int64    `json:"last_byte_deviation"`
	Samples            []Sample `json:"samples"`
}
