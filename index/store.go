package index

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// SampleIndex locates one sample in the input file and places it on the track
// timeline. DecodeTicks and CompositeTicks are raw ticks in the track's time
// base (never unwrapped 64-bit seconds): the time base travels with the index.
type SampleIndex struct {
	Offset         int64 `json:"offset"`
	Size           int64 `json:"size"`
	DecodeTicks    int64 `json:"dts"`
	CompositeTicks int64 `json:"cts"`
	DurationTicks  int64 `json:"duration"`
	Sync           bool  `json:"sync"`
}

// TrackIndex is the parsed layout of one track.
type TrackIndex struct {
	TrackID   uint32        `json:"track_id"`
	Handler   string        `json:"handler"` // vide, soun, subt, text, sbtl, meta
	TimeScale uint32        `json:"timescale"`
	Samples   []SampleIndex `json:"samples"`
	// EditOffsetTicks shifts media time onto presentation time for this track.
	EditOffsetTicks int64 `json:"edit_offset_ticks,omitempty"`
}

// Keyframes returns the sample indices of sync samples, ascending.
func (t TrackIndex) Keyframes() []int {
	var out []int
	for i, s := range t.Samples {
		if s.Sync {
			out = append(out, i)
		}
	}
	return out
}

// EndTicks returns the largest decode end time in the track.
func (t TrackIndex) EndTicks() int64 {
	var end int64
	for _, s := range t.Samples {
		if e := s.DecodeTicks + s.DurationTicks; e > end {
			end = e
		}
	}
	return end
}

// ParseIndex is everything needed to slice a file into segments without
// re-parsing containers. It is invalidated as a unit when the container
// fingerprint changes.
type ParseIndex struct {
	Fingerprint Fingerprint  `json:"fingerprint"`
	Regions     []Region     `json:"regions,omitempty"`
	Tracks      []TrackIndex `json:"tracks"`
	// ParserVersion lets a parser change invalidate all stored indexes.
	ParserVersion int   `json:"parser_version"`
	CreatedUnix   int64 `json:"created_unix"`
}

// Track returns the track with the given id.
func (p *ParseIndex) Track(id uint32) (TrackIndex, bool) {
	if p == nil {
		return TrackIndex{}, false
	}
	for _, t := range p.Tracks {
		if t.TrackID == id {
			return t, true
		}
	}
	return TrackIndex{}, false
}

// StoredIndex is the on-disk record for one input path.
type StoredIndex struct {
	Path  string     `json:"path"`
	Index ParseIndex `json:"index"`
}

// Store is a directory of per-input index records. Writes are atomic
// (temp file + rename) so an interrupted batch never leaves a half-written
// index that a resumed run would trust.
type Store struct {
	dir string
	mu  sync.Mutex
}

// OpenStore opens or creates the index directory.
func OpenStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create index dir: %w", err)
	}
	return &Store{dir: dir}, nil
}

func (s *Store) pathFor(inputPath string) string {
	sum := sha256.Sum256([]byte(inputPath))
	return filepath.Join(s.dir, hex.EncodeToString(sum[:16])+".json")
}

// Get returns the stored index for path, or (nil, false) when absent.
func (s *Store) Get(path string) (*ParseIndex, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.pathFor(path))
	if err != nil {
		return nil, false
	}
	var rec StoredIndex
	if err := json.Unmarshal(data, &rec); err != nil || rec.Path != path {
		return nil, false
	}
	return &rec.Index, true
}

// Put stores idx for path atomically.
func (s *Store) Put(path string, idx *ParseIndex) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if idx == nil {
		return errors.New("index: nil parse index")
	}
	idx.CreatedUnix = time.Now().Unix()
	data, err := json.MarshalIndent(StoredIndex{Path: path, Index: *idx}, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(s.pathFor(path), data)
}

// Delete removes any stored index for path.
func (s *Store) Delete(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := os.Remove(s.pathFor(path))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// ReuseFor computes what may be reused for an input whose fingerprint is fp.
// It returns the previous index (possibly nil) together with the reuse plan.
func (s *Store) ReuseFor(path string, fp Fingerprint, parserVersion int) (*ParseIndex, Reuse) {
	prev, ok := s.Get(path)
	if !ok || prev.ParserVersion != parserVersion {
		return nil, Reuse{}
	}
	return prev, fp.Plan(prev.Fingerprint)
}

// Paths lists inputs currently in the store, sorted.
func (s *Store) Paths() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			continue
		}
		var rec StoredIndex
		if json.Unmarshal(data, &rec) == nil && rec.Path != "" {
			out = append(out, rec.Path)
		}
	}
	sort.Strings(out)
	return out, nil
}

// writeAtomic writes data to a temp file in the same directory and renames it
// into place, fsyncing first so a crash cannot expose a partial record.
func writeAtomic(path string, data []byte) error {
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
