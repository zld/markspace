// Package index persists scan results bound to the input's SHA-256. The
// index is a cache only: when it is missing, incomplete, or the input digest
// changed, callers fall back to a fresh scan and rebuild it.
package index

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"archive/internal/model"
	"archive/internal/scan"
)

// FileName is the index file name inside a working directory.
const FileName = "index.json"

// Index binds a scan result to the exact input it was derived from.
type Index struct {
	InputSHA256 string       `json:"inputSHA256"`
	InputSize   int64        `json:"inputSize"`
	Scan        *scan.Result `json:"scan"`
}

// HashFile streams the file through SHA-256 (bounded memory).
func HashFile(path string) (string, error) {
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

// Load reads the index from dir and validates it against the input digest.
// The returned error is a model.ErrCode-tagged message the caller can
// report; a nil *Index always means "rescan".
func Load(dir, inputPath, digest string) (*Index, error) {
	p := filepath.Join(dir, FileName)
	data, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%s: no index at %s", model.ErrIndexMissing, p)
	}
	if err != nil {
		return nil, err
	}
	var idx Index
	if err := json.Unmarshal(data, &idx); err != nil || idx.Scan == nil || idx.InputSHA256 == "" {
		return nil, fmt.Errorf("%s: index file damaged, will rescan", model.ErrIndexIncomplete)
	}
	if idx.InputSHA256 != digest {
		return nil, fmt.Errorf("%s: input digest changed (%s -> %s), will rescan",
			model.ErrIndexStale, idx.InputSHA256[:12], digest[:12])
	}
	return &idx, nil
}

// Save writes the index atomically (tmp file + rename).
func Save(dir string, idx *Index) error {
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, FileName+".tmp")
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, FileName))
}
