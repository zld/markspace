// Package provenance records which input bytes and processing checkpoints
// produced an artifact, and provides an independent verifier that re-reads the
// artifact rather than trusting the producing pipeline's in-memory state.
package provenance

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// CheckpointRef names a processing checkpoint that contributed to an artifact.
type CheckpointRef struct {
	ID     string `json:"id"`
	Offset int64  `json:"parse_offset"`
	Digest string `json:"digest"`
}

// InputRef names an input byte range that contributed to an artifact.
type InputRef struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Span   Span   `json:"span"`
}

// Span is a half-open byte range, duplicated here so provenance records do not
// depend on the parser package types.
type Span struct {
	Offset int64 `json:"offset"`
	Length int64 `json:"length"`
}

// Record ties an artifact to its inputs, config and checkpoints.
type Record struct {
	Version        int             `json:"version"`
	Artifact       string          `json:"artifact"`
	ArtifactSHA256 string          `json:"artifact_sha256"`
	ManifestSHA256 string          `json:"manifest_sha256"`
	ConfigDigest   string          `json:"config_digest"`
	InputDigest    string          `json:"input_digest"`
	Inputs         []InputRef      `json:"inputs"`
	Checkpoints    []CheckpointRef `json:"checkpoints"`
	Segments       int             `json:"segments"`
	SegmentHashes  []string        `json:"segment_hashes"`
}

// ErrInvalidRecord reports a record that cannot be verified structurally.
var ErrInvalidRecord = errors.New("provenance: invalid record")

// Validate checks required fields and digest shapes.
func (r *Record) Validate() error {
	if r == nil {
		return fmt.Errorf("%w: nil", ErrInvalidRecord)
	}
	if r.Artifact == "" || r.ArtifactSHA256 == "" || r.ConfigDigest == "" || r.InputDigest == "" {
		return fmt.Errorf("%w: missing required field", ErrInvalidRecord)
	}
	if len(r.SegmentHashes) != r.Segments {
		return fmt.Errorf("%w: %d hashes for %d segments", ErrInvalidRecord, len(r.SegmentHashes), r.Segments)
	}
	for _, h := range append([]string{r.ArtifactSHA256, r.ConfigDigest, r.InputDigest}, r.SegmentHashes...) {
		if len(h) != 64 {
			return fmt.Errorf("%w: digest %q is not SHA-256 hex", ErrInvalidRecord, h)
		}
		if _, err := hex.DecodeString(h); err != nil {
			return fmt.Errorf("%w: digest %q is not hex", ErrInvalidRecord, h)
		}
	}
	return nil
}

// MarshalCanonical returns canonical JSON bytes. Slices are sorted by stable
// keys first so the record bytes do not depend on discovery order.
func (r *Record) MarshalCanonical() ([]byte, error) {
	canon := *r
	canon.Inputs = append([]InputRef(nil), r.Inputs...)
	sort.Slice(canon.Inputs, func(i, j int) bool {
		if canon.Inputs[i].Path != canon.Inputs[j].Path {
			return canon.Inputs[i].Path < canon.Inputs[j].Path
		}
		return canon.Inputs[i].Span.Offset < canon.Inputs[j].Span.Offset
	})
	canon.Checkpoints = append([]CheckpointRef(nil), r.Checkpoints...)
	sort.Slice(canon.Checkpoints, func(i, j int) bool { return canon.Checkpoints[i].ID < canon.Checkpoints[j].ID })
	canon.SegmentHashes = append([]string(nil), r.SegmentHashes...)
	return json.Marshal(canon)
}

// WriteFile writes the canonical record JSON to path atomically.
func (r *Record) WriteFile(path string) error {
	if err := r.Validate(); err != nil {
		return err
	}
	b, err := r.MarshalCanonical()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// LoadFile reads and validates a record from disk.
func LoadFile(path string) (*Record, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var r Record
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidRecord, err)
	}
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return &r, nil
}
