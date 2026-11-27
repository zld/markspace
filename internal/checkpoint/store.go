// Package checkpoint persists unit-level progress so an interrupted run can
// resume from the last complete unit without reprocessing or producing a
// different output.
//
// One checkpoint file per run holds an append-only, sequence-numbered list of
// committed units.  Every append rewrites the file atomically (temp file, sync,
// rename) with a trailing whole-file CRC, so a crash mid-write can never leave
// a checkpoint that claims work which was not recorded.  Replaying a committed
// unit is idempotent: the same key may be committed again with identical
// results and the store ignores it; committing the same key with different
// results is rejected as a conflict.
package checkpoint

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"mediapipeline/internal/clock"
	"mediapipeline/internal/fault"
)

// UnitRecord is one committed unit.  Digest is the hex SHA-256 of the unit's
// output, so a re-run can prove it reproduced identical bytes.
type UnitRecord struct {
	Key    string `json:"key"`    // stable unit identity, e.g. "u0007"
	Digest string `json:"digest"` // hex SHA-256 of the unit output
	Bytes  int64  `json:"bytes"`  // output byte count
	Seq    int    `json:"seq"`    // commit order, 1-based
}

// state is the on-disk representation.  The trailing CRC covers the canonical
// encoding of Records with CRC zeroed, so a corrupted file is rejected rather
// than trusted.
type state struct {
	Version int          `json:"version"`
	Records []UnitRecord `json:"records"`
	CRC     uint32       `json:"crc"`
}

const formatVersion = 1

// Store is an open checkpoint file.
type Store struct {
	path    string
	records []UnitRecord
	index   map[string]UnitRecord
	commits int
	reports *fault.Report
}

// Open loads an existing checkpoint or starts an empty one.  A corrupt file is
// reported with CHECKPOINT_CORRUPT and an error; callers must not treat it as
// empty, because that would silently redo committed work.
func Open(path string, report *fault.Report) (*Store, error) {
	if report == nil {
		return nil, errors.New("checkpoint: nil fault report")
	}
	s := &Store{path: path, index: map[string]UnitRecord{}, reports: report}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s, nil
		}
		return nil, fmt.Errorf("checkpoint: read: %w", err)
	}
	var st state
	if err := json.Unmarshal(data, &st); err != nil {
		report.Addf(fault.CodeCheckpointBad, -1, zero(), zero(),
			"checkpoint %s is not valid JSON: %v", path, err)
		return nil, fmt.Errorf("checkpoint: decode: %w", err)
	}
	if st.Version != formatVersion {
		report.Addf(fault.CodeCheckpointBad, -1, zero(), zero(),
			"checkpoint %s has unsupported version %d", path, st.Version)
		return nil, fmt.Errorf("checkpoint: unsupported version %d", st.Version)
	}
	if got := checksum(st.Records); got != st.CRC {
		report.Addf(fault.CodeCheckpointBad, -1, zero(), zero(),
			"checkpoint %s checksum %08x does not match stored %08x", path, got, st.CRC)
		return nil, errors.New("checkpoint: checksum mismatch")
	}
	for _, r := range st.Records {
		if prev, dup := s.index[r.Key]; dup && prev.Digest != r.Digest {
			report.Addf(fault.CodeCheckpointBad, -1, zero(), zero(),
				"checkpoint %s has conflicting digests for key %s", path, r.Key)
			return nil, errors.New("checkpoint: conflicting records")
		}
		s.index[r.Key] = r
	}
	s.records = append([]UnitRecord(nil), st.Records...)
	s.commits = 0 // counts appends made by this process only
	sort.SliceStable(s.records, func(i, j int) bool { return s.records[i].Seq < s.records[j].Seq })
	return s, nil
}

// zero is the trivial rational used for faults with no time extent.
func zero() clock.Rational { return clock.MustRational(0, 1) }

