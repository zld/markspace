// Package index persists deterministic local batch indexes and recovery
// checkpoints. It has no network or service dependencies.
package index

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"

	"replaycheck/internal/clock"
)

// InputSummary is the stable digest and size of one input file.
type InputSummary struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// BlockRecord identifies one confirmed processing block/chunk.
type BlockRecord struct {
	Key               string `json:"key"`
	File              string `json:"file"`
	Sequence          int    `json:"sequence"`
	StartNS           int64  `json:"start_ns"`
	EndNS             int64  `json:"end_ns"`
	InputOffset       int64  `json:"input_offset,omitempty"`
	Tracks            int    `json:"tracks_checked,omitempty"`
	WrapCount         int    `json:"wrap_count,omitempty"`
	MaxDriftNS        int64  `json:"max_drift_ns,omitempty"`
	MaxBoundaryNS     int64  `json:"max_boundary_ns,omitempty"`
	InputDurationNS   int64  `json:"input_duration_ns,omitempty"`
	PlannedClipNS     int64  `json:"planned_clip_ns,omitempty"`
	ActualClipNS      int64  `json:"actual_clip_ns,omitempty"`
	AnomalyDurationNS int64  `json:"anomaly_duration_ns,omitempty"`
}

// FileConclusion is the item-level processing result printed and indexed.
type FileConclusion struct {
	Name          string          `json:"name,omitempty"`
	File          string          `json:"file"`
	Status        string          `json:"status"` // pass, warning, failed
	ExitCode      int             `json:"exit_code,omitempty"`
	Tracks        int             `json:"tracks_checked"`
	WrapCount     int             `json:"wrap_count"`
	DriftMS       float64         `json:"drift_ms"`
	MaxBoundaryMS float64         `json:"vfr_boundary_error_ms"`
	AnomalyCount  int             `json:"anomaly_count"`
	Message       string          `json:"message,omitempty"`
	Anomalies     []clock.Anomaly `json:"anomalies,omitempty"`
}

// Index is the deterministic on-disk index record.
type Index struct {
	BasisVersion       string           `json:"basis_version"`
	InputCount         int              `json:"input_count"`
	Inputs             []InputSummary   `json:"inputs"`
	CompletedBlocks    []BlockRecord    `json:"completed_blocks"`
	Conclusions        []FileConclusion `json:"conclusions"`
	Anomalies          []clock.Anomaly  `json:"anomalies"`
	AnomalyRate        float64          `json:"anomaly_rate"`
	BudgetDeviationPct float64          `json:"budget_deviation_pct"`
	IndexSHA256        string           `json:"index_sha256,omitempty"`
}

// CanonicalJSON encodes an index without its self-hash field. Callers should
// normally use Write, which adds the file hash in IndexSHA256.
func CanonicalJSON(idx Index) ([]byte, error) {
	idx.IndexSHA256 = ""
	return json.MarshalIndent(idx, "", "  ")
}

// Digest returns the SHA-256 of canonical index bytes.
func Digest(idx Index) (string, []byte, error) {
	b, err := CanonicalJSON(idx)
	if err != nil {
		return "", nil, err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), b, nil
}

// Write writes deterministic JSON and a sidecar SHA-256 file. The returned
// hash is the hash of the canonical body, which remains stable across reruns.
func Write(path string, idx Index) (string, error) {
	// IndexSHA256 is deliberately not embedded: the returned digest is the
	// SHA-256 of the exact bytes written to path and can be checked externally.
	idx.IndexSHA256 = ""
	full, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return "", err
	}
	full = append(full, '\n')
	sum := sha256.Sum256(full)
	hash := hex.EncodeToString(sum[:])
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := atomicWrite(path, full); err != nil {
		return "", err
	}
	if err := atomicWrite(path+".sha256", []byte(hash+"  "+filepath.Base(path)+"\n")); err != nil {
		return "", err
	}
	return hash, nil
}

// Read loads an index JSON file.
func Read(path string) (*Index, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var idx Index
	if err := json.Unmarshal(b, &idx); err != nil {
		return nil, err
	}
	return &idx, nil
}

// HashFile computes a bounded-by-caller SHA-256 digest.
func HashFile(path string) (InputSummary, error) {
	f, err := os.Open(path)
	if err != nil {
		return InputSummary{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return InputSummary{}, err
	}
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(f, containerMaxBytesPlusOne)); err != nil {
		return InputSummary{}, err
	}
	return InputSummary{Path: path, Size: st.Size(), SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}

const containerMaxBytesPlusOne = 2<<20 + 1

func atomicWrite(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
