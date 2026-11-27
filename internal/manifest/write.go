// Package manifest defines the committed review manifest and its on-disk form.
//
// The manifest is the durable record a reviewer checks against. It is written
// only through txn's atomic-rename path and read back for verification. Field
// order and encoding are fixed so two runs over the same inputs produce
// byte-identical manifests.
package manifest

import (
	"encoding/json"
	"os"

	"media.rebuild/internal/model"
)

// Version is the manifest schema version.
const Version = 1

// Artifact is one published file recorded in the manifest.
type Artifact struct {
	Name   string `json:"name"`
	File   string `json:"file"`
	Bytes  int    `json:"bytes"`
	SHA256 string `json:"sha256"`
}

// ItemConclusion is the per-item verdict a reviewer needs.
type ItemConclusion struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Track  string `json:"track"`
	Source string `json:"source"`
	Line   int    `json:"line,omitempty"`
	TimeNs int64  `json:"time_ns"`
	SkewNs int64  `json:"skew_ns"`
	Over   bool   `json:"over_budget"`
	Group  string `json:"group"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// Anomaly records one over-budget span.
type Anomaly struct {
	GroupIDs  []string `json:"group_ids"`
	StartNs   int64    `json:"start_ns"`
	EndNs     int64    `json:"end_ns"`
	MaxSkewNs int64    `json:"max_skew_ns"`
}

// FrameRecord ties a rendered frame to its source event.
type FrameRecord struct {
	Index       int    `json:"index"`
	TimeNs      int64  `json:"time_ns"`
	SourceEvent string `json:"source_event"`
	File        string `json:"file"`
	Bytes       int    `json:"bytes"`
	SHA256      string `json:"sha256"`
}

// InputDigest records one input file's identity.
type InputDigest struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Bytes  int    `json:"bytes"`
	SHA256 string `json:"sha256"`
}

// Summary is the acceptance record printed and persisted by the tool.
type Summary struct {
	ItemsTotal      int              `json:"items_total"`
	ItemsOK         int              `json:"items_ok"`
	ItemsError      int              `json:"items_error"`
	MaxSkewMs       int64            `json:"max_skew_ms"`
	CoverageStartNs int64            `json:"coverage_start_ns"`
	CoverageEndNs   int64            `json:"coverage_end_ns"`
	MaxSkewNs       int64            `json:"max_skew_ns"`
	ThresholdMs     int64            `json:"threshold_ms"`
	OverBudget      int              `json:"over_budget"`
	AnomalyRanges   int              `json:"anomaly_ranges"`
	Frames          int              `json:"frames"`
	FrameDigest     string           `json:"frame_sha256"`
	RTPDuplicate    int              `json:"rtp_duplicate"`
	RTPGap          int              `json:"rtp_gap"`
	RTPMissing      uint64           `json:"rtp_missing"`
	MP4Invalid      int              `json:"mp4_invalid_samples"`
	Inputs          []InputDigest    `json:"inputs"`
	Items           []ItemConclusion `json:"items"`
	Anomalies       []Anomaly        `json:"anomalies"`
	FrameList       []FrameRecord    `json:"frame_list"`
	Errors          []string         `json:"errors,omitempty"`
}

// Manifest is the published document.
type Manifest struct {
	Version    int        `json:"version"`
	Task       string     `json:"task"`
	Generation int        `json:"generation"`
	Committed  bool       `json:"committed"`
	Artifacts  []Artifact `json:"artifacts"`
	Summary    *Summary   `json:"summary,omitempty"`
}

// Encode serializes deterministically with a trailing newline.
func (m *Manifest) Encode() ([]byte, error) {
	if m.Artifacts == nil {
		m.Artifacts = []Artifact{}
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, model.New(model.CodeIO, model.Source{Name: "manifest", Offset: -1}, "encode manifest", err)
	}
	return append(data, '\n'), nil
}

// Read loads and validates a manifest.
func Read(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, model.New(model.CodeIO, model.Source{Name: path, Offset: -1}, "read manifest", err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, model.New(model.CodeJournalCorrupt, model.Source{Name: path, Offset: -1}, "manifest does not decode", err)
	}
	if m.Version != Version {
		return nil, model.Newf(model.CodeTxnConflict, model.Source{Name: path, Offset: -1},
			"manifest version %d, kernel expects %d", m.Version, Version)
	}
	return &m, nil
}

// Write writes the manifest through a temporary file and atomic rename.
// A zero Version is stamped with the current schema version.
func Write(path string, m *Manifest) error {
	if m.Version == 0 {
		m.Version = Version
	}
	data, err := m.Encode()
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return model.New(model.CodeIO, model.Source{Name: tmp, Offset: -1}, "write manifest temp", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return model.New(model.CodeIO, model.Source{Name: path, Offset: -1}, "rename manifest", err)
	}
	return nil
}
