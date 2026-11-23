// Package cache is a content-addressed store. Keys derive from the input
// bytes, the time base and the processing parameters; hits are still
// verified against origin and digest, and tampered entries are
// quarantined and recomputed.
package cache

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"mediqc/internal/fault"
)

// Store keeps entries in a directory: <key>.bin payload plus a
// <key>.meta sidecar holding the origin and the payload digest.
type Store struct {
	dir       string
	Hits      int
	Misses    int
	Tampered  int
	Quaranted []string // keys moved to quarantine
}

// Open creates (or reuses) a store directory.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

// Key derives the content address from input bytes, time base and params.
func Key(input []byte, timebase, params string) string {
	h := sha256.New()
	h.Write(input)
	h.Write([]byte{0})
	h.Write([]byte(timebase))
	h.Write([]byte{0})
	h.Write([]byte(params))
	return hex.EncodeToString(h.Sum(nil))
}

// Digest is the payload checksum stored alongside each entry.
func Digest(payload []byte) [32]byte { return sha256.Sum256(payload) }

// Get looks up key. A hit is returned only when the stored origin matches
// and the payload digest verifies with a fixed-length constant-time
// compare; otherwise the entry is quarantined and reported as tampered.
func (s *Store) Get(key, origin string) (payload []byte, hit, tampered bool, err error) {
	bin := filepath.Join(s.dir, key+".bin")
	meta := filepath.Join(s.dir, key+".meta")
	raw, err := os.ReadFile(bin)
	if errors.Is(err, fs.ErrNotExist) {
		s.Misses++
		return nil, false, false, nil
	}
	if err != nil {
		return nil, false, false, err
	}
	mraw, err := os.ReadFile(meta)
	if err != nil {
		s.quarantine(key)
		s.Tampered++
		return nil, false, true, nil
	}
	parts := strings.SplitN(strings.TrimSpace(string(mraw)), "\n", 2)
	if len(parts) != 2 {
		s.quarantine(key)
		s.Tampered++
		return nil, false, true, nil
	}
	wantOrigin, wantDigest := parts[0], parts[1]
	digest := Digest(raw)
	if subtle.ConstantTimeCompare([]byte(wantOrigin), []byte(origin)) != 1 ||
		subtle.ConstantTimeCompare([]byte(wantDigest),
			[]byte(hex.EncodeToString(digest[:]))) != 1 {
		s.quarantine(key)
		s.Tampered++
		return nil, false, true, nil
	}
	s.Hits++
	return raw, true, false, nil
}

// Put stores payload under key with its origin and digest.
func (s *Store) Put(key, origin string, payload []byte) error {
	digest := Digest(payload)
	meta := fmt.Sprintf("%s\n%s", origin, hex.EncodeToString(digest[:]))
	if err := os.WriteFile(filepath.Join(s.dir, key+".bin"), payload, 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.dir, key+".meta"), []byte(meta), 0o644)
}

// quarantine moves a suspect entry aside so it is never served again.
func (s *Store) quarantine(key string) {
	qdir := filepath.Join(s.dir, "quarantine")
	if err := os.MkdirAll(qdir, 0o755); err != nil {
		return
	}
	for _, ext := range []string{".bin", ".meta"} {
		os.Rename(filepath.Join(s.dir, key+ext), filepath.Join(qdir, key+ext))
	}
	s.Quaranted = append(s.Quaranted, key)
}

// ErrTamper is returned to callers that want a fault value for a tamper event.
var ErrTamper = fault.New(fault.CodeCacheTamper, 0, -1,
	"cached digest or origin mismatch", "entry quarantined; recompute from source")