// Commit records a completed unit and persists the checkpoint atomically.
//
// Re-committing an identical key/digest is a no-op and returns committed=false.
// Re-committing a key with a different digest is a conflict and an error.
func (s *Store) Commit(key, digest string, bytes int64) (committed bool, err error) {
	if key == "" {
		return false, errors.New("checkpoint: empty unit key")
	}
	if prev, ok := s.index[key]; ok {
		if prev.Digest == digest {
			return false, nil
		}
		s.reports.Addf(fault.CodeCheckpointBad, -1, zero(), zero(),
			"unit %s replayed with digest %s, previously %s", key, digest, prev.Digest)
		return false, fmt.Errorf("checkpoint: unit %s digest conflict", key)
	}
	rec := UnitRecord{Key: key, Digest: digest, Bytes: bytes, Seq: len(s.records) + 1}
	s.records = append(s.records, rec)
	s.index[key] = rec
	if err := s.persist(); err != nil {
		// Roll the in-memory state back so it matches the disk.
		s.records = s.records[:len(s.records)-1]
		delete(s.index, key)
		return false, err
	}
	s.commits++
	return true, nil
}

// persist writes the whole checkpoint atomically with a trailing CRC.
func (s *Store) persist() error {
	st := state{Version: formatVersion, Records: s.records}
	st.CRC = checksum(st.Records)
	data, err := json.Marshal(st)
	if err != nil {
		return fmt.Errorf("checkpoint: encode: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("checkpoint: mkdir: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".ckpt-*")
	if err != nil {
		return fmt.Errorf("checkpoint: temp: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("checkpoint: write: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("checkpoint: sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("checkpoint: close: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("checkpoint: rename: %w", err)
	}
	return nil
}

// Done reports whether a unit key already has a committed record.
func (s *Store) Done(key string) bool {
	_, ok := s.index[key]
	return ok
}

// Record returns the committed record for a key.
func (s *Store) Record(key string) (UnitRecord, bool) {
	r, ok := s.index[key]
	return r, ok
}

// Records returns the committed units in commit order.
func (s *Store) Records() []UnitRecord {
	out := make([]UnitRecord, len(s.records))
	copy(out, s.records)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out
}

// Len returns the number of committed units.
func (s *Store) Len() int { return len(s.records) }

// Commits returns how many units this store appended in this process.
func (s *Store) Commits() int { return s.commits }

// Path returns the checkpoint file path.
func (s *Store) Path() string { return s.path }

// Rollback removes every record committed after the given sequence number and
// persists the truncated state.  It returns the removed records so the caller
// can report what was discarded.
func (s *Store) Rollback(toSeq int) ([]UnitRecord, error) {
	if toSeq < 0 {
		return nil, errors.New("checkpoint: negative rollback sequence")
	}
	var removed []UnitRecord
	kept := s.records[:0]
	for _, r := range s.records {
		if r.Seq > toSeq {
			removed = append(removed, r)
			delete(s.index, r.Key)
			continue
		}
		kept = append(kept, r)
	}
	s.records = kept
	if err := s.persist(); err != nil {
		return nil, err
	}
	return removed, nil
}

// checksum is a deterministic FNV-1a over the canonical record encoding.  It
// detects truncation and reordering; the digest inside each record is the real
// content proof.
func checksum(records []UnitRecord) uint32 {
	const (
		offset32 = 2166136261
		prime32  = 16777619
	)
	h := uint32(offset32)
	mix := func(b []byte) {
		for _, c := range b {
			h ^= uint32(c)
			h *= prime32
		}
	}
	for _, r := range records {
		// Length-prefixing each field keeps boundaries unambiguous.
		for _, field := range []string{r.Key, r.Digest} {
			var n [4]byte
			n[0] = byte(len(field))
			n[1] = byte(len(field) >> 8)
			n[2] = byte(len(field) >> 16)
			n[3] = byte(len(field) >> 24)
			mix(n[:])
			mix([]byte(field))
		}
		var seq [8]byte
		for i := 0; i < 8; i++ {
			seq[i] = byte(uint64(r.Seq) >> (8 * i))
		}
		mix(seq[:])
		var b [8]byte
		for i := 0; i < 8; i++ {
			b[i] = byte(uint64(r.Bytes) >> (8 * i))
		}
		mix(b[:])
	}
	return h
}
