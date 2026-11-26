// Package store implements an immutable, content-addressed object store plus an
// append-only journal of checkpoints. Objects are written once under their
// SHA-256 digest; the journal is the recovery log.
package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"example.com/mediavault/internal/fault"
	"example.com/mediavault/internal/fingerprint"
)

// Store is a rooted content-addressed blob store.
type Store struct {
	root    string
	objects string
}

// Open creates the store layout under root.
func Open(root string) (*Store, error) {
	s := &Store{root: root, objects: filepath.Join(root, "objects")}
	if err := os.MkdirAll(s.objects, 0o755); err != nil {
		return nil, fault.Wrap(fault.CodeIO, fault.StageReceived, -1, err, "create object store %s", s.objects)
	}
	return s, nil
}

// Root returns the store root directory.
func (s *Store) Root() string { return s.root }

// Path returns the deterministic location of an object digest.
func (s *Store) Path(digest string) (string, error) {
	if !fingerprint.ValidDigest(digest) {
		return "", fault.New(fault.CodeDigest, fault.StageFingerprinted, -1, "invalid object digest %q", digest)
	}
	return filepath.Join(s.objects, digest[:2], digest[2:]), nil
}

// Exists reports whether an object is already stored.
func (s *Store) Exists(digest string) bool {
	p, err := s.Path(digest)
	if err != nil {
		return false
	}
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

// PutBytes writes immutable bytes under digest, verifying the digest matches.
func (s *Store) PutBytes(digest string, p []byte) error {
	sum := sha256.Sum256(p)
	if got := hex.EncodeToString(sum[:]); got != digest {
		return fault.New(fault.CodeDigest, fault.StageFingerprinted, -1,
			"content hash %s does not match expected %s", got, digest)
	}
	return s.PutReader(digest, bytes.NewReader(p), int64(len(p)))
}

// PutReader streams r into the object location. If the object already exists
// the write is skipped after confirming the stored size.
func (s *Store) PutReader(digest string, r io.Reader, size int64) error {
	return s.put(digest, r, size, false)
}

// RepairBytes overwrites an existing object with verified bytes. It is used
// only when recovery has re-derived a block from an intact source and found
// the stored copy corrupt; immutable objects are otherwise never replaced.
func (s *Store) RepairBytes(digest string, p []byte) error {
	sum := sha256.Sum256(p)
	if got := hex.EncodeToString(sum[:]); got != digest {
		return fault.New(fault.CodeDigest, fault.StageNormalized, -1,
			"repair content hashes to %s, expected %s", got, digest)
	}
	return s.put(digest, bytes.NewReader(p), int64(len(p)), true)
}

func (s *Store) put(digest string, r io.Reader, size int64, replace bool) error {
	path, err := s.Path(digest)
	if err != nil {
		return err
	}
	if !replace && s.Exists(digest) {
		st, err := os.Stat(path)
		if err != nil {
			return fault.Wrap(fault.CodeIO, fault.StageNormalized, -1, err, "stat object %s", digest)
		}
		if st.Size() == size {
			return nil
		}
		return fault.New(fault.CodeDigest, fault.StageNormalized, -1,
			"existing object %s is %d bytes, expected %d", digest, st.Size(), size)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fault.Wrap(fault.CodeIO, fault.StageNormalized, -1, err, "create object dir")
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".incoming-*")
	if err != nil {
		return fault.Wrap(fault.CodeIO, fault.StageNormalized, -1, err, "create temp object")
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := io.Copy(tmp, r); err != nil {
		tmp.Close()
		return fault.Wrap(fault.CodeIO, fault.StageNormalized, -1, err, "write object %s", digest)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fault.Wrap(fault.CodeIO, fault.StageNormalized, -1, err, "sync object %s", digest)
	}
	if err := tmp.Close(); err != nil {
		return fault.Wrap(fault.CodeIO, fault.StageNormalized, -1, err, "close object %s", digest)
	}
	if st, err := os.Stat(tmpName); err == nil && st.Size() != size {
		return fault.New(fault.CodeDigest, fault.StageNormalized, -1,
			"streamed %d bytes for %s, expected %d", st.Size(), digest, size)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fault.Wrap(fault.CodeIO, fault.StageNormalized, -1, err, "commit object %s", digest)
	}
	return nil
}

// OpenObject opens a stored object read-only.
func (s *Store) OpenObject(digest string) (*os.File, error) {
	path, err := s.Path(digest)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fault.Wrap(fault.CodeIO, fault.StageArchived, -1, err, "open object %s", digest)
	}
	return f, nil
}

// ReadObject returns an object's bytes, verifying its digest.
func (s *Store) ReadObject(digest string) ([]byte, error) {
	f, err := s.OpenObject(digest)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, 0, 1<<20)
	chunk := make([]byte, 64<<10)
	for {
		n, err := f.Read(chunk)
		buf = append(buf, chunk[:n]...)
		if len(buf) > 4<<20 {
			return nil, fault.New(fault.CodeLimit, fault.StageArchived, -1, "object %s exceeds 4 MiB", digest)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fault.Wrap(fault.CodeIO, fault.StageArchived, -1, err, "read object %s", digest)
		}
	}
	sum := sha256.Sum256(buf)
	if got := hex.EncodeToString(sum[:]); got != digest {
		return nil, fault.New(fault.CodeDigest, fault.StageArchived, -1,
			"stored object %s hashes to %s", digest, got)
	}
	return buf, nil
}

