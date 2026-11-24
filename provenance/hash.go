// Package provenance computes content hashes and records deterministic origin
// and processing metadata for generated media segments.
package provenance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"example.com/media-kernel/media"
)

// SchemaVersion is the manifest schema version.
const SchemaVersion = "provenance-v1"

// ParseVersion identifies parser semantics, not the wall-clock build.
const ParseVersion = "media-kernel-1;go-mp4-v1.4.1;pion-rtp-v1.8.11"

// Source describes the exact input from which a result was produced.
type Source struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	SizeBytes int64  `json:"size_bytes"`
	SHA256    string `json:"sha256"`
	Generated bool   `json:"generated"`
}

// SegmentSummary is the auditable summary of one output segment.
type SegmentSummary struct {
	ID         string          `json:"id"`
	Index      int             `json:"index"`
	StartTicks int64           `json:"start_ticks"`
	EndTicks   int64           `json:"end_ticks"`
	Timebase   media.Timebase  `json:"timebase"`
	ByteRange  media.ByteRange `json:"byte_range"`
	SHA256     string          `json:"sha256"`
	Errors     []*media.Error  `json:"errors"`
}

// Manifest is the deterministic result record written by the verifier.
type Manifest struct {
	SchemaVersion  string           `json:"schema_version"`
	ParseVersion   string           `json:"parse_version"`
	Input          Source           `json:"input"`
	ParametersHash string           `json:"parameters_hash"`
	Segments       []SegmentSummary `json:"segments"`
	Errors         []*media.Error   `json:"errors,omitempty"`
	Metrics        map[string]any   `json:"metrics,omitempty"`
}

// HashBytes returns the lowercase hex SHA-256 of data.
func HashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// HashReader copies r into a SHA-256 hasher.
func HashReader(r io.Reader) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// HashFile hashes a whole file.
func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	return HashReader(f)
}

// HashRange hashes a half-open byte range from data.
func HashRange(data []byte, r media.ByteRange) (string, error) {
	if r.Start < 0 || r.End < r.Start || r.End > int64(len(data)) {
		return "", media.NewError("provenance", media.CodeInvalidArgument,
			fmt.Sprintf("invalid hash range %d..%d for %d bytes", r.Start, r.End, len(data)), nil)
	}
	return HashBytes(data[r.Start:r.End]), nil
}

// HashParameters canonicalizes and hashes run parameters.
func HashParameters(params any) (string, error) {
	b, err := json.Marshal(params)
	if err != nil {
		return "", media.NewError("provenance", media.CodeInvalidArgument, "cannot encode parameters", err)
	}
	return HashBytes(b), nil
}

// CanonicalJSON renders stable JSON for records and tests.
func CanonicalJSON(v any) ([]byte, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, media.NewError("provenance", media.CodeInvalidArgument, "cannot encode provenance", err)
	}
	return append(b, '\n'), nil
}

// NewManifest creates a manifest with current parser metadata.
func NewManifest(source Source, params any) (*Manifest, error) {
	ph, err := HashParameters(params)
	if err != nil {
		return nil, err
	}
	return &Manifest{
		SchemaVersion:  SchemaVersion,
		ParseVersion:   ParseVersion,
		Input:          source,
		ParametersHash: ph,
		Metrics:        map[string]any{},
	}, nil
}
