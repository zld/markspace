// Package manifest reads and writes versioned release manifests.
// Each supported version has a strict schema: unknown fields are
// rejected, and versions newer than CurrentVersion are refused.
package manifest

import (
	"bytes"
	"encoding/json"

	"mediacheck/internal/fault"
)

// CurrentVersion is the manifest version this build publishes.
const CurrentVersion = 1

// SegmentEntry records one exported segment.
type SegmentEntry struct {
	Codec     string `json:"codec"`
	Index     int    `json:"index"`
	StartTick int64  `json:"start_tick"`
	EndTick   int64  `json:"end_tick"`
	SHA256    string `json:"sha256"`
	Packets   int    `json:"packets"`
}

// FrameEntry records one offscreen-decoded frame.
type FrameEntry struct {
	Codec  string  `json:"codec"`
	Index  int     `json:"index"`
	SHA256 string  `json:"sha256"`
	Min    int     `json:"min"`
	Max    int     `json:"max"`
	Mean   float64 `json:"mean"`
}

// Quality holds the measured quality gates.
type Quality struct {
	LoudnessPeakDBFS float64 `json:"loudness_peak_dbfs"`
	MaxAlignError    float64 `json:"max_align_error_ticks"`
}

// Manifest is the current (v1) release manifest.
type Manifest struct {
	Version  int            `json:"version"`
	Codecs   []string       `json:"codecs"`
	Timebase uint32         `json:"timebase"`
	Segments []SegmentEntry `json:"segments"`
	Frames   []FrameEntry   `json:"frames"`
	Quality  Quality        `json:"quality"`
}

// V0 is the legacy manifest layout: no timebase, frames or quality.
type V0 struct {
	Version  int            `json:"version"`
	Codecs   []string       `json:"codecs"`
	Segments []SegmentEntry `json:"segments"`
}

// Marshal serializes a v1 manifest deterministically.
func Marshal(m *Manifest) ([]byte, error) {
	m.Version = CurrentVersion
	return json.MarshalIndent(m, "", "  ")
}

// MarshalV0 serializes a legacy manifest deterministically.
func MarshalV0(v *V0) ([]byte, error) {
	v.Version = 0
	return json.MarshalIndent(v, "", "  ")
}

// Parse reads a manifest of any supported version and upgrades it to
// the current in-memory layout.
func Parse(data []byte) (*Manifest, error) {
	var head struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return nil, fault.New(fault.ErrUnknownField, fault.NoOffset, "manifest header: %v", err)
	}
	switch head.Version {
	case 0:
		var v0 V0
		if err := decodeStrict(data, &v0); err != nil {
			return nil, err
		}
		return &Manifest{Codecs: v0.Codecs, Segments: v0.Segments}, nil
	case CurrentVersion:
		var m Manifest
		if err := decodeStrict(data, &m); err != nil {
			return nil, err
		}
		return &m, nil
	default:
		return nil, fault.New(fault.ErrBadVersion, fault.NoOffset,
			"manifest version %d not supported (current %d)", head.Version, CurrentVersion)
	}
}

func decodeStrict(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fault.New(fault.ErrUnknownField, fault.NoOffset, "%v", err)
	}
	return nil
}
