package task

import (
	"audionorm/internal/media"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// StableKey derives the deterministic task identifier from the stream
// identity, the covered timeline range and the target spec. The same
// key with the same input resumes; the same key with a different input
// hash is reported as a conflict.
func StableKey(streamID string, r media.Range, spec Spec) string {
	h := sha256.New()
	fmt.Fprintf(h, "stream=%s\n", streamID)
	fmt.Fprintf(h, "range=%d:%d\n", r.Start, r.End)
	fmt.Fprintf(h, "rate=%d\n", spec.SampleRate)
	fmt.Fprintf(h, "channels=%d\n", spec.Channels)
	fmt.Fprintf(h, "rms=%.6f\n", spec.TargetRMS)
	fmt.Fprintf(h, "peak=%.6f\n", spec.PeakCeilDB)
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// HashBytes returns the lowercase hex SHA-256 of data.
func HashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// HashFile streams a file's SHA-256 without loading it into memory.
func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	return hashReader(f)
}

// AtomicWrite serializes value as canonical JSON to dir/file via a
// temp file + rename, so a crash never leaves a half-written artifact.
// The content hash is returned.
func AtomicWrite(dir, file string, value interface{}) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return "", err
	}
	data = append(data, '\n')
	final := filepath.Join(dir, file)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return "", err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return "", err
	}
	if err := os.Rename(tmpName, final); err != nil {
		os.Remove(tmpName)
		return "", err
	}
	return HashBytes(data), nil
}

// AtomicWriteBytes is the raw-bytes variant of AtomicWrite.
func AtomicWriteBytes(dir, file string, data []byte) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	final := filepath.Join(dir, file)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return "", err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return "", err
	}
	if err := os.Rename(tmpName, final); err != nil {
		os.Remove(tmpName)
		return "", err
	}
	return HashBytes(data), nil
}
