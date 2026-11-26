// Package source ingests capture assets into an immutable content-addressed
// store and exposes bounded, streaming readers to the scanners.
package source

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// readBufSize bounds the in-memory buffer used while hashing and copying.
const readBufSize = 64 * 1024

// maxIngestAttempts bounds retries when a source is replaced mid-read.
const maxIngestAttempts = 3

// ErrSourceChanged means the file at a path changed while it was being read.
// Callers must record a SOURCE_CHANGED anomaly and retry instead of publishing
// results derived from a half-read version.
var ErrSourceChanged = errors.New("source: file replaced while reading")

// Store is the immutable, content-addressed source store. Blobs are named by
// their full-file SHA-256, so replacing a file at the same path always yields
// a new blob and forces a fresh hash computation.
type Store struct {
	root string
}

// NewStore creates (or opens) a store rooted at root/objects.
func NewStore(root string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(root, "objects"), 0o755); err != nil {
		return nil, fmt.Errorf("source: create store: %w", err)
	}
	return &Store{root: root}, nil
}

// Asset is one ingested source. BlobPath points at the immutable copy; all
// downstream reads must use it rather than the original mutable path.
type Asset struct {
	Path     string `json:"path"`
	Hash     string `json:"hash"`
	Size     int64  `json:"size"`
	BlobPath string `json:"blob_path"`
}

// blobPath maps a hash to objects/xx/<full hash>, keeping directories small.
func (s *Store) blobPath(hash string) string {
	return filepath.Join(s.root, "objects", hash[:2], hash)
}

// Ingest streams the file at path, computing its full SHA-256 and writing an
// immutable copy into the store. If the file is replaced during the read the
// attempt is aborted with ErrSourceChanged and automatically retried; stale
// partial results are never published.
func (s *Store) Ingest(ctx context.Context, path string) (*Asset, error) {
	var lastErr error
	for attempt := 0; attempt < maxIngestAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		a, err := s.ingestOnce(path)
		if err == nil {
			return a, nil
		}
		if !errors.Is(err, ErrSourceChanged) {
			return nil, err
		}
		lastErr = err
	}
	return nil, fmt.Errorf("source: ingest gave up after %d attempts: %w", maxIngestAttempts, lastErr)
}

// fingerprint captures the identity a replacement would change.
type fingerprint struct {
	size int64
	mode os.FileMode
	msec int64 // modification time, nanoseconds
	ino  uint64
}

func statFP(path string) (fingerprint, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return fingerprint{}, err
	}
	st := statIno(fi)
	return fingerprint{size: fi.Size(), mode: fi.Mode(), msec: fi.ModTime().UnixNano(), ino: st}, nil
}

func (s *Store) ingestOnce(path string) (*Asset, error) {
	before, err := statFP(path)
	if err != nil {
		return nil, fmt.Errorf("source: stat %s: %w", path, err)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("source: open %s: %w", path, err)
	}
	defer f.Close()

	h := sha256.New()
	tmp, err := os.CreateTemp(s.root, "ingest-*.tmp")
	if err != nil {
		return nil, fmt.Errorf("source: temp blob: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	buf := make([]byte, readBufSize)
	var total int64
	for {
		n, rerr := f.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
			if _, werr := tmp.Write(buf[:n]); werr != nil {
				tmp.Close()
				return nil, fmt.Errorf("source: write blob: %w", werr)
			}
			total += int64(n)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			tmp.Close()
			return nil, fmt.Errorf("source: read %s: %w", path, rerr)
		}
		// Re-check identity every chunk: a replacement invalidates this read.
		if cur, ferr := statFP(path); ferr != nil || cur != before {
			tmp.Close()
			if ferr == nil {
				return nil, ErrSourceChanged
			}
			return nil, fmt.Errorf("source: re-stat %s: %w", path, ferr)
		}
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("source: close blob: %w", err)
	}
	after, err := statFP(path)
	if err != nil || after != before {
		if err == nil {
			return nil, ErrSourceChanged
		}
		return nil, fmt.Errorf("source: re-stat %s: %w", path, err)
	}
	if total != before.size {
		return nil, fmt.Errorf("source: short read of %s: %d/%d bytes", path, total, before.size)
	}

	hash := hex.EncodeToString(h.Sum(nil))
	dst := s.blobPath(hash)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return nil, err
	}
	// Link over rename when possible: same content never gets duplicated.
	if err := os.Link(tmpName, dst); err != nil {
		if errors.Is(err, os.ErrExist) {
			return s.existing(path, hash, total)
		}
		if err := os.Rename(tmpName, dst); err != nil {
			return nil, fmt.Errorf("source: finalize blob: %w", err)
		}
	}
	return &Asset{Path: path, Hash: hash, Size: total, BlobPath: dst}, nil
}

func (s *Store) existing(path, hash string, size int64) (*Asset, error) {
	dst := s.blobPath(hash)
	if err := VerifyBlob(dst, hash); err != nil {
		return nil, err
	}
	return &Asset{Path: path, Hash: hash, Size: size, BlobPath: dst}, nil
}

// Open opens the immutable blob for an asset.
func (s *Store) Open(a *Asset) (*os.File, error) {
	f, err := os.Open(a.BlobPath)
	if err != nil {
		return nil, fmt.Errorf("source: open blob %s: %w", a.Hash, err)
	}
	return f, nil
}

// VerifyBlob re-hashes a stored blob and compares against the expected hash.
func VerifyBlob(blobPath, expectedHash string) error {
	f, err := os.Open(blobPath)
	if err != nil {
		return fmt.Errorf("source: verify open: %w", err)
	}
	defer f.Close()
	h := sha256.New()
	buf := make([]byte, readBufSize)
	if _, err := io.CopyBuffer(h, f, buf); err != nil {
		return fmt.Errorf("source: verify read: %w", err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != expectedHash {
		return fmt.Errorf("source: blob checksum mismatch: got %s want %s", got, expectedHash)
	}
	return nil
}

// HashFile streams a SHA-256 over any file (used for metadata sidecars and
// derived segments). It never caches by path: callers get a fresh hash.
func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("source: hash open %s: %w", path, err)
	}
	defer f.Close()
	h := sha256.New()
	buf := make([]byte, readBufSize)
	if _, err := io.CopyBuffer(h, f, buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
