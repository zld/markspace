package cache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"mediamux/media"
)

// DiskStore is a content-addressed store that keeps entry bodies on disk and
// only digests and provenance in memory. It has the same verification rules
// as Store: a hit must prove source node, input length and digest.
type DiskStore struct {
	mu          sync.Mutex
	dir         string
	entries     map[string]*Entry
	quarantined map[string][]*Entry
	hits        int
	misses      int
	rejects     int
	rebuilt     int
	used        int64
	capacity    int64
}

// NewDiskStore roots a store at dir. Bodies live under dir/objects.
func NewDiskStore(dir string, capacityBytes int64) (*DiskStore, error) {
	if dir == "" {
		return nil, media.NewError(media.CodeInput, media.PhaseCache, "cache directory is required").AtOffset(0)
	}
	if capacityBytes <= 0 {
		return nil, media.NewError(media.CodeInput, media.PhaseCache, "cache capacity must be positive").AtOffset(0)
	}
	if err := os.MkdirAll(filepath.Join(dir, "objects"), 0o755); err != nil {
		return nil, media.NewError(media.CodeInternal, media.PhaseCache, "create cache directory").AtOffset(0).Because(err)
	}
	return &DiskStore{
		dir: dir, entries: make(map[string]*Entry), quarantined: make(map[string][]*Entry),
		capacity: capacityBytes,
	}, nil
}

func (s *DiskStore) metaPath(key string) string {
	return filepath.Join(s.dir, "objects", key[:2], key+".meta")
}

func (s *DiskStore) provPath(key string) string {
	return filepath.Join(s.dir, "objects", key[:2], key+".prov.json")
}

func (s *DiskStore) objectPath(key string) string {
	// Fan out by the first two hex characters to keep directories small.
	return filepath.Join(s.dir, "objects", key[:2], key)
}

// Put writes data to disk under its digest and records provenance.
func (s *DiskStore) Put(data []byte, prov Provenance) (*Entry, error) {
	return s.PutWithMeta(data, prov, nil)
}

// PutWithMeta also persists a small analysis record and binds it to the body
// through a recorded meta digest.
func (s *DiskStore) PutWithMeta(data []byte, prov Provenance, meta []byte) (*Entry, error) {
	key := Key(data)
	if prov.Digest != "" && prov.Digest != key {
		return nil, media.NewError(media.CodeCachePoison, media.PhaseCache,
			"provenance digest does not match content").AtOffset(0)
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
			if err := writeFileAtomic(s.metaPath(key), meta); err != nil {
				return nil, err
			}
			existing.Meta = append([]byte(nil), meta...)
			existing.Provenance.MetaDigest = prov.MetaDigest
		}
		return existing, nil
	}
	if s.used+int64(len(data)) > s.capacity {
		return nil, media.NewError(media.CodeInput, media.PhaseCache,
			"disk cache capacity exceeded").AtOffset(0)
	}
	path := s.objectPath(key)
	if err := writeFileAtomic(path, data); err != nil {
		return nil, err
	}
	if len(meta) > 0 {
		if err := writeFileAtomic(s.metaPath(key), meta); err != nil {
			return nil, err
		}
	}
	provJSON, err := json.Marshal(prov)
	if err != nil {
		return nil, media.NewError(media.CodeInternal, media.PhaseCache, "encode provenance").AtOffset(0).Because(err)
	}
	if err := writeFileAtomic(s.provPath(key), provJSON); err != nil {
		return nil, err
	}
	e := &Entry{Key: key, Meta: append([]byte(nil), meta...), Provenance: prov}
	e.Data = nil // bodies stay on disk; memory holds metadata only
	s.entries[key] = e
	s.used += int64(len(data))
	return e, nil
}

