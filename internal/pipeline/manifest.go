package pipeline

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"archive/internal/model"
	"archive/internal/render"
	"archive/internal/timeline"
)

// FrameRecord is the rebuildable per-frame manifest entry.
type FrameRecord struct {
	Index       int            `json:"index"`
	File        string         `json:"file"` // path relative to the output dir
	SampleIndex uint32         `json:"sample_index"`
	Start       model.Rational `json:"start"` // unified seconds
	Keyframe    bool           `json:"keyframe"`
}

// SegmentRecord binds one segment's frames, hash and transform record.
type SegmentRecord struct {
	Index       int                `json:"index"`
	Track       int                `json:"track"`
	Source      string             `json:"source"`
	SampleCount int                `json:"sample_count"`
	SHA256      string             `json:"sha256"`
	Frames      []FrameRecord      `json:"frames"`
	Transform   timeline.Transform `json:"transform"`
}

// StageSummary is the verifiable digest of one pipeline stage.
type StageSummary struct {
	Digest string `json:"digest"`
	Detail string `json:"detail"`
}

// Manifest is the rebuildable record of a completed run.
type Manifest struct {
	Version        int                     `json:"version"`
	Policy         timeline.Policy         `json:"policy"`
	InputHash      string                  `json:"input_hash"`
	Segments       []SegmentRecord         `json:"segments"`
	StageSummaries map[string]StageSummary `json:"stage_summaries"`
}

// ManifestVersion is the current manifest schema version.
const ManifestVersion = 1

// Digest is the SHA-256 of the manifest's canonical JSON encoding.
func (m *Manifest) Digest() (string, error) {
	data, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	return render.BytesDigest(data), nil
}

func manifestPath(dir string) string { return filepath.Join(dir, "manifest.json") }

// WriteManifest atomically writes the manifest.
func WriteManifest(dir string, m *Manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(manifestPath(dir), data)
}

// ReadManifest loads and validates a manifest.
func ReadManifest(dir string) (*Manifest, error) {
	data, err := os.ReadFile(manifestPath(dir))
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, model.StageFault(model.StageVerify, -1, -1, nil, "manifest is not valid JSON: %v", err)
	}
	if m.Version != ManifestVersion {
		return nil, model.StageFault(model.StageVerify, -1, -1, nil,
			"unsupported manifest version %d", m.Version)
	}
	return &m, nil
}

// CheckRelPath rejects absolute paths and path traversal in manifest
// file references.
func CheckRelPath(dir, rel string) (string, error) {
	if rel == "" || filepath.IsAbs(rel) {
		return "", fmt.Errorf("unsafe path %q", rel)
	}
	clean := filepath.Clean(rel)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes output directory", rel)
	}
	return filepath.Join(dir, clean), nil
}
