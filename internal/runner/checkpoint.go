// Package runner executes a planned manifest against the indexed inputs with
// bounded memory, per-segment atomic output and resumable checkpoints. A
// checkpoint is only trusted after its own checksum and every completed
// segment output verify; otherwise execution rolls back to the last safe
// boundary.
package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"

	"mediapack/internal/model"
)

// CheckpointVersion is bumped when the on-disk checkpoint schema changes.
const CheckpointVersion = "cp-2"

// SegmentResult records one completed output segment and its digest.
type SegmentResult struct {
	Index   int    `json:"index"`
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256"`
	StartNS int64  `json:"start_ns"`
	EndNS   int64  `json:"end_ns"`
}

// Checkpoint is the resumable execution state.
type Checkpoint struct {
	Version        string          `json:"version"`
	PlanHash       string          `json:"plan_hash"`
	InputsDigest   string          `json:"inputs_digest"`
	Completed      []SegmentResult `json:"completed"`
	NextSegment    int             `json:"next_segment"`
	OutputSHA256   string          `json:"output_sha256,omitempty"`
	ManifestSHA256 string          `json:"manifest_sha256,omitempty"`
}

func checkpointPath(dir string) string { return filepath.Join(dir, "checkpoint.json") }
func checksumSibling(p string) string  { return p + ".sha256" }

// SaveCheckpoint writes the checkpoint to a temp file, checksumms it, writes a
// sibling digest and atomically renames both into place.
func SaveCheckpoint(dir string, cp *Checkpoint) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return err
	}
	final := checkpointPath(dir)
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	sumTmp := checksumSibling(tmp)
	if err := os.WriteFile(sumTmp, []byte(hex.EncodeToString(sum[:])+"\n"), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, final); err != nil {
		return err
	}
	if err := os.Rename(sumTmp, checksumSibling(final)); err != nil {
		return err
	}
	return nil
}

// LoadCheckpoint reads and validates a checkpoint. A missing checkpoint yields
// (nil, nil). A corrupt or tampered checkpoint yields a structured error so
// callers can roll back rather than silently restart.
func LoadCheckpoint(dir string) (*Checkpoint, error) {
	final := checkpointPath(dir)
	raw, err := os.ReadFile(final)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	wantSum, err := os.ReadFile(checksumSibling(final))
	if err != nil {
		return nil, cpError(final, "missing checkpoint checksum")
	}
	got := sha256.Sum256(raw)
	if hex.EncodeToString(got[:]) != trimSpace(string(wantSum)) {
		return nil, cpError(final, "checkpoint checksum mismatch")
	}
	var cp Checkpoint
	if err := json.Unmarshal(raw, &cp); err != nil {
		return nil, cpError(final, "checkpoint is not valid JSON: "+err.Error())
	}
	if cp.Version != CheckpointVersion {
		return nil, cpError(final, "checkpoint version mismatch: "+cp.Version)
	}
	return &cp, nil
}

// VerifyCompleted re-hashes every completed segment output. The first mismatch
// is returned as a structured checkpoint error identifying the bad segment.
func (cp *Checkpoint) VerifyCompleted() error {
	for _, seg := range cp.Completed {
		sum, size, err := hashFileBounded(seg.Path)
		if err != nil {
			return cpError(seg.Path, "segment output unreadable: "+err.Error())
		}
		if sum != seg.SHA256 || size != seg.Size {
			return cpError(seg.Path, "segment output checksum/size mismatch")
		}
	}
	return nil
}

// SafeBoundary returns the number of verified-completed segments. When the
// checkpoint cannot be fully trusted, callers truncate outputs beyond this
// index and resume there.
func (cp *Checkpoint) SafeBoundary() int {
	if cp == nil {
		return 0
	}
	return len(cp.Completed)
}

func cpError(path, msg string) *model.PipelineError {
	return &model.PipelineError{
		Stage: model.StageRunner, Code: model.CodeCheckpointBad, Input: path,
		ByteRange: model.ByteRange{Offset: 0}, Message: msg,
	}
}

func trimSpace(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == ' ' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

// hashFileBounded streams a file through SHA256 with a fixed buffer, returning
// the hex digest and size without ever holding the whole file in memory.
func hashFileBounded(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	buf := make([]byte, 64*1024)
	size, err := io.CopyBuffer(h, f, buf)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), size, nil
}
