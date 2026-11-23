package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"mediadeliver/internal/align"
	"mediadeliver/internal/model"
	"mediadeliver/internal/segment"
)

// cursorVersion is bumped whenever the on-disk resume format changes; an
// old cursor is ignored rather than misread.
const cursorVersion = 1

// InputEntry identifies one input file and its content hash.
type InputEntry struct {
	Role           string `json:"role"`
	Representation string `json:"representation,omitempty"`
	Path           string `json:"path"` // relative to the work directory
	SHA256         string `json:"sha256"`
	Size           int64  `json:"size"`
}

// Checkpoint is the persisted stage cursor plus the expensive scan result.
// Align can be re-run from it without touching the media files again.
type Checkpoint struct {
	Version     int                   `json:"version"`
	InputDigest string                `json:"input_digest"`
	Stage       string                `json:"stage"`
	Scans       map[string]ScanRecord `json:"scans"`
}

// ScanRecord is the serialisable form of one representation scan.
type ScanRecord struct {
	Path        string             `json:"path"`
	SHA256      string             `json:"sha256"`
	Size        int64              `json:"size"`
	ScannedTo   int64              `json:"scanned_to"`
	IndexFound  bool               `json:"index_found"`
	SampleCount int64              `json:"sample_count"`
	Segments    []segment.Boundary `json:"segments"`
}

// Outcome is returned by every Execute call.
type Outcome struct {
	Manifest        *align.Manifest
	Diagnostics     []*model.Error
	Stage           string
	SegmentsCounted int
	Resumed         bool
	Published       bool
	ManifestPath    string
	ManifestSHA     string
	ResumeMillis    int64
}

func checkpointPath(work string) string { return filepath.Join(work, "resume.json") }

// inputDigest is the order-independent SHA-256 over all input identities.
func inputDigest(entries []InputEntry) string {
	sorted := append([]InputEntry(nil), entries...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Role != sorted[j].Role {
			return sorted[i].Role < sorted[j].Role
		}
		if sorted[i].Representation != sorted[j].Representation {
			return sorted[i].Representation < sorted[j].Representation
		}
		return sorted[i].Path < sorted[j].Path
	})
	h := sha256.New()
	for _, e := range sorted {
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\x00%d\n", e.Role, e.Representation, e.Path, e.SHA256, e.Size)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// hashFile streams a file through SHA-256 with a fixed buffer.
func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	buf := make([]byte, 64*1024)
	n, err := io.CopyBuffer(h, f, buf)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func loadCheckpoint(work string) (*Checkpoint, error) {
	data, err := os.ReadFile(checkpointPath(work))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var cp Checkpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return nil, nil // corrupt cursor: start over deterministically
	}
	if cp.Version != cursorVersion {
		return nil, nil
	}
	return &cp, nil
}

// writeAtomic publishes data with a temporary file and a rename in the same
// directory, so readers never observe a partial file.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func saveCheckpoint(work string, cp *Checkpoint) error {
	cp.Version = cursorVersion
	data, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writeAtomic(checkpointPath(work), data)
}
