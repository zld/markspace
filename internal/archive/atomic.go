// Package archive stores verified assets under a local root, keyed by the
// SHA-256 of the source content. Writes are atomic and repeated deliveries of
// the same bytes reuse the existing result instead of appending duplicates.
package archive

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"

	"mediacore/internal/fault"
)

// Store is an on-disk archive rooted at Dir.
type Store struct {
	Dir string
}

// Open creates or opens an archive rooted at dir.
func Open(dir string) (*Store, error) {
	if dir == "" {
		return nil, fault.Newf(fault.CodeArchiveUnverified, "", "archive directory must not be empty")
	}
	if err := os.MkdirAll(filepath.Join(dir, "blobs"), 0o755); err != nil {
		return nil, fault.Newf(fault.CodeArchiveUnverified, "", "cannot create archive: %v", err)
	}
	return &Store{Dir: dir}, nil
}

// DigestFile computes the SHA-256 of a file without loading it fully.
func DigestFile(path string) (Digest, error) {
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

// Load reads the archive manifest. A missing manifest is an empty archive.
func (s *Store) Load() ([]Entry, error) {
	b, err := os.ReadFile(filepath.Join(s.Dir, manifestName))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fault.Newf(fault.CodeArchiveUnverified, "", "cannot read archive manifest: %v", err)
	}
	var entries []Entry
	if err := json.Unmarshal(b, &entries); err != nil {
		return nil, fault.Newf(fault.CodeArchiveHashMismatch, "", "archive manifest is corrupt: %v", err)
	}
	return entries, nil
}

// Find returns the entry for a digest.
func (s *Store) Find(digest Digest) (Entry, bool, error) {
	entries, err := s.Load()
	if err != nil {
		return Entry{}, false, err
	}
	for _, e := range entries {
		if e.Digest == digest {
			return e, true, nil
		}
	}
	return Entry{}, false, nil
}

// Put stores an asset idempotently. Repeating the same digest returns the
// existing entry unchanged; the blob is written atomically before the
// manifest is updated. A digest mismatch between the supplied bytes and the
// entry is rejected, so a corrupted input can never overwrite a good record.
func (s *Store) Put(entry Entry, data []byte) (Entry, bool, error) {
	actual := DigestOf(data)
	if entry.BlobDigest == "" {
		entry.BlobDigest = actual
	}
	if entry.BlobDigest != actual {
		return Entry{}, false, fault.Newf(fault.CodeArchiveHashMismatch, entry.Codec,
			"declared blob digest %s does not match content %s", entry.BlobDigest, actual)
	}
	if entry.Digest == "" {
		entry.Digest = actual
	}
	entries, err := s.Load()
	if err != nil {
		return Entry{}, false, err
	}
	for _, e := range entries {
		if e.Digest == entry.Digest {
			// The source fingerprint already has an archive. If the new
			// delivery carries different bytes, refuse rather than replace
			// the verified record.
			if e.BlobDigest != "" && e.BlobDigest != actual {
				return Entry{}, false, fault.Newf(fault.CodeArchiveHashMismatch, entry.Codec,
					"source digest %s already archived with different content", entry.Digest)
			}
			return e, true, nil
		}
	}
	entry.Bytes = int64(len(data))
	entry.Blob = filepath.Join("blobs", entry.Digest+".bin")
	blobPath := filepath.Join(s.Dir, entry.Blob)
	if err := writeFileAtomic(blobPath, data); err != nil {
		return Entry{}, false, fault.Newf(fault.CodeArchiveUnverified, entry.Codec, "cannot write blob: %v", err)
	}
	// Re-read the blob and verify it byte for byte before committing.
	got, err := os.ReadFile(blobPath)
	if err != nil || DigestOf(got) != entry.BlobDigest {
		return Entry{}, false, fault.Newf(fault.CodeArchiveHashMismatch, entry.Codec, "blob verification failed")
	}
	entries = append(entries, entry)
	sort.Slice(entries, func(i, j int) bool { return entries[i].Digest < entries[j].Digest })
	if err := s.writeManifest(entries); err != nil {
		return Entry{}, false, err
	}
	return entry, false, nil
}

// Commit publishes the archive only when the asset passed every stage. The
// caller must have verified scanning, recovery and quality; Verified is
// enforced here as a second line of defence.
func (s *Store) Commit(entry Entry, data []byte) (Entry, bool, error) {
	if !entry.Verified {
		return Entry{}, false, fault.Newf(fault.CodeArchiveUnverified, entry.Codec,
			"asset %s did not pass all validation stages", entry.Digest)
	}
	return s.Put(entry, data)
}

// BlobPath returns the absolute path of a stored blob.
func (s *Store) BlobPath(entry Entry) string { return filepath.Join(s.Dir, entry.Blob) }

func (s *Store) writeManifest(entries []Entry) error {
	b, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if err := writeFileAtomic(filepath.Join(s.Dir, manifestName), b); err != nil {
		return fault.Newf(fault.CodeArchiveUnverified, "", "cannot write archive manifest: %v", err)
	}
	return nil
}

// writeFileAtomic writes data to a temporary file in the same directory and
// renames it into place, so readers never observe a partial file.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
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