// VerifyObject re-reads and re-hashes an object and checks its expected size.
func (s *Store) VerifyObject(digest string, size int64) error {
	f, err := s.OpenObject(digest)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return fault.Wrap(fault.CodeIO, fault.StageArchived, -1, err, "verify object %s", digest)
	}
	if size >= 0 && n != size {
		return fault.New(fault.CodeDigest, fault.StageArchived, -1,
			"object %s is %d bytes, checkpoint says %d", digest, n, size)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != digest {
		return fault.New(fault.CodeDigest, fault.StageArchived, -1,
			"object %s hashes to %s", digest, got)
	}
	return nil
}

// PutFile copies a source file into the store under its full-file digest.
func (s *Store) PutFile(digest, path string, size int64) error {
	f, err := os.Open(path)
	if err != nil {
		return fault.Wrap(fault.CodeIO, fault.StageReceived, -1, err, "open source %s", path)
	}
	defer f.Close()
	if err := s.PutReader(digest, f, size); err != nil {
		return err
	}
	return s.VerifyObject(digest, size)
}

// WriteFileAtomic writes a small metadata file via rename.
func (s *Store) WriteFileAtomic(name string, data []byte) error {
	path := filepath.Join(s.root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fault.Wrap(fault.CodeIO, fault.StageArchived, -1, err, "create dir for %s", name)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return fault.Wrap(fault.CodeIO, fault.StageArchived, -1, err, "create temp for %s", name)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fault.Wrap(fault.CodeIO, fault.StageArchived, -1, err, "write %s", name)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fault.Wrap(fault.CodeIO, fault.StageArchived, -1, err, "sync %s", name)
	}
	if err := tmp.Close(); err != nil {
		return fault.Wrap(fault.CodeIO, fault.StageArchived, -1, err, "close %s", name)
	}
	return os.Rename(tmpName, path)
}

// ReadFile reads a metadata file from the store root.
func (s *Store) ReadFile(name string) ([]byte, error) {
	b, err := os.ReadFile(filepath.Join(s.root, name))
	if err != nil {
		return nil, fault.Wrap(fault.CodeIO, fault.StageArchived, -1, err, "read %s", name)
	}
	return b, nil
}

// String renders a short store description for terminal output.
func (s *Store) String() string { return fmt.Sprintf("store(%s)", s.root) }
