// Package index builds deterministic random-access indexes and reads ranges.
package index

import (
	"example.com/archiveverify/internal/model"
	"example.com/archiveverify/internal/rtp"
	"example.com/archiveverify/internal/timebase"
)

// Entry is one indexed media sample.
type Entry struct {
	TrackID     uint32   `json:"track_id"`
	Role        string   `json:"role,omitempty"`
	SourceID    string   `json:"source_id"`
	EventIndex  int      `json:"event_index"`
	Sequence    uint32   `json:"mfhd_sequence"`
	SampleIndex int      `json:"sample_index"`
	StartUs     model.Us `json:"start_us"`
	EndUs       model.Us `json:"end_us"`
	Timescale   uint32   `json:"timescale,omitempty"`
	RawStart    int64    `json:"raw_start,omitempty"`
	RawEnd      int64    `json:"raw_end,omitempty"`
	KeyFrame    bool     `json:"key_frame"`
	File        string   `json:"file"`
	BoxOffset   int64    `json:"box_offset"`
	ByteOffset  int64    `json:"byte_offset"`
	Size        uint32   `json:"size"`
}

// KeyFrame is a compact random-access point.
type KeyFrame struct {
	TrackID  uint32   `json:"track_id"`
	StartUs  model.Us `json:"start_us"`
	File     string   `json:"file"`
	Offset   int64    `json:"offset"`
	Sequence uint32   `json:"sequence"`
}

// FileRecord fingerprints an input file used by an index.
type FileRecord struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Index is the deterministic index document.
type Index struct {
	Version          int                `json:"version"`
	Case             string             `json:"case"`
	InputFingerprint string             `json:"input_fingerprint"`
	TimeMinUs        model.Us           `json:"time_min_us"`
	TimeMaxUs        model.Us           `json:"time_max_us"`
	Entries          []Entry            `json:"entries"`
	KeyFrames        []KeyFrame         `json:"keyframes"`
	Files            []FileRecord       `json:"files"`
	MFHDGaps         []model.Gap        `json:"mfhd_gaps"`
	Repairs          []model.Repair     `json:"repairs"`
	Quarantine       []model.Quarantine `json:"quarantined"`
}

// BuildResult bundles the index with non-index audit/recovery reports.
type BuildResult struct {
	Index    *Index                `json:"index"`
	Timeline *timebase.Report      `json:"timeline"`
	RTP      *rtp.Report           `json:"rtp,omitempty"`
	Anchors  []timebase.SyncResult `json:"anchors,omitempty"`
}

// Digest returns the stable digest stored separately from the document.
func (idx *Index) Digest() string {
	return DigestIndex(idx)
}
