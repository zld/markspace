// Package manifest aggregates representations and segments into the
// multi-bitrate delivery manifest.
package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"

	"continuity/media"
	"continuity/planner"
	"continuity/segment"
)

// FileName is the manifest file name inside the output directory.
const FileName = "manifest.json"

// Representation describes one output representation layer.
type Representation struct {
	ID          int             `json:"id"`
	Width       uint16          `json:"width,omitempty"`
	Height      uint16          `json:"height,omitempty"`
	Channels    uint16          `json:"channels,omitempty"`
	SampleRate  uint32          `json:"sample_rate,omitempty"`
	ColorSource media.ColorMeta `json:"color_source"`
	ColorOutput media.ColorMeta `json:"color_output"`
}

// SegmentRef points at one segment artifact.
type SegmentRef struct {
	Rep        int    `json:"rep"`
	Index      int    `json:"index"`
	StartNanos int64  `json:"start_nanos"`
	EndNanos   int64  `json:"end_nanos"`
	SHA256     string `json:"sha256"`
	File       string `json:"file"`
}

// Manifest is the multi-bitrate segment manifest.
type Manifest struct {
	Version         int              `json:"version"`
	InputHashes     []string         `json:"input_hashes"`
	PlanHash        string           `json:"plan_hash"`
	SegmentNanos    int64            `json:"segment_nanos"`
	Representations []Representation `json:"representations"`
	Segments        []SegmentRef     `json:"segments"`
}

// Build assembles the manifest from the plan and the cut result.
func Build(inputs []*media.Container, plan *planner.Plan, res *segment.Result) *Manifest {
	m := &Manifest{
		Version:      1,
		PlanHash:     plan.PlanHash,
		SegmentNanos: plan.SegmentNanos,
	}
	for i, in := range inputs {
		m.InputHashes = append(m.InputHashes, in.FileSHA256)
		v := in.VideoTrack()
		rep := Representation{ID: i, Width: v.Width, Height: v.Height,
			ColorSource: plan.ColorSources[i], ColorOutput: plan.ColorOutput}
		for _, a := range plan.Audio {
			rep.Channels = a.ToChannels
			rep.SampleRate = a.ToRate
			break // one audio track per input in this kernel
		}
		m.Representations = append(m.Representations, rep)
	}
	for _, s := range res.Segments {
		m.Segments = append(m.Segments, SegmentRef{
			Rep: s.Rep, Index: s.Index,
			StartNanos: s.StartNanos, EndNanos: s.EndNanos,
			SHA256: s.SHA256, File: s.FileName(),
		})
	}
	return m
}

// Hash returns the SHA-256 of the canonical JSON encoding.
func Hash(m *Manifest) (string, error) {
	raw, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// Write persists the manifest into dir.
func Write(dir string, m *Manifest) error {
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, FileName), raw, 0o644)
}

// Read loads the manifest from dir.
func Read(dir string) (*Manifest, error) {
	raw, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return &m, nil
}
