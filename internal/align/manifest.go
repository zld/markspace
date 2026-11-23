// Package align maps DASH representations and HLS discontinuity intervals
// onto one nanosecond timeline and produces the reviewable manifest draft.
package align

import "mediadeliver/internal/model"

// Schema identifies the manifest layout.
const Schema = "mediadeliver.manifest/v1"

// MaxDriftNS is the delivery tolerance for a shared segment boundary.
const MaxDriftNS int64 = 1_000_000

// InputRef is one hashed input file used to build the manifest.
type InputRef struct {
	Role           string `json:"role"` // mpd, hls or media
	Representation string `json:"representation,omitempty"`
	Path           string `json:"path"`
	SHA256         string `json:"sha256"`
	Size           int64  `json:"size"`
}

// Manifest is the aligned, reviewable draft.
type Manifest struct {
	Schema          string           `json:"schema"`
	Inputs          []InputRef       `json:"inputs"`
	Representations []Representation `json:"representations"`
	Intervals       []Interval       `json:"intervals"`
	Errors          []*model.Error   `json:"errors,omitempty"`
	Summary         Summary          `json:"summary"`
}

// Representation summarises one DASH representation's alignment result.
type Representation struct {
	ID           string `json:"id"`
	MimeType     string `json:"mime_type,omitempty"`
	Codecs       string `json:"codecs,omitempty"`
	Timescale    uint32 `json:"timescale"`
	Bandwidth    int64  `json:"bandwidth"`
	MediaPath    string `json:"media_path,omitempty"`
	MediaSHA256  string `json:"media_sha256,omitempty"`
	SegmentCount int    `json:"segment_count"`
	StartNS      int64  `json:"start_ns"`
	EndNS        int64  `json:"end_ns"`
	Deliverable  bool   `json:"deliverable"`
	ErrorCode    string `json:"error_code,omitempty"`
	SourceOffset int64  `json:"source_offset"`
	SourcePath   string `json:"source_path,omitempty"`
}

// Interval is one aligned time span; exactly one per HLS media segment.
type Interval struct {
	Index              int            `json:"index"`
	DiscontinuitySeq   uint64         `json:"discontinuity_seq"`
	HLSIntervalIndex   int            `json:"hls_interval_index"`
	HLSIntervalStartNS int64          `json:"hls_interval_start_ns"`
	IntervalOffsetNS   int64          `json:"interval_offset_ns"`
	StartNS            int64          `json:"start_ns"`
	EndNS              int64          `json:"end_ns"`
	HLSMediaSequences  []uint64       `json:"hls_media_sequences"`
	Deliverable        bool           `json:"deliverable"`
	MaxDriftNS         int64          `json:"max_drift_ns"`
	Entries            []Entry        `json:"entries"`
	Errors             []*model.Error `json:"errors,omitempty"`
}

// Entry is one representation's slice of an interval.
type Entry struct {
	RepresentationID string   `json:"representation_id"`
	StartNS          int64    `json:"start_ns"`
	EndNS            int64    `json:"end_ns"`
	DriftNS          int64    `json:"drift_ns"`
	SegmentNumbers   []uint64 `json:"segment_numbers,omitempty"`
	SourceOffset     int64    `json:"source_offset"`
	MPDDigest        string   `json:"mpd_sha256"`
	MediaOffset      int64    `json:"media_offset"`
	MediaSize        int64    `json:"media_size"`
	MediaDigest      string   `json:"media_sha256,omitempty"`
	ScanSampleCount  uint32   `json:"scan_sample_count"`
	Corrupt          bool     `json:"corrupt"`
	CorruptReason    string   `json:"corrupt_reason,omitempty"`
	Deliverable      bool     `json:"deliverable"`
}

// Summary is the aggregate view used by the demo and by verification.
type Summary struct {
	Representations int   `json:"representations"`
	Segments        int   `json:"segments"`
	Intervals       int   `json:"intervals"`
	MaxDriftNS      int64 `json:"max_drift_ns"`
	Diagnostics     int   `json:"diagnostics"`
	Deliverable     bool  `json:"deliverable"`
}
