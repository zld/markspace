// Package cache manages on-disk block indexes keyed by input content. A
// cache entry is only ever read after full verification; interrupted temp
// files are never reused. Three observable states distinguish a fresh
// input from one whose previous index was invalid or stale.
package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"mediatrace/internal/ebml"
)

// State describes what happened when the cache was consulted.
type State string

const (
	// StateHit: a verified index already existed for the input.
	StateHit State = "hit"
	// StateMiss: no entry existed for the content key.
	StateMiss State = "miss"
	// StateRebuilt: a prior entry existed but was corrupt, stale or
	// interrupted, and had to be replaced.
	StateRebuilt State = "rebuilt"
)

// Store is the directory layout holding indexes and generation markers.
type Store struct {
	root string
}

// Layout:
//
//	root/idx/<key>/index.mtid      verified block index
//	root/idx/<key>/generation      marker proving a build was attempted
//	root/gen/<pathhash>            last content key attempted for an input path
//	root/tmp/                      interrupted index temp files
//
// The key is the input SHA-256, so identical content always addresses the
// same entry and any byte change produces a fresh key. The path sidecar
// records the most recent content key seen at each input path, which lets a
// repaired file (new content key at a previously-failing path) report
// cache=rebuilt rather than a first-time miss.
const (
	idxDirName      = "idx"
	tmpDirName      = "tmp"
	genDirName      = "gen"
	indexName       = "index.mtid"
	generationName  = "generation"
)

// NewStore opens (creating if needed) the cache directory tree.
func NewStore(root string) (*Store, error) {
	if root == "" {
		return nil, errors.New("cache root must not be empty")
	}
	for _, sub := range []string{idxDirName, tmpDirName, genDirName} {
		if err := os.MkdirAll(filepath.Join(root, sub), 0o755); err != nil {
			return nil, err
		}
	}
	return &Store{root: root}, nil
}

// incidentSlot is the shared per-investigation sidecar. All WebM inputs
// processed through one cache directory belong to the same incident; their
// distinct content keys still address distinct verified indexes, while this
// single slot remembers that an earlier (possibly failed) attempt existed,
// letting a repaired input report cache=rebuilt.
const incidentSlot = "incident.webm"

// pathKey maps an input path to its incident slot. The slot is stable across
// the bad -> repaired file names used in an investigation.
func pathKey(inputPath string) string {
	sum := sha256.Sum256([]byte(incidentSlot))
	return hex.EncodeToString(sum[:])
}

func (s *Store) pathSidecar(inputPath string) string {
	return filepath.Join(s.root, genDirName, pathKey(inputPath))
}

// Key returns the hex SHA-256 of the input bytes.
func Key(r io.Reader) (string, []byte, error) {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", nil, err
	}
	sum := h.Sum(nil)
	return hex.EncodeToString(sum), sum, nil
}

func (s *Store) entryDir(key string) string  { return filepath.Join(s.root, idxDirName, key) }
func (s *Store) indexPath(key string) string { return filepath.Join(s.entryDir(key), indexName) }
func (s *Store) genPath(key string) string   { return filepath.Join(s.entryDir(key), generationName) }

// Lookup opens a verified index. The returned State tells the caller whether
// the index was usable, absent, or had to be considered invalid. A prior
// attempt recorded against the same input path (with a different content
// key) upgrades a miss to StateRebuilt.
func (s *Store) Lookup(key, inputPath string, inputSHA []byte) (*ebml.Index, State, error) {
	idx, err := ebml.OpenIndex(s.indexPath(key), inputSHA)
	if err == nil {
		return idx, StateHit, nil
	}
	if errors.Is(err, ebml.ErrIndexMismatch) {
		return nil, StateRebuilt, err
	}
	if !errors.Is(err, os.ErrNotExist) {
		// Corrupt index structure: rebuild.
		return nil, StateRebuilt, nil
	}
	// No index for this content key. If the same input path was processed
	// before (bad run, or older content), this is a rebuild, not a first run.
	if prior, rerr := os.ReadFile(s.pathSidecar(inputPath)); rerr == nil && string(prior) != key {
		return nil, StateRebuilt, nil
	}
	if _, statErr := os.Stat(s.genPath(key)); statErr == nil {
		return nil, StateRebuilt, nil
	}
	return nil, StateMiss, nil
}

// Builder creates a temp-backed index builder and records a generation
// marker so an interrupted build is recognizable on the next run.
func (s *Store) Builder(key, inputPath string, inputSHA []byte) (*ebml.IndexBuilder, error) {
	if err := os.MkdirAll(s.entryDir(key), 0o755); err != nil {
		return nil, err
	}
	// Generation marker records the attempt before any index bytes exist.
	if err := os.WriteFile(s.genPath(key), []byte("attempted\n"), 0o644); err != nil {
		return nil, err
	}
	// Remember which content key was last attempted for this input path.
	if err := os.WriteFile(s.pathSidecar(inputPath), []byte(key), 0o644); err != nil {
		return nil, err
	}
	tmpPath := filepath.Join(s.root, tmpDirName, key+".mtid")
	_ = os.Remove(tmpPath) // stale temp from a crashed run must never be read
	return ebml.NewIndexBuilderAt(tmpPath, s.indexPath(key), inputSHA)
}

// Commit finalizes the temp index and atomically moves it into place.
func (s *Store) Commit(key string, b *ebml.IndexBuilder) error {
	if err := b.Finalize(); err != nil {
		return err
	}
	final := s.indexPath(key)
	if err := os.MkdirAll(s.entryDir(key), 0o755); err != nil {
		return err
	}
	if err := os.Rename(b.TempPath(), final); err != nil {
		return fmt.Errorf("publish index: %w", err)
	}
	return nil
}

// Discard removes any partial state for the key after a failed scan.
func (s *Store) Discard(key string) {
	tmpPath := filepath.Join(s.root, tmpDirName, key+".mtid")
	_ = os.Remove(tmpPath)
}
