// Package manifest defines the versioned analysis manifest. The manifest
// is the durable contract between a scan and later cache/index/migration
// stages: every coordinate references the unified clock and fault model,
// and every record is content-addressed so replacement is detectable.
package manifest

import (
	"errors"
	"time"
)

// SchemaVersion is the manifest version this build writes.
const SchemaVersion = 2

// MinSupportedVersion is the oldest manifest the adapter can read.
const MinSupportedVersion = 1

// ErrUnsupportedVersion is returned when a manifest is newer than this
// build or older than MinSupportedVersion.
var ErrUnsupportedVersion = errors.New("unsupported manifest version")

// ErrMalformed means a manifest cannot be decoded or fails validation.
var ErrMalformed = errors.New("malformed manifest")

// SampleRow is one per-sample mapping row.
type SampleRow struct {
	Index       int    `json:"index"`
	Codec       string `json:"codec"`
	KeyFrame    bool   `json:"key_frame"`
	FirstSeq    uint64 `json:"first_seq"`
	LastSeq     uint64 `json:"last_seq"`
	ByteOffset  int64  `json:"byte_offset"`
	ByteLength  int64  `json:"byte_length"`
	TimeStart   int64  `json:"time_start"`
	TimeEnd     int64  `json:"time_end"`
	Duration    int64  `json:"duration"`
	NALTypes    []uint32 `json:"nal_types,omitempty"`
	FaultClasses []string `json:"fault_classes,omitempty"`
}

// FaultRow is one localized fault in manifest form.
type FaultRow struct {
	Class      string `json:"class"`
	Codec      string `json:"codec,omitempty"`
	SeqFirst   uint64 `json:"seq_first"`
	SeqLast    uint64 `json:"seq_last"`
	ByteOffset int64  `json:"byte_offset"`
	ByteLength int64  `json:"byte_length"`
	TimeStart  int64  `json:"time_start"`
	TimeEnd    int64  `json:"time_end"`
	NALType    uint32 `json:"nal_type,omitempty"`
	OverWindow bool   `json:"over_window,omitempty"`
	Detail     string `json:"detail,omitempty"`
}

// Identity records the content, container and metadata hashes that drive
// independent cache and index invalidation.
type Identity struct {
	ContentHash   string `json:"content_hash"`
	ContainerHash string `json:"container_hash"`
	MetadataHash  string `json:"metadata_hash"`
}

// Summary is the stable, path-and-wall-clock-free digest of a run. The
// same seed and input must always produce the identical summary.
type Summary struct {
	Version        int            `json:"version"`
	Codec          string         `json:"codec"`
	SampleCount    int            `json:"sample_count"`
	KeyFrameCount  int            `json:"key_frame_count"`
	FaultCount     int            `json:"fault_count"`
	FaultsByClass  map[string]int `json:"faults_by_class"`
	MaxKeyInterval int64          `json:"max_key_interval_ticks"`
	ClockRate      uint32         `json:"clock_rate"`
	TimeTrusted    bool           `json:"time_trusted"`
}

// Manifest is the current (v2) in-memory representation.
type Manifest struct {
	Version  int          `json:"version"`
	TrackID  uint32       `json:"track_id"`
	Identity Identity     `json:"identity"`
	Summary  Summary      `json:"summary"`
	Samples  []SampleRow  `json:"samples"`
	Faults   []FaultRow   `json:"faults"`
	// CreatedAt is metadata only and never participates in summaries or
	// hashes; adapters strip it when computing stable digests.
	CreatedAt time.Time `json:"created_at,omitempty"`
}

// Validate checks structural invariants independent of version. It does
// not touch the filesystem and therefore stays deterministic.
func (m *Manifest) Validate() error {
	if m.Version < MinSupportedVersion || m.Version > SchemaVersion {
		return ErrUnsupportedVersion
	}
	if m.Identity.ContentHash == "" {
		return ErrMalformed
	}
	for i, s := range m.Samples {
		if s.Index != i {
			return ErrMalformed
		}
		if s.FirstSeq > s.LastSeq || s.ByteLength < 0 || s.TimeEnd < s.TimeStart {
			return ErrMalformed
		}
	}
	if m.Summary.SampleCount != len(m.Samples) || m.Summary.FaultCount != len(m.Faults) {
		return ErrMalformed
	}
	return nil
}
