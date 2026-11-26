package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"

	"pwflow/internal/fault"
	"pwflow/internal/timebase"
)

// InputDigest identifies all inputs that influence a run.
type InputDigest struct {
	Digest string            `json:"digest"`
	Files  map[string]string `json:"files"`
}

// CompletedSegment is the checkpoint record for one finished segment.
type CompletedSegment struct {
	Index        int               `json:"index"`
	Interval     timebase.Interval `json:"interval"`
	File         string            `json:"file"`
	FileSHA256   string            `json:"file_sha256"`
	ResultSHA256 string            `json:"result_sha256"`
}

// ManifestSummary is the stable subset used by checkpoint/resume checks.
type ManifestSummary struct {
	Status           string `json:"status"`
	SegmentCount     int    `json:"segment_count"`
	SilenceCount     int    `json:"silence_count"`
	DuplicatePackets int    `json:"duplicate_packets"`
	MissingPackets   int    `json:"missing_packets"`
	ManifestSHA256   string `json:"manifest_sha256,omitempty"`
}

// Checkpoint is written atomically after each chronological segment.
type Checkpoint struct {
	Version         int                `json:"version"`
	Input           InputDigest        `json:"input"`
	OutDir          string             `json:"out_dir"`
	Completed       []CompletedSegment `json:"completed_intervals"`
	ManifestSummary ManifestSummary    `json:"manifest_summary"`
}

// DigestFiles hashes named input paths and produces one combined digest.
func DigestFiles(paths map[string]string) (InputDigest, error) {
	id := InputDigest{Files: make(map[string]string, len(paths))}
	h := sha256.New()
	for role, path := range paths {
		sum, err := hashFile(path)
		if err != nil {
			return id, err
		}
		id.Files[role] = sum
		io.WriteString(h, role)
		io.WriteString(h, "\x00")
		io.WriteString(h, path)
		io.WriteString(h, "\x00")
		io.WriteString(h, sum)
		io.WriteString(h, "\n")
	}
	id.Digest = hex.EncodeToString(h.Sum(nil))
	return id, nil
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// SaveCheckpoint atomically writes cp to path.
func SaveCheckpoint(path string, cp Checkpoint) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fault.Wrap(fault.CheckpointCorrupt, err)
	}
	b, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return fault.Wrap(fault.CheckpointCorrupt, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fault.Wrap(fault.CheckpointCorrupt, err)
	}
	return nil
}

// LoadCheckpoint reads and validates a checkpoint. Missing files return a
// nil checkpoint and nil error so first runs are easy to handle.
func LoadCheckpoint(path string) (*Checkpoint, error) {
	if path == "" {
		return nil, nil
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fault.Wrap(fault.CheckpointCorrupt, err)
	}
	var cp Checkpoint
	if err := json.Unmarshal(b, &cp); err != nil {
		return nil, fault.Wrap(fault.CheckpointCorrupt, err)
	}
	if cp.Version != 1 {
		return nil, fault.New(fault.CheckpointCorrupt, "unsupported checkpoint version %d", cp.Version)
	}
	return &cp, nil
}

// MatchesInput reports whether the checkpoint was made for the same inputs.
func (cp *Checkpoint) MatchesInput(in InputDigest) bool {
	return cp != nil && cp.Input.Digest == in.Digest
}

// CompletedIndex reports whether index is already durably completed.
func (cp *Checkpoint) CompletedIndex(index int) (CompletedSegment, bool) {
	if cp == nil {
		return CompletedSegment{}, false
	}
	for _, c := range cp.Completed {
		if c.Index == index {
			return c, true
		}
	}
	return CompletedSegment{}, false
}
