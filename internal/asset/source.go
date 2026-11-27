// Package asset opens read-only source assets identified by their content
// digest. Sources are immutable: every read reports the byte offset it came
// from so that downstream stages can cite exact positions.
package asset

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"archivecheck/internal/fault"
)

// MaxAssetBytes is the hard read limit for a single source.
const MaxAssetBytes = 64 << 20

// Source is an immutable, digest-identified byte source. Name is the escaped,
// caller-supplied path; Prefix is the short digest used to namespace
// derivatives.
type Source struct {
	Name   string
	Digest string
	Size   int64
	data   []byte
}

// ReadAll opens path, hashing it while enforcing the per-asset limit. Path
// escapes outside root are rejected before any read happens. The returned
// Source owns a private copy of the bytes.
func ReadAll(root, path string, limit int64) (*Source, error) {
	rel, err := Resolve(root, path)
	if err != nil {
		return nil, err
	}
	full := filepath.Join(root, rel)
	fi, err := os.Stat(full)
	if err != nil {
		return nil, fault.Wrap(err, fault.StageSource, fault.KindMissing, rel, 0, "stat source")
	}
	if fi.IsDir() {
		return nil, fault.New(fault.StageSource, fault.KindInvalid, rel, 0, "source is a directory")
	}
	if fi.Size() > limit {
		return nil, fault.New(fault.StageSource, fault.KindLimit, rel, 0, "source is %d bytes, limit is %d", fi.Size(), limit)
	}
	f, err := os.Open(full)
	if err != nil {
		return nil, fault.Wrap(err, fault.StageSource, fault.KindMissing, rel, 0, "open source")
	}
	defer f.Close()

	h := sha256.New()
	var buf bytes.Buffer
	buf.Grow(int(fi.Size()))
	// The digest is computed over exactly the bytes retained, and a source
	// that changed size since Stat is read up to the limit only.
	n, err := io.Copy(io.MultiWriter(h, &buf), io.LimitReader(f, limit+1))
	if err != nil {
		return nil, fault.Wrap(err, fault.StageSource, fault.KindCorrupt, rel, n, "read source")
	}
	if n > limit {
		return nil, fault.New(fault.StageSource, fault.KindLimit, rel, limit, "source exceeds the %d byte limit", limit)
	}
	return &Source{Name: rel, Digest: hex.EncodeToString(h.Sum(nil)), Size: n, data: buf.Bytes()}, nil
}

// FromBytes wraps an in-memory asset (used by the demo and tests).
func FromBytes(name string, data []byte) *Source {
	sum := sha256.Sum256(data)
	cp := append([]byte(nil), data...)
	return &Source{Name: name, Digest: hex.EncodeToString(sum[:]), Size: int64(len(cp)), data: cp}
}

// Bytes returns the source content. Callers must not modify it.
func (s *Source) Bytes() []byte { return s.data }

// Short is the first 12 hex characters of the digest, used in file names.
func (s *Source) Short() string {
	if len(s.Digest) >= 12 {
		return s.Digest[:12]
	}
	return s.Digest
}

// Slice returns data[start:end] and an offset-carrying error when the request
// falls outside the source.
func (s *Source) Slice(start, end int64) ([]byte, error) {
	if start < 0 || end < start || end > s.Size {
		return nil, fault.New(fault.StageSource, fault.KindBoundary, s.Name, start, "slice %d..%d outside source of %d bytes", start, end, s.Size)
	}
	return s.data[start:end], nil
}

// Resolve validates that path stays inside root and returns a clean
// root-relative path. Absolute paths and any component that escapes root are
// rejected.
func Resolve(root, path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fault.New(fault.StageSource, fault.KindBoundary, path, 0, "empty path")
	}
	if filepath.IsAbs(path) {
		return "", fault.New(fault.StageSource, fault.KindBoundary, path, 0, "absolute paths are not accepted")
	}
	base, err := filepath.Abs(root)
	if err != nil {
		return "", fault.Wrap(err, fault.StageSource, fault.KindBoundary, root, 0, "resolve root")
	}
	// Evaluate symlinks in the root only; the candidate must exist to be
	// evaluated, which ReadAll checks afterwards.
	if real, err := filepath.EvalSymlinks(base); err == nil {
		base = real
	}
	joined := filepath.Join(base, filepath.FromSlash(path))
	rel, err := filepath.Rel(base, joined)
	if err != nil {
		return "", fault.Wrap(err, fault.StageSource, fault.KindBoundary, path, 0, "resolve path")
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fault.New(fault.StageSource, fault.KindBoundary, path, 0, "path escapes the input root")
	}
	// A lexically contained path can still escape through a symlink, so the
	// resolved target is checked as well. A path that does not exist yet is
	// fine here; ReadAll reports it as missing.
	real, err := filepath.EvalSymlinks(joined)
	if err == nil {
		if !Within(base, real) {
			return "", fault.New(fault.StageSource, fault.KindBoundary, path, 0, "path escapes the input root through a symlink")
		}
	} else if !os.IsNotExist(err) {
		return "", fault.Wrap(err, fault.StageSource, fault.KindBoundary, path, 0, "resolve path links")
	}
	return filepath.ToSlash(rel), nil
}

// Within reports whether p is root itself or a descendant of root.
func Within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel))
}

// Verify reports a digest-mismatch error when want differs from s.Digest.
func (s *Source) Verify(want string) error {
	if want != s.Digest {
		return fault.New(fault.StageSource, fault.KindDigest, s.Name, 0, "expected digest %s, got %s", want, s.Digest)
	}
	return nil
}

// WriteAtomic writes data to path via a temporary file in the same directory,
// fsyncing and atomically renaming it into place. It returns the SHA-256 of
// the written bytes. The destination directory must already exist.
func WriteAtomic(path string, data []byte) (string, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create output directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return "", fmt.Errorf("create temporary file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), bytes.NewReader(data)); err != nil {
		tmp.Close()
		cleanup()
		return "", fmt.Errorf("write temporary file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return "", fmt.Errorf("sync temporary file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return "", fmt.Errorf("close temporary file: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		cleanup()
		return "", fmt.Errorf("chmod temporary file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return "", fmt.Errorf("rename temporary file: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
