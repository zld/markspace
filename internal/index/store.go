// Package index manages the parse index that maps container structure
// onto cached content. It is invalidated independently of the content
// cache: a container change rebuilds the index while content stays hot,
// and a metadata-only update rebuilds manifest mappings the same way.
package index

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
)

// ErrIdentityMismatch means an index entry was looked up under a
// container/metadata identity that no longer matches the stored one.
var ErrIdentityMismatch = errors.New("index identity mismatch")

// Row is one index mapping: a sample identity tied to the content key
// whose bytes it references plus the manifest mapping metadata.
type Row struct {
	SampleIndex int
	ContentKey  string
	Offset      int64
	Length      int64
}

// Stats makes index lifecycle observable for the acceptance scenarios.
type Stats struct {
	Builds        int
	Invalidations int
	Lookups       int
}

// Store is an in-memory parse index. One store serves one track and is
// keyed by the container and metadata hashes from the cache package.
type Store struct {
	containerHash string
	metadataHash  string
	rows          []Row
	stats         Stats
}

// NewStore creates an empty store.
func NewStore() *Store { return &Store{} }

// Build (re)creates the index for a given identity and row set. Calling
// it with a new container or metadata hash is the explicit rebuild path:
// content caches are untouched, only mappings are replaced.
func (s *Store) Build(containerHash, metadataHash string, rows []Row) {
	if s.rows != nil {
		s.stats.Invalidations++
	}
	s.containerHash = containerHash
	s.metadataHash = metadataHash
	cp := make([]Row, len(rows))
	copy(cp, rows)
	s.rows = cp
	s.stats.Builds++
}

// Lookup returns the row for a sample. It fails when the store identity
// differs from the one the caller currently trusts.
func (s *Store) Lookup(containerHash, metadataHash string, sampleIndex int) (Row, error) {
	s.stats.Lookups++
	if containerHash != s.containerHash || metadataHash != s.metadataHash {
		return Row{}, ErrIdentityMismatch
	}
	for _, r := range s.rows {
		if r.SampleIndex == sampleIndex {
			return r, nil
		}
	}
	return Row{}, errors.New("sample index not found")
}

// Invalidate drops the index while leaving any content cache alone.
func (s *Store) Invalidate() {
	if s.rows != nil {
		s.stats.Invalidations++
	}
	s.rows = nil
	s.containerHash = ""
	s.metadataHash = ""
}

// Identity returns the hashes the current index was built for.
func (s *Store) Identity() (container, metadata string) {
	return s.containerHash, s.metadataHash
}

// Rows returns a defensive copy of the current mappings.
func (s *Store) Rows() []Row {
	out := make([]Row, len(s.rows))
	copy(out, s.rows)
	return out
}

// Stats returns a snapshot of the observable counters.
func (s *Store) Stats() Stats { return s.stats }

// Fingerprint is a convenience hash over an identity pair, used by the
// migration rollback checks to confirm the restored index matches.
func Fingerprint(containerHash, metadataHash string, rows []Row) string {
	h := sha256.New()
	h.Write([]byte(containerHash))
	h.Write([]byte{0})
	h.Write([]byte(metadataHash))
	for _, r := range rows {
		var buf [8]byte
		buf[0] = byte(r.SampleIndex)
		h.Write(buf[:])
		h.Write([]byte(r.ContentKey))
	}
	return "sha256:index:" + hex.EncodeToString(h.Sum(nil))
}
