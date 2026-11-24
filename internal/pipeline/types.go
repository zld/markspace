package pipeline

import (
	"mediapipeline/internal/demux"
	"mediapipeline/internal/track"
)

// Stage names, in execution order.
const (
	StageParse    = "parse"
	StageMapping  = "map"
	StageRemux    = "remux"
	StageValidate = "validate"
)

// Summary is the machine-readable result of one pipeline run.
type Summary struct {
	Input  string `json:"input"`
	Output string `json:"output,omitempty"`

	Format     string   `json:"format"`
	Tracks     int      `json:"tracks"`
	TrackIDs   []uint32 `json:"track_ids"`
	EventsIn   int      `json:"events_in"`
	EventsOut  int      `json:"events_out"`
	Duplicates int      `json:"events_duplicate"`
	// EventOrderSHA256 fingerprints the ordered, de-duplicated event sequence
	// so a verifier can re-derive it from the output alone.
	EventOrderSHA256 string `json:"event_order_sha256"`

	PayloadSHA256In  string `json:"payload_sha256_in"`
	PayloadSHA256Out string `json:"payload_sha256_out"`

	OutputBytes    int64  `json:"output_bytes"`
	PayloadBytes   int64  `json:"payload_bytes"`
	EventTimescale uint32 `json:"event_timescale"`

	Cut       []demux.Range `json:"cut,omitempty"`
	Committed bool          `json:"committed"`
	Resumed   bool          `json:"resumed"`
	ResumeMS  int64         `json:"resume_ms,omitempty"`

	CommittedSegments int `json:"committed_segments,omitempty"`
	RecoveredSegments int `json:"recovered_segments,omitempty"`

	ErrorStage  string `json:"error_stage,omitempty"`
	ErrorOp     string `json:"error_op,omitempty"`
	InputOffset int64  `json:"input_offset,omitempty"`
	OffsetDelta int64  `json:"offset_delta,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

// Checkpoint is the on-disk breakpoint state written after every stage.
type Checkpoint struct {
	Stage          string         `json:"stage"`
	Input          string         `json:"input"`
	InputSHA256    string         `json:"input_sha256"`
	Tracks         []track.Entry  `json:"tracks"`
	Events         []EventRecord  `json:"events"`
	EventTimescale uint32         `json:"event_timescale"`
	Segments       []SegmentState `json:"segments,omitempty"`
	Committed      int            `json:"committed_segments,omitempty"`
	OutputSHA256   string         `json:"output_payload_sha256,omitempty"`
	OutputBytes    int64          `json:"output_bytes,omitempty"`
	UpdatedAt      string         `json:"updated_at"`
}

// EventRecord is a checkpoint-stable event description.
type EventRecord struct {
	Kind      string `json:"kind"`
	TrackID   uint32 `json:"track_id"`
	Ticks     int64  `json:"ticks"`
	Timescale uint32 `json:"timescale"`
	Digest    string `json:"digest"`
}

// SegmentState records one recovered fragment and whether it was committed to
// the output.  Re-running must never commit an already committed segment.
type SegmentState struct {
	ID         int    `json:"id"`
	MoofOffset int64  `json:"moof_offset"`
	Size       int64  `json:"size"`
	Digest     string `json:"digest"`
	Committed  bool   `json:"committed"`
}
