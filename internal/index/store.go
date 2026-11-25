package index

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"continuity/internal/media"
)

// BlockRecord is the persisted, reviewable state of one processed block.
type BlockRecord struct {
	Index       int              `json:"index"`
	StartSample int64            `json:"start_sample"`
	EndSample   int64            `json:"end_sample"`
	ContentHash string           `json:"content_hash"`
	GapSamples  int64            `json:"gap_samples"`
	GapRanges   []BlockRecordGap `json:"gap_ranges,omitempty"`
	PeakDBFS    float64          `json:"peak_dbfs"`
	Complete    bool             `json:"complete"`
}

// BlockRecordGap marks a missing sample range inside a block.
type BlockRecordGap struct {
	StartSample int64 `json:"start_sample"`
	EndSample   int64 `json:"end_sample"`
}

// ContainerRecord captures the separately-hashed MP4 container metadata.
type ContainerRecord struct {
	Hash         string `json:"hash"`
	Timescale    uint32 `json:"timescale"`
	Duration     uint64 `json:"duration"`
	Brand        string `json:"brand"`
	BoxSignature string `json:"box_signature"`
}

// Store is the on-disk incremental index. Writes go to a temporary
// record and are atomically renamed, so an interrupted process never
// leaves a half-written checkpoint behind.
type Store struct {
	dir             string
	indexPath       string
	tmpPath         string
	state           stateFile
	rollbackDropped int
	rollbackReason  string
}

type stateFile struct {
	Version   int              `json:"version"`
	Blocks    []BlockRecord    `json:"blocks"`
	Container *ContainerRecord `json:"container,omitempty"`
	LastValid int              `json:"last_valid_index"`
}

// OpenStore loads (or initializes) the index inside workDir. A corrupt
// index is rolled back to the last valid block boundary rather than
// failing outright.
func OpenStore(workDir string) (*Store, error) {
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return nil, media.WrapError(media.CatIO, "index: create work dir", err)
	}
	s := &Store{
		dir:       workDir,
		indexPath: filepath.Join(workDir, "index.json"),
		tmpPath:   filepath.Join(workDir, "index.json.tmp"),
		state:     stateFile{Version: 1, LastValid: -1},
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) load() error {
	raw, err := os.ReadFile(s.indexPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return media.WrapError(media.CatIO, "index: read", err)
	}
	var sf stateFile
	if jerr := json.Unmarshal(raw, &sf); jerr != nil {
		// Unparseable file: no block can be trusted, so the run recovers
		// from zero valid blocks. The on-disk block count is unknowable;
		// report at least one discarded unit for the checkpoint report.
		s.rollbackDropped = maxInt(len(sf.Blocks), 1)
		s.rollbackReason = "index.json is not valid JSON"
		s.state = stateFile{Version: 1, LastValid: -1}
		return nil
	}
	if sf.Version != 1 {
		s.rollbackReason = "unsupported index version"
		s.rollbackDropped = len(sf.Blocks)
		s.state = stateFile{Version: 1, LastValid: -1}
		return nil
	}
	cut, verr := s.verify(sf)
	if verr != nil {
		s.markRollback(sf, cut, verr.Error())
	}
	sf.Blocks = sf.Blocks[:cut+1]
	sf.LastValid = cut
	s.state = sf
	return nil
}

// markRollback records a load-time rollback to the highest verified
// boundary. The caller continues with the truncated, valid state.
func (s *Store) markRollback(sf stateFile, cut int, reason string) {
	s.rollbackReason = reason
	s.rollbackDropped = len(sf.Blocks) - (cut + 1)
}

// verify returns the highest contiguous block index whose record is
// complete and internally consistent.
func (s *Store) verify(sf stateFile) (int, error) {
	last := -1
	for i, b := range sf.Blocks {
		if b.Index != i || !b.Complete || b.ContentHash == "" || b.EndSample <= b.StartSample {
			return last, media.NewError(media.CatCheckpoint, "checkpoint record failed verification")
		}
		last = i
	}
	return last, nil
}

// RollbackInfo describes the most recent load-time rollback.
type RollbackInfo struct {
	Dropped int
	Reason  string
}

