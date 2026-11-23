// Package cache implements a content-addressed store for media segments. An
// entry is addressed by its SHA-256, but a hit is only accepted after the
// recorded origin node, input length and digest all verify. Any mismatch is
// quarantined as cache poisoning and the caller must rebuild from input.
package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"

	"mediamux/media"
)

// Provenance is the record written alongside cached bytes.
type Provenance struct {
	Digest      string `json:"digest"`
	Node        string `json:"node"`
	SegmentID   string `json:"segment_id"`
	SegmentIdx  int    `json:"segment_index"`
	InputLength int64  `json:"input_length"`
	ManifestVer string `json:"manifest_version"`
	Bytes       int64  `json:"bytes"`
	Epoch       int64  `json:"epoch"`
	MetaDigest  string `json:"meta_digest,omitempty"`
}

// Entry is a cached segment plus its provenance record.
type Entry struct {
	Key         string     `json:"key"`
	Data        []byte     `json:"-"`
	Meta        []byte     `json:"-"`
	Provenance  Provenance `json:"provenance"`
	Quarantined bool       `json:"quarantined"`
}

// LookupResult explains the outcome of a cache probe.
type LookupResult struct {
	Hit       bool       `json:"hit"`
	Source    string     `json:"source"` // "cache", "rebuilt", "quarantine"
	Reason    string     `json:"reason,omitempty"`
	Entry     *Entry     `json:"entry,omitempty"`
	Data      []byte     `json:"-"`
	Rejection *Rejection `json:"rejection,omitempty"`
}

// Rejection records why a cache hit was refused, for `cache_source_rejects`.
type Rejection struct {
	Key            string `json:"key"`
	ExpectedNode   string `json:"expected_node"`
	RecordedNode   string `json:"recorded_node"`
	ExpectedLength int64  `json:"expected_length"`
	RecordedLength int64  `json:"recorded_length"`
	DigestMatches  bool   `json:"digest_matches"`
	Reason         string `json:"reason"`
}

// Store is an in-memory CAS keyed by SHA-256. It never holds the same content
// twice and reports memory use so the pipeline can honour its buffer cap.
type Store struct {
	mu          sync.Mutex
	entries     map[string]*Entry
	quarantined map[string][]*Entry
	rejects     int
	hits        int
	misses      int
	rebuilt     int
	capacity    int64
	used        int64
}

// NewStore builds a store that refuses to hold more than capacityBytes.
func NewStore(capacityBytes int64) (*Store, error) {
	if capacityBytes <= 0 {
		return nil, media.NewError(media.CodeInput, media.PhaseCache, "cache capacity must be positive").AtOffset(0)
	}
	return &Store{
		entries:     make(map[string]*Entry),
		quarantined: make(map[string][]*Entry),
		capacity:    capacityBytes,
	}, nil
}

