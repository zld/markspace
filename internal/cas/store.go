// Package cas implements a content-addressed store for derived artifacts.
// Source files are read-only and identified by SHA-256. A cache key binds the
// source digest, a normalization version and the processing parameters, so a
// changed source only invalidates the derivatives that depend on it.
package cas

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"mediamerge/internal/fault"
)

// NormalizationVersion is bumped when derived artifact semantics change.
const NormalizationVersion = "norm-1"

// Store is a directory-backed content-addressed cache.
type Store struct {
	root string
}

// Open creates the cache directory if needed and returns a Store.
func Open(root string) (*Store, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fault.New(fault.StageCAS, fault.KindIO, root, err.Error())
	}
	return &Store{root: root}, nil
}

// DigestFile hashes a read-only source file without assuming it fits in memory.
func DigestFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fault.New(fault.StageIO, fault.KindIO, path, err.Error())
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fault.New(fault.StageIO, fault.KindIO, path, err.Error())
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// DigestBytes hashes an in-memory source (used by generated fixtures).
func DigestBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Key is the deterministic cache identity for one derivative.
type Key struct {
	SourceDigest string
	NormVersion  string
	Params       map[string]string
}

// String renders the key as a stable, filesystem-safe identifier.
func (k Key) String() string {
	keys := make([]string, 0, len(k.Params))
	for p := range k.Params {
		keys = append(keys, p)
	}
	sort.Strings(keys)
	h := sha256.New()
	fmt.Fprintf(h, "norm=%s\nsource=%s\n", k.NormVersion, k.SourceDigest)
	for _, p := range keys {
		fmt.Fprintf(h, "%s=%s\n", p, k.Params[p])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Entry is one cached artifact record.
type Entry struct {
	Key          string            `json:"key"`
	SourceDigest string            `json:"source_digest"`
	NormVersion  string            `json:"norm_version"`
	Params       map[string]string `json:"params"`
	PayloadHash  string            `json:"payload_hash"`
	Size         int64             `json:"size"`
}

func (s *Store) path(key string) string     { return filepath.Join(s.root, key+".blob") }
func (s *Store) metaPath(key string) string { return filepath.Join(s.root, key+".json") }

// Put stores payload under key and writes its metadata atomically.
func (s *Store) Put(k Key, payload []byte) (*Entry, error) {
	key := k.String()
	sum := sha256.Sum256(payload)
	tmp, err := os.CreateTemp(s.root, "put-*.tmp")
	if err != nil {
		return nil, fault.New(fault.StageCAS, fault.KindIO, s.root, err.Error())
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(payload); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return nil, fault.New(fault.StageCAS, fault.KindIO, s.root, err.Error())
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return nil, fault.New(fault.StageCAS, fault.KindIO, s.root, err.Error())
	}
	if err := os.Rename(tmpName, s.path(key)); err != nil {
		return nil, fault.New(fault.StageCAS, fault.KindIO, s.root, err.Error())
	}
	e := &Entry{Key: key, SourceDigest: k.SourceDigest, NormVersion: k.NormVersion,
		Params: k.Params, PayloadHash: hex.EncodeToString(sum[:]), Size: int64(len(payload))}
	if err := writeJSONAtomic(s.metaPath(key), e); err != nil {
		return nil, err
	}
	return e, nil
}

// Get returns the cached entry after re-verifying the payload digest.
func (s *Store) Get(k Key) (*Entry, []byte, error) {
	key := k.String()
	meta, err := os.ReadFile(s.metaPath(key))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, fault.New(fault.StageCAS, fault.KindIO, s.root, err.Error())
	}
	var e Entry
	if err := json.Unmarshal(meta, &e); err != nil {
		return nil, nil, fault.New(fault.StageCAS, fault.KindChecksum, key, "corrupt metadata")
	}
	payload, err := os.ReadFile(s.path(key))
	if err != nil {
		return nil, nil, fault.New(fault.StageCAS, fault.KindIO, key, err.Error())
	}
	sum := sha256.Sum256(payload)
	if hex.EncodeToString(sum[:]) != e.PayloadHash {
		return nil, nil, fault.New(fault.StageCAS, fault.KindChecksum, key,
			"cached payload failed digest verification")
	}
	return &e, payload, nil
}

// InvalidateSource removes every derivative whose metadata names sourceDigest.
// Unrelated cache entries are left untouched.
func (s *Store) InvalidateSource(sourceDigest string) (int, error) {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return 0, fault.New(fault.StageCAS, fault.KindIO, s.root, err.Error())
	}
	removed := 0
	for _, ent := range entries {
		if filepath.Ext(ent.Name()) != ".json" {
			continue
		}
		meta, err := os.ReadFile(filepath.Join(s.root, ent.Name()))
		if err != nil {
			return removed, fault.New(fault.StageCAS, fault.KindIO, ent.Name(), err.Error())
		}
		var e Entry
		if err := json.Unmarshal(meta, &e); err != nil {
			continue
		}
		if e.SourceDigest != sourceDigest {
			continue
		}
		base := ent.Name()[:len(ent.Name())-len(".json")]
		if err := os.Remove(filepath.Join(s.root, base+".blob")); err != nil && !os.IsNotExist(err) {
			return removed, fault.New(fault.StageCAS, fault.KindIO, base, err.Error())
		}
		if err := os.Remove(filepath.Join(s.root, ent.Name())); err != nil {
			return removed, fault.New(fault.StageCAS, fault.KindIO, ent.Name(), err.Error())
		}
		removed++
	}
	return removed, nil
}

func writeJSONAtomic(path string, v interface{}) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fault.New(fault.StageCAS, fault.KindState, path, err.Error())
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "meta-*.tmp")
	if err != nil {
		return fault.New(fault.StageCAS, fault.KindIO, path, err.Error())
	}
	name := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(name)
		return fault.New(fault.StageCAS, fault.KindIO, path, err.Error())
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return fault.New(fault.StageCAS, fault.KindIO, path, err.Error())
	}
	if err := os.Rename(name, path); err != nil {
		return fault.New(fault.StageCAS, fault.KindIO, path, err.Error())
	}
	return nil
}