// Lookup reads the stored body and verifies its digest plus the expected
// provenance. A mismatch quarantines the entry and asks the caller to rebuild.
func (s *DiskStore) Lookup(key string, expect Provenance) LookupResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[key]
	if !ok {
		// A fresh process has no in-memory index; rehydrate it from the
		// provenance record written alongside the body.
		loaded, err := s.loadEntry(key)
		if err != nil {
			s.misses++
			return LookupResult{Hit: false, Source: "rebuilt", Reason: "cache miss"}
		}
		e = loaded
		s.entries[key] = e
		s.used += e.Provenance.Bytes
	}
	body, err := os.ReadFile(s.objectPath(key))
	if err != nil {
		s.misses++
		return LookupResult{Hit: false, Source: "rebuilt", Reason: "cache body missing"}
	}
	s.used -= int64(len(body))
	expectedDigest := expect.Digest
	if expectedDigest == "" {
		expectedDigest = key
	}
	actualDigest := Key(body)
	digestOK := actualDigest == expectedDigest && e.Provenance.Digest == expectedDigest
	meta, _ := os.ReadFile(s.metaPath(key))
	metaOK := true
	if expect.MetaDigest != "" {
		metaOK = e.Provenance.MetaDigest == expect.MetaDigest && Key(meta) == expect.MetaDigest
	}
	if e.Provenance.Node != expect.Node || e.Provenance.InputLength != expect.InputLength || !digestOK || !metaOK {
		s.rejects++
		s.rebuilt++
		delete(s.entries, key)
		_ = os.Remove(s.objectPath(key))
		_ = os.Remove(s.metaPath(key))
		_ = os.Remove(s.provPath(key))
		q := *e
		q.Quarantined = true
		s.quarantined[key] = append(s.quarantined[key], &q)
		reason := "cache poisoned: "
		switch {
		case e.Provenance.Node != expect.Node:
			reason += "source node " + e.Provenance.Node + " != expected " + expect.Node
		case e.Provenance.InputLength != expect.InputLength:
			reason += "input length mismatch"
		case !metaOK:
			reason += "analysis record digest mismatch"
		default:
			reason += "digest mismatch"
		}
		return LookupResult{
			Source: "quarantine", Reason: reason,
			Rejection: &Rejection{
				Key: key, ExpectedNode: expect.Node, RecordedNode: e.Provenance.Node,
				ExpectedLength: expect.InputLength, RecordedLength: e.Provenance.InputLength,
				DigestMatches: digestOK, Reason: reason,
			},
		}
	}
	s.hits++
	s.used += int64(len(body))
	e.Meta = meta
	return LookupResult{Hit: true, Source: "cache", Entry: e, Data: body}
}

// loadEntry rehydrates an entry's provenance and meta record from disk.
func (s *DiskStore) loadEntry(key string) (*Entry, error) {
	b, err := os.ReadFile(s.provPath(key))
	if err != nil {
		return nil, err
	}
	var prov Provenance
	if err := json.Unmarshal(b, &prov); err != nil {
		return nil, err
	}
	if prov.Digest != key {
		return nil, media.NewError(media.CodeCachePoison, media.PhaseCache,
			"recorded provenance digest does not match its address").AtOffset(0)
	}
	meta, _ := os.ReadFile(s.metaPath(key))
	return &Entry{Key: key, Meta: meta, Provenance: prov}, nil
}

// Fetch rehydrates an entry by address for checkpoint recovery. It does not
// count as a cache hit and performs no provenance comparison.
func (s *DiskStore) Fetch(key string) (*Entry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.entries[key]; ok {
		return e, true
	}
	e, err := s.loadEntry(key)
	if err != nil {
		return nil, false
	}
	s.entries[key] = e
	s.used += e.Provenance.Bytes
	return e, true
}

// Rebuild stores fresh bytes and records the event.
func (s *DiskStore) Rebuild(data []byte, prov Provenance) (*Entry, error) {
	s.mu.Lock()
	s.rebuilt++
	s.mu.Unlock()
	return s.Put(data, prov)
}

// Stats snapshots telemetry.
func (s *DiskStore) Stats() Stats {
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

// Quarantined returns the quarantined records for key.
func (s *DiskStore) Quarantined(key string) []*Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Entry, len(s.quarantined[key]))
	copy(out, s.quarantined[key])
	return out
}
