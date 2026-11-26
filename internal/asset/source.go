// Package asset manages read-only source evidence addressed by SHA-256.
package asset

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"

	"mediacheck/internal/timebase"
)

// Source is an immutable, content-addressed original capture.
type Source struct {
	SHA256    string `json:"source_sha"`
	Size      int64  `json:"size"`
	Path      string `json:"path"`
	MediaType string `json:"media_type"`
}

// Open opens the read-only source bytes.
func (s *Source) Open() (io.ReadCloser, error) {
	f, err := os.Open(s.Path)
	if err != nil {
		return nil, timebase.NewError(timebase.CodeInvalidArgument, "open source").Wrap(err)
	}
	return f, nil
}

// Verify recomputes the digest and checks the stored size.
func (s *Source) Verify() error {
	f, err := s.Open()
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return timebase.NewError(timebase.CodeDigest, "verify source read failed").Wrap(err)
	}
	if n != s.Size {
		return timebase.At(timebase.CodeDigest, -1, "source size changed: stored=%d actual=%d", s.Size, n)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != s.SHA256 {
		return timebase.At(timebase.CodeDigest, -1, "source digest changed: stored=%s actual=%s", s.SHA256, got)
	}
	return nil
}

// Store is a local content-addressed object store. It has no network dependency.
type Store struct {
	Root string
}

func NewStore(root string) (*Store, error) {
	if root == "" {
		return nil, timebase.NewError(timebase.CodeInvalidArgument, "empty asset store root")
	}
	objDir := filepath.Join(root, "objects", "sha256")
	if err := os.MkdirAll(objDir, 0o755); err != nil {
		return nil, timebase.NewError(timebase.CodeCache, "create asset store").Wrap(err)
	}
	return &Store{Root: root}, nil
}

func (s *Store) objectPath(digest string) string {
	return filepath.Join(s.Root, "objects", "sha256", digest[:2], digest[2:])
}

// ImportFile hashes and atomically stores path without modifying the original.
func (s *Store) ImportFile(path, mediaType string) (*Source, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, timebase.At(timebase.CodeInvalidArgument, -1, "open import").Wrap(err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, timebase.NewError(timebase.CodeInvalidArgument, "stat import").Wrap(err)
	}
	return s.Import(f, st.Size(), mediaType)
}

// Import streams r into a temporary object while hashing it, then renames it
// to its digest address. Repeated imports share the same immutable object.
func (s *Store) Import(r io.Reader, size int64, mediaType string) (*Source, error) {
	if size < 0 {
		return nil, timebase.NewError(timebase.CodeInvalidArgument, "negative source size")
	}
	tmp, err := os.CreateTemp(s.Root, "upload-*.tmp")
	if err != nil {
		return nil, timebase.NewError(timebase.CodeCache, "create temporary source").Wrap(err)
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()

	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), r)
	if err != nil {
		_ = tmp.Close()
		return nil, timebase.NewError(timebase.CodeDigest, "hash source stream").Wrap(err)
	}
	if size >= 0 && n != size {
		_ = tmp.Close()
		return nil, timebase.At(timebase.CodeDigest, -1, "source length mismatch: expected=%d actual=%d", size, n)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return nil, timebase.NewError(timebase.CodeCache, "sync temporary source").Wrap(err)
	}
	if err := tmp.Chmod(0o444); err != nil {
		_ = tmp.Close()
		return nil, timebase.NewError(timebase.CodeCache, "make source read-only").Wrap(err)
	}
	if err := tmp.Close(); err != nil {
		return nil, timebase.NewError(timebase.CodeCache, "close temporary source").Wrap(err)
	}

	digest := hex.EncodeToString(h.Sum(nil))
	dst := s.objectPath(digest)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return nil, timebase.NewError(timebase.CodeCache, "create digest directory").Wrap(err)
	}
	if _, err := os.Stat(dst); errors.Is(err, os.ErrNotExist) {
		if err := os.Rename(tmpName, dst); err != nil {
			return nil, timebase.NewError(timebase.CodeCache, "atomically publish source").Wrap(err)
		}
		cleanup = false
	} else if err != nil {
		return nil, timebase.NewError(timebase.CodeCache, "stat immutable source").Wrap(err)
	}
	return &Source{SHA256: digest, Size: n, Path: dst, MediaType: mediaType}, nil
}

// DigestBytes returns the stable lowercase hex SHA-256 of b.
func DigestBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