// Key computes the content address for data.
func Key(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Put stores data under its digest together with its provenance. Re-putting
// identical content with identical provenance is idempotent.
func (s *Store) Put(data []byte, prov Provenance) (*Entry, error) {
	return s.PutWithMeta(data, prov, nil)
}

// PutWithMeta stores data and a small analysis record under the data digest.
// The meta digest is recorded so a hit can prove the record was not swapped.
func (s *Store) PutWithMeta(data []byte, prov Provenance, meta []byte) (*Entry, error) {
	key := Key(data)
	if prov.Digest != "" && prov.Digest != key {
		return nil, media.NewError(media.CodeCachePoison, media.PhaseCache,
			"provenance digest does not match content").AtOffset(0).Because(fmt.Errorf("%s != %s", prov.Digest, key))
	}
	prov.Digest = key
	prov.Bytes = int64(len(data))
	if len(meta) > 0 {
		prov.MetaDigest = Key(meta)
	} else {
		prov.MetaDigest = ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.entries[key]; ok {
		if len(meta) > 0 && len(existing.Meta) == 0 {
			existing.Meta = append([]byte(nil), meta...)
			existing.Provenance.MetaDigest = prov.MetaDigest
		}
		return existing, nil
	}
	if s.used+int64(len(data)) > s.capacity {
		return nil, media.NewError(media.CodeInput, media.PhaseCache,
			"cache capacity exceeded").AtOffset(0)
	}
	e := &Entry{Key: key, Data: append([]byte(nil), data...), Meta: append([]byte(nil), meta...), Provenance: prov}
	s.entries[key] = e
	s.used += int64(len(data))
	return e, nil
}

// Lookup verifies a cache hit. The caller supplies the provenance it expects
// from the manifest and recovery cursor: node, input length and digest. A hit
// that fails any check is quarantined and reported, and the caller rebuilds.
func (s *Store) Lookup(key string, expect Provenance) LookupResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[key]
	if !ok {
		s.misses++
		return LookupResult{Hit: false, Source: "rebuilt", Reason: "cache miss"}
	}
	expectedDigest := expect.Digest
	if expectedDigest == "" {
		// The key is the digest, so a caller that only knows the address can
		// still require the stored content to hash back to it.
		expectedDigest = key
	}
	actualDigest := Key(e.Data)
	digestOK := actualDigest == expectedDigest && e.Provenance.Digest == expectedDigest
	metaOK := true
	if expect.MetaDigest != "" {
		metaOK = e.Provenance.MetaDigest == expect.MetaDigest && Key(e.Meta) == expect.MetaDigest
	}
	if e.Provenance.Node != expect.Node || e.Provenance.InputLength != expect.InputLength || !digestOK || !metaOK {
		s.rejects++
		s.rebuilt++
		delete(s.entries, key)
		s.used -= int64(len(e.Data))
		e.Quarantined = true
		s.quarantined[key] = append(s.quarantined[key], e)
		reason := "cache poisoned: "
		switch {
		case e.Provenance.Node != expect.Node:
			reason += fmt.Sprintf("source node %q != expected %q", e.Provenance.Node, expect.Node)
		case e.Provenance.InputLength != expect.InputLength:
			reason += fmt.Sprintf("input length %d != expected %d", e.Provenance.InputLength, expect.InputLength)
		case !metaOK:
			reason += "analysis record digest mismatch"
		default:
			reason += "digest mismatch"
		}
		return LookupResult{
			Source: "quarantine",
			Reason: reason,
			Rejection: &Rejection{
				Key: key, ExpectedNode: expect.Node, RecordedNode: e.Provenance.Node,
				ExpectedLength: expect.InputLength, RecordedLength: e.Provenance.InputLength,
				DigestMatches: digestOK && metaOK, Reason: reason,
			},
		}
	}
	s.hits++
	return LookupResult{Hit: true, Source: "cache", Entry: e, Data: e.Data}
}

// Rebuild stores freshly read bytes and records the rebuild event.
func (s *Store) Rebuild(data []byte, prov Provenance) (*Entry, error) {
	s.mu.Lock()
	s.rebuilt++
	s.mu.Unlock()
	return s.Put(data, prov)
}

// Fetch returns an entry by address without treating it as a cache hit for
// analysis. It is used to rehydrate committed checkpoint state.
func (s *Store) Fetch(key string) (*Entry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[key]
	return e, ok
}

// Quarantine removes an entry without deleting its evidence.
func (s *Store) Quarantine(key, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[key]
	if !ok {
		return
	}
	e.Quarantined = true
	delete(s.entries, key)
	s.used -= int64(len(e.Data))
	s.quarantined[key] = append(s.quarantined[key], e)
}

// Quarantined returns the quarantined entries for a key, oldest first.
func (s *Store) Quarantined(key string) []*Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Entry, len(s.quarantined[key]))
	copy(out, s.quarantined[key])
	return out
}

// Stats is the cache telemetry surfaced in the acceptance output.
type Stats struct {
	Entries       int   `json:"entries"`
	UsedBytes     int64 `json:"used_bytes"`
	Capacity      int64 `json:"capacity_bytes"`
	Hits          int   `json:"cache_hits"`
	Misses        int   `json:"cache_misses"`
	SourceRejects int   `json:"cache_source_rejects"`
	Rebuilt       int   `json:"cache_rebuilds"`
	Quarantined   int   `json:"cache_quarantined"`
}

// Stats snapshots telemetry.
func (s *Store) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	q := 0
	for _, list := range s.quarantined {
		q += len(list)
	}
	return Stats{
		Entries: len(s.entries), UsedBytes: s.used, Capacity: s.capacity,
		Hits: s.hits, Misses: s.misses, SourceRejects: s.rejects,
		Rebuilt: s.rebuilt, Quarantined: q,
	}
}

// WriteManifest persists provenance JSON to a stable file name derived from
// the key. It is a helper for the CLI's evidence dump.
func (p Provenance) WriteManifest(dir string) (string, error) {
	name := filepath.Join(dir, p.Digest+".json")
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return "", err
	}
	if err := writeFileAtomic(name, b); err != nil {
		return "", err
	}
	return name, nil
}
