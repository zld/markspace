package segment

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mediapack/model"
)

// ManifestVersion is the schema version emitted by this kernel.
const ManifestVersion = 1

// SegmentRef references one representation-specific segment file.
type SegmentRef struct {
	ID           string  `json:"id"`
	Track        string  `json:"track"`
	Rep          string  `json:"representation"`
	Bitrate      int     `json:"bitrate"`
	Index        int     `json:"index"`
	StartNanos   int64   `json:"start_nanos"`
	EndNanos     int64   `json:"end_nanos"`
	StartSeconds float64 `json:"start_seconds"`
	EndSeconds   float64 `json:"end_seconds"`
	Path         string  `json:"path"`
	SizeBytes    int     `json:"size_bytes"`
	SHA256       string  `json:"sha256"`
}

// ManifestEvent is a timed event annotated with the segments it intersects.
type ManifestEvent struct {
	Track          model.TrackID  `json:"track"`
	Kind           model.UnitKind `json:"kind"`
	Source         string         `json:"source,omitempty"`
	StartNanos     int64          `json:"start_nanos"`
	EndNanos       int64          `json:"end_nanos"`
	Text           string         `json:"text,omitempty"`
	Key            string         `json:"key,omitempty"`
	Value          string         `json:"value,omitempty"`
	SegmentIndices []int          `json:"segment_indices"`
	SegmentRefs    []string       `json:"segment_refs"`
}

// RepresentationManifest contains one bitrate's segment references.
type RepresentationManifest struct {
	ID       string       `json:"id"`
	Bitrate  int          `json:"bitrate_bps"`
	Width    int          `json:"width,omitempty"`
	Height   int          `json:"height,omitempty"`
	Segments []SegmentRef `json:"segments"`
}

// TrackManifest records track identity and timebase evidence.
type TrackManifest struct {
	ID       model.TrackID  `json:"id"`
	Kind     model.UnitKind `json:"kind"`
	Timebase string         `json:"timebase"`
}

// Manifest is the stable, content-addressed delivery manifest.
type Manifest struct {
	Version            int                      `json:"version"`
	Seed               int64                    `json:"seed"`
	Timebase           string                   `json:"timebase"`
	DurationNanos      int64                    `json:"duration_nanos"`
	SegmentTargetNanos int64                    `json:"segment_target_nanos"`
	Boundaries         []Boundary               `json:"boundaries"`
	Tracks             []TrackManifest          `json:"tracks"`
	Representations    []RepresentationManifest `json:"representations"`
	Events             []ManifestEvent          `json:"events"`
	Warnings           []model.Warning          `json:"warnings,omitempty"`
	ManifestSHA256     string                   `json:"manifest_sha256"`
}

// SafePath resolves name inside root and rejects absolute paths and traversal.
func SafePath(root, name string) (string, error) {
	if name == "" || filepath.IsAbs(name) {
		return "", model.NewError("", name, -1, model.CategoryPathTraversal, "segment path must be relative and non-empty", nil)
	}
	cleanName := filepath.Clean(name)
	if cleanName == ".." || strings.HasPrefix(cleanName, ".."+string(filepath.Separator)) {
		return "", model.NewError("", name, -1, model.CategoryPathTraversal, "path traversal rejected", nil)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", model.NewError("", root, -1, model.CategoryOutputWrite, "cannot resolve output directory", err)
	}
	target := filepath.Join(absRoot, cleanName)
	rel, err := filepath.Rel(absRoot, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", model.NewError("", name, -1, model.CategoryPathTraversal, "path escapes output directory", err)
	}
	return target, nil
}

// WriteFileSafe writes data under root after path-traversal validation.
func WriteFileSafe(root, name string, data []byte) (string, error) {
	target, err := SafePath(root, name)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", model.NewError("", name, -1, model.CategoryOutputWrite, "cannot create output directory", err)
	}
	if err := os.WriteFile(target, data, 0o644); err != nil {
		return "", model.NewError("", name, -1, model.CategoryOutputWrite, "cannot write output file", err)
	}
	return target, nil
}

// SHA256Bytes returns the lowercase hex SHA-256 of data.
func SHA256Bytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Digest returns the stable manifest checksum excluding ManifestSHA256 itself.
func (m *Manifest) Digest() (string, error) {
	copyManifest := *m
	copyManifest.ManifestSHA256 = ""
	data, err := json.Marshal(copyManifest)
	if err != nil {
		return "", err
	}
	return SHA256Bytes(data), nil
}

// FillChecksum computes and sets ManifestSHA256.
func (m *Manifest) FillChecksum() error {
	digest, err := m.Digest()
	if err != nil {
		return err
	}
	m.ManifestSHA256 = digest
	return nil
}

// WriteManifest writes indented JSON and returns its path.
func WriteManifest(root string, m *Manifest) (string, error) {
	if err := m.FillChecksum(); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return "", err
	}
	data = append(data, '\n')
	return WriteFileSafe(root, "manifest.json", data)
}

// ReadManifest reads and verifies a manifest's self-checksum.
func ReadManifest(root string) (*Manifest, string, error) {
	path, err := SafePath(root, "manifest.json")
	if err != nil {
		return nil, "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", model.NewError("", "manifest.json", -1, model.CategoryOutputWrite, "cannot read manifest", err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, "", model.NewError("", "manifest.json", -1, model.CategoryChecksumMismatch, "manifest JSON is corrupt", err)
	}
	digest, err := m.Digest()
	if err != nil {
		return nil, "", err
	}
	if m.ManifestSHA256 != digest {
		return &m, digest, model.NewError("", "manifest.json", -1, model.CategoryChecksumMismatch,
			fmt.Sprintf("manifest digest mismatch: file=%s recomputed=%s", m.ManifestSHA256, digest), nil)
	}
	return &m, digest, nil
}

// VerifySegment verifies size and SHA-256 for an existing segment. Missing is
// not an error: callers use the boolean to regenerate interrupted work.
func VerifySegment(root string, ref SegmentRef) (exists bool, err error) {
	path, err := SafePath(root, ref.Path)
	if err != nil {
		return false, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return true, model.NewError(model.TrackID(ref.Track), ref.Path, -1, model.CategoryOutputWrite, "cannot read segment", err)
	}
	if len(data) != ref.SizeBytes {
		return true, model.NewError(model.TrackID(ref.Track), ref.Path, -1, model.CategoryChecksumMismatch,
			fmt.Sprintf("size mismatch: manifest=%d disk=%d", ref.SizeBytes, len(data)), nil)
	}
	if got := SHA256Bytes(data); got != ref.SHA256 {
		return true, model.NewError(model.TrackID(ref.Track), ref.Path, -1, model.CategoryChecksumMismatch,
			fmt.Sprintf("sha256 mismatch: manifest=%s disk=%s", ref.SHA256, got), nil)
	}
	return true, nil
}

// VerificationResult reports resume evidence.
type VerificationResult struct {
	VerifiedSegments int          `json:"verified_segments"`
	MissingSegments  []SegmentRef `json:"missing_segments"`
}

// VerifyAllSegments checks every reference and stops on an existing bad digest.
func VerifyAllSegments(root string, m *Manifest) (VerificationResult, error) {
	res := VerificationResult{}
	for _, rep := range m.Representations {
		for _, ref := range rep.Segments {
			exists, err := VerifySegment(root, ref)
			if err != nil {
				return res, err
			}
			if exists {
				res.VerifiedSegments++
			} else {
				res.MissingSegments = append(res.MissingSegments, ref)
			}
		}
	}
	return res, nil
}
