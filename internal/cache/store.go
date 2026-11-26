// Package cache stores stage artifacts keyed by source and configuration
// digests. A hit is never trusted on key match alone: source digest,
// artifact digest and declared length are re-verified before reuse, and
// any mismatch invalidates the entry so the pipeline rebuilds it.
package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Entry is the on-disk verification record accompanying one artifact.
type Entry struct {
	Key         string `json:"key"`
	SourceSHA   string `json:"source_sha256"`
	ConfigSHA   string `json:"config_sha256"`
	ArtifactSHA string `json:"artifact_sha256"`
	Size        int64  `json:"size"`
	MediaType   string `json:"media_type"`
}

// HitOutcome reports why a cached entry was or was not reused.
type HitOutcome string

const (
	HitMiss       HitOutcome = "miss"
	HitVerified   HitOutcome = "verified"
	HitTampered   HitOutcome = "tampered"
	HitWrongSize  HitOutcome = "wrong_size"
	HitBadRecord  HitOutcome = "bad_record"
)

// Store is a content-addressed directory of artifacts plus their records.
type Store struct {
	root string
}

// Open creates the cache root if needed.
func Open(root string) (*Store, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	return &Store{root: root}, nil
}

// Root exposes the cache directory.
func (s *Store) Root() string { return s.root }

// Key derives a stable 64-hex key from source and config digests.
func Key(sourceSHA [32]byte, configDigest string) string {
	h := sha256.New()
	h.Write(sourceSHA[:])
	h.Write([]byte(configDigest))
	return hex.EncodeToString(h.Sum(nil))
}

// ConfigDigest hashes the sorted configuration key/value pairs so that
// equal configurations always produce the same digest.
func ConfigDigest(cfg map[string]string) string {
	keys := make([]string, 0, len(cfg))
	for k := range cfg {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		h.Write([]byte(k))
		h.Write([]byte{0})
		h.Write([]byte(cfg[k]))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (s *Store) paths(key string) (artifact, record string) {
	return filepath.Join(s.root, key+".bin"), filepath.Join(s.root, key+".json")
}

// Put atomically writes the artifact and its verification record. The
// artifact digest is computed by the caller from the exact bytes delivered.
func (s *Store) Put(e Entry, artifact []byte) error {
	if e.Key == "" {
		return fmt.Errorf("cache: empty key")
	}
	sum := sha256.Sum256(artifact)
	e.ArtifactSHA = hex.EncodeToString(sum[:])
	e.Size = int64(len(artifact))
	ap, rp := s.paths(e.Key)
	if err := atomicWrite(ap, artifact); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(rp, raw)
}

// Get verifies a cached artifact against its record and the expected
// source digest. Any discrepancy returns a non-verified outcome and the
// caller is expected to rebuild rather than trust the bytes.
func (s *Store) Get(key, sourceSHA, mediaType string) ([]byte, Entry, HitOutcome) {
	ap, rp := s.paths(key)
	var e Entry
	raw, err := os.ReadFile(rp)
	if err != nil {
		return nil, e, HitMiss
	}
	if err := json.Unmarshal(raw, &e); err != nil || e.Key != key {
		s.invalidate(key)
		return nil, e, HitBadRecord
	}
	if e.SourceSHA != sourceSHA || (mediaType != "" && e.MediaType != mediaType) {
		s.invalidate(key)
		return nil, e, HitTampered
	}
	artifact, err := os.ReadFile(ap)
	if err != nil {
		s.invalidate(key)
		return nil, e, HitMiss
	}
	if int64(len(artifact)) != e.Size {
		s.invalidate(key)
		return nil, e, HitWrongSize
	}
	sum := sha256.Sum256(artifact)
	if hex.EncodeToString(sum[:]) != e.ArtifactSHA {
		s.invalidate(key)
		return nil, e, HitTampered
	}
	return artifact, e, HitVerified
}

// invalidate removes every file belonging to a key.
func (s *Store) invalidate(key string) {
	ap, rp := s.paths(key)
	os.Remove(ap)
	os.Remove(rp)
}

// VerifyArtifact re-hashes an artifact already materialized outside the
// cache (e.g. an output clip) against an expected digest and length.
func VerifyArtifact(path, wantSHA string, wantSize int64) HitOutcome {
	fi, err := os.Stat(path)
	if err != nil {
		return HitMiss
	}
	if fi.Size() != wantSize {
		return HitWrongSize
	}
	f, err := os.Open(path)
	if err != nil {
		return HitMiss
	}
	defer f.Close()
	h := sha256.New()
	buf := make([]byte, 64*1024)
	for {
		n, err := f.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}
	if hex.EncodeToString(h.Sum(nil)) != wantSHA {
		return HitTampered
	}
	return HitVerified
}

// atomicWrite writes to a temp file in the same directory and renames it
// into place, so readers never observe a half-written artifact.
func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}