// Rollback returns details when OpenStore had to discard corrupt blocks.
func (s *Store) Rollback() (RollbackInfo, bool) {
	if s.rollbackReason == "" {
		return RollbackInfo{}, false
	}
	return RollbackInfo{Dropped: s.rollbackDropped, Reason: s.rollbackReason}, true
}

// CommitBlock appends (or replaces at the same index) one block record
// via a temp-file write and atomic rename.
func (s *Store) CommitBlock(rec BlockRecord) error {
	rec.Complete = true
	if rec.Index <= len(s.state.Blocks) {
		// Normal append, or re-processing the exact next boundary.
		if rec.Index == len(s.state.Blocks) {
			s.state.Blocks = append(s.state.Blocks, rec)
		} else {
			s.state.Blocks[rec.Index] = rec
		}
	} else {
		return media.NewError(media.CatState, "index: non-contiguous block commit")
	}
	s.state.LastValid = len(s.state.Blocks) - 1
	return s.persist()
}

// SetContainer stores the independent container digest.
func (s *Store) SetContainer(c ContainerRecord) error {
	s.state.Container = &c
	return s.persist()
}

func (s *Store) persist() error {
	raw, err := json.MarshalIndent(&s.state, "", "  ")
	if err != nil {
		return media.WrapError(media.CatIO, "index: marshal", err)
	}
	if err := os.WriteFile(s.tmpPath, raw, 0o644); err != nil {
		return media.WrapError(media.CatIO, "index: temp write", err)
	}
	if err := os.Rename(s.tmpPath, s.indexPath); err != nil {
		return media.WrapError(media.CatIO, "index: atomic rename", err)
	}
	return nil
}

// Blocks returns a copy of the verified block records.
func (s *Store) Blocks() []BlockRecord {
	out := make([]BlockRecord, len(s.state.Blocks))
	copy(out, s.state.Blocks)
	return out
}

// Container returns the stored container record, if any.
func (s *Store) Container() (ContainerRecord, bool) {
	if s.state.Container == nil {
		return ContainerRecord{}, false
	}
	return *s.state.Container, true
}

// FindHash returns the stored content hash for a block index, if present.
func (s *Store) FindHash(index int) (string, bool) {
	if index < 0 || index >= len(s.state.Blocks) {
		return "", false
	}
	return s.state.Blocks[index].ContentHash, true
}

// TruncateFrom discards every block at index >= from so downstream
// assets depending on a changed block are invalidated together.
func (s *Store) TruncateFrom(from int) (int, error) {
	if from < 0 || from >= len(s.state.Blocks) {
		return 0, nil
	}
	dropped := len(s.state.Blocks) - from
	s.state.Blocks = s.state.Blocks[:from]
	s.state.LastValid = len(s.state.Blocks) - 1
	return dropped, s.persist()
}

// BlockCount returns the number of verified blocks currently stored.
func (s *Store) BlockCount() int { return len(s.state.Blocks) }

// CorruptTailRecord is a fault-injection helper for the acceptance demo.
// It appends an unverifiable (incomplete) block record after the valid
// prefix and persists it, so the next OpenStore rolls back to the last
// valid boundary. It returns the number of valid blocks kept.
func CorruptTailRecord(workDir string) (int, error) {
	path := filepath.Join(workDir, "index.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, media.WrapError(media.CatIO, "corrupt: read checkpoint", err)
	}
	var sf stateFile
	if err := json.Unmarshal(raw, &sf); err != nil {
		return 0, media.WrapError(media.CatCheckpoint, "corrupt: checkpoint already unreadable", err)
	}
	valid := len(sf.Blocks)
	sf.Blocks = append(sf.Blocks, BlockRecord{Index: valid, Complete: false})
	out, err := json.MarshalIndent(&sf, "", "  ")
	if err != nil {
		return 0, media.WrapError(media.CatIO, "corrupt: marshal", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return 0, media.WrapError(media.CatIO, "corrupt: temp write", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return 0, media.WrapError(media.CatIO, "corrupt: rename", err)
	}
	return valid, nil
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
