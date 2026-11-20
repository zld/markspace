// Package export re-reads selected samples and writes a JSONL manifest
// with hashes and Base64 sample data, via a temp file plus rename.
package export

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"mp4seek/internal/avc"
	"mp4seek/internal/bmff"
	"mp4seek/internal/report"
)

// Record is one JSONL manifest line.
type Record struct {
	Sample     int    `json:"sample"`
	Offset     int64  `json:"offset"`
	Bytes      uint32 `json:"bytes"`
	PTSTicks   int64  `json:"pts_ticks"`
	MoviePTSMs int64  `json:"movie_pts_ms"`
	Type       string `json:"type"`
	SHA256     string `json:"sha256"`
	DataB64    string `json:"data_b64"`
}

// Input is one sample selected for export.
type Input struct {
	Index      int
	Offset     int64
	Size       uint32
	PTSTicks   int64 // media PTS
	MoviePTSMs int64
}

// ReadSample loads, hashes, and classifies one sample.
func ReadSample(r *bmff.Reader, in Input, lengthSize int) (Record, error) {
	data, err := r.ReadSample(in.Offset, in.Size)
	if err != nil {
		return Record{}, err
	}
	sum := sha256.Sum256(data)
	cls, err := avc.Classify(data, lengthSize)
	if err != nil {
		return Record{}, fmt.Errorf("sample %d: %w", in.Index, err)
	}
	return Record{
		Sample:     in.Index,
		Offset:     in.Offset,
		Bytes:      in.Size,
		PTSTicks:   in.PTSTicks,
		MoviePTSMs: in.MoviePTSMs,
		Type:       string(cls.Type),
		SHA256:     hex.EncodeToString(sum[:]),
		DataB64:    base64.StdEncoding.EncodeToString(data),
	}, nil
}

// HashOf reads one sample and returns its hex SHA-256.
func HashOf(r *bmff.Reader, off int64, size uint32) (string, error) {
	data, err := r.ReadSample(off, size)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// WriteManifest atomically writes records as JSONL to outPath.
func WriteManifest(outPath string, records []Record) error {
	dir := filepath.Dir(outPath)
	tmp, err := os.CreateTemp(dir, ".mp4seek-export-*")
	if err != nil {
		return fmt.Errorf("export: temp file: %w", err)
	}
	tmpName := tmp.Name()
	enc := report.NewJSONL(tmp)
	for _, rec := range records {
		if err := enc.Emit(rec); err != nil {
			tmp.Close()
			os.Remove(tmpName)
			return fmt.Errorf("export: write: %w", err)
		}
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("export: close: %w", err)
	}
	if err := os.Rename(tmpName, outPath); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("export: rename: %w", err)
	}
	return nil
}
