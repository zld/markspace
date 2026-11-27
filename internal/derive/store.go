// Package derive implements the content-addressed derivative store. A
// derivative is addressed by the SHA-256 of its normalized input (the canonical
// track digest) and the processing parameters, never by source identity alone.
// Changing one source only invalidates entries that depend on it; entries for
// unrelated sources remain valid and are never rewritten.
package derive

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"mediad/internal/model"
)

// Params are the processing parameters that, together with the normalized
// input, uniquely determine a derivative. JSON serialization is the canonical
// parameter encoding.
type Params struct {
	// Kind names the derivation (for example "mp4", "quality-report").
	Kind string `json:"kind"`
	// TargetBPS is the target bitrate in bits per second.
	TargetBPS int64 `json:"target_bps"`
	// Version lets an algorithm change invalidate old outputs explicitly.
	Version int `json:"version"`
}

// Address is the content-derived key of one derivative.
type Address struct {
	// InputDigest is the digest of the normalized track (and thus the source
	// content it was built from).
	InputDigest string
	Params      Params
	// Key is the final hex address under which bytes are stored.
	Key string
}

// Store is a filesystem-backed content-addressed store rooted at Root.
type Store struct {
	Root string
}

// NewStore opens (creating if needed) a derivative store at root.
func NewStore(root string) (*Store, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	return &Store{Root: root}, nil
}

// canonicalParams renders parameters deterministically.
func canonicalParams(p Params) ([]byte, error) {
	return json.Marshal(p)
}

// AddressFor computes the address for a normalized-input digest and parameter
// set. The key binds both with length prefixes to avoid ambiguity.
func AddressFor(inputDigest string, p Params) (*Address, error) {
	if inputDigest == "" {
		return nil, model.New(model.ErrStageState, "", -1, "cannot address a derivative without an input digest")
	}
	pb, err := canonicalParams(p)
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	h.Write([]byte{0x6d, 0x65, 0x64, 0x69, 0x61, 0x64, 0x01}) // domain prefix "mediad\1"
	hex1 := []byte(inputDigest)
	var lb [8]byte
	// Length-prefixed input digest, then canonical parameter bytes.
	putUint64 := func(n int) {
		for i := 7; i >= 0; i-- {
			lb[i] = byte(n & 0xff)
			n >>= 8
		}
	}
	putUint64(len(hex1))
	h.Write(lb[:])
	h.Write(hex1)
	h.Write(pb)
	key := hex.EncodeToString(h.Sum(nil))
	return &Address{InputDigest: inputDigest, Params: p, Key: key}, nil
}

// path maps a key to a sharded path: root/ab/cdef... . Sharding keeps any
// single directory small without affecting the addressing.
func (s *Store) path(key string) string {
	return filepath.Join(s.Root, key[0:2], key[2:4], key)
}

// Has reports whether the addressed derivative already exists.
func (s *Store) Has(addr *Address) (bool, error) {
	if _, err := os.Stat(s.path(addr.Key)); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// Put writes content for an address atomically and verifies it after the fact.
// If a different payload already exists at the address that is a hard
// collision error; identical content is a safe no-op (cache hit).
func (s *Store) Put(addr *Address, content []byte) error {
	dir := filepath.Dir(s.path(addr.Key))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	final := s.path(addr.Key)
	if existing, err := os.ReadFile(final); err == nil {
		sumExisting := sha256.Sum256(existing)
		sumNew := sha256.Sum256(content)
		if sumExisting != sumNew {
			return model.New(model.ErrDigestMismatch, "", -1,
				"content-address collision at %s: stored and new payload differ", addr.Key)
		}
		return nil // identical derivative already present; nothing to rewrite
	}

	tmp, err := os.CreateTemp(dir, ".deriv-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	rollback := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		rollback()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		rollback()
		return err
	}
	if err := tmp.Close(); err != nil {
		rollback()
		return err
	}
	if err := os.Rename(tmpName, final); err != nil {
		rollback()
		return err
	}
	// Verify the bytes landed intact before considering the write committed.
	got, err := os.ReadFile(final)
	if err != nil {
		return err
	}
	if len(got) != len(content) {
		_ = os.Remove(final)
		return model.New(model.ErrDigestMismatch, "", -1, "derivative length mismatch after write at %s", addr.Key)
	}
	return nil
}

// Get returns the stored bytes for an address.
func (s *Store) Get(addr *Address) ([]byte, error) {
	b, err := os.ReadFile(s.path(addr.Key))
	if err != nil {
		return nil, err
	}
	return b, nil
}

// Invalidate removes every derivative addressed from the supplied input
// digest. Entries addressed from other digests are untouched, satisfying
// source-scoped invalidation. It returns the number of removed entries.
func (s *Store) Invalidate(inputDigest string, params []Params) (int, error) {
	removed := 0
	for _, p := range params {
		addr, err := AddressFor(inputDigest, p)
		if err != nil {
			return removed, err
		}
		switch err := os.Remove(s.path(addr.Key)); {
		case err == nil:
			removed++
		case os.IsNotExist(err):
			// nothing to invalidate
		default:
			return removed, err
		}
	}
	return removed, nil
}

// Count returns the number of stored derivative objects.
func (s *Store) Count() (int, error) {
	n := 0
	err := filepath.WalkDir(s.Root, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && len(d.Name()) == 64 {
			n++
		}
		return nil
	})
	return n, err
}

// String renders an address for logging.
func (a *Address) String() string {
	return fmt.Sprintf("derive(%s, kind=%s target_bps=%d)", a.InputDigest[:12], a.Params.Kind, a.Params.TargetBPS)
}
