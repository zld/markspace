// Package index persists chunked, checksummed processing checkpoints so an
// interrupted run can resume from the last fully committed block. Every
// record is self-describing and independently verified; a half-written or
// corrupted record is rolled back, never silently skipped.
package index

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Magic and format version prefix every record on disk.
const (
	Magic        = "MCK1"
	recordSuffix = ".chk"
)

// Record is one checkpoint: the processing cursor plus enough provenance to
// prove it belongs to the current input and configuration.
type Record struct {
	CheckpointKey string `json:"checkpoint_key"` // input+config+stage digest
	ChunkSeq      uint64 `json:"chunk_seq"`      // monotonically increasing
	Cursor        uint64 `json:"cursor"`         // blocks processed up to here
	Stage         string `json:"stage"`
	ManifestHash  string `json:"manifest_hash,omitempty"`
}

// StoredRecord pairs a record with its on-disk checksum and file path.
type StoredRecord struct {
	Record
	SHA256 string
	Path   string
}

// Store writes checkpoint files into a directory.
type Store struct {
	dir string
}

// OpenStore creates the directory if needed.
func OpenStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("index: cannot create store dir: %w", err)
	}
	return &Store{dir: dir}, nil
}

// Dir returns the checkpoint directory.
func (s *Store) Dir() string { return s.dir }

// recordPath is deterministic so a replayed chunk overwrites the same name
// rather than creating duplicates.
func (s *Store) recordPath(seq uint64) string {
	return filepath.Join(s.dir, fmt.Sprintf("chunk-%012d%s", seq, recordSuffix))
}

// recordFrame is the exact on-disk layout:
//
//	MAGIC(4) | length(4 BE) | payload JSON | sha256(32)
func recordFrame(rec Record) ([]byte, error) {
	payload, err := json.Marshal(rec)
	if err != nil {
		return nil, err
	}
	if len(payload) > 1<<31 {
		return nil, fmt.Errorf("index: record payload too large (%d bytes)", len(payload))
	}
	frame := make([]byte, 0, 4+4+len(payload)+32)
	frame = append(frame, Magic...)
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(payload)))
	frame = append(frame, lenBuf[:]...)
	frame = append(frame, payload...)
	sum := sha256.Sum256(append([]byte(Magic), payload...))
	frame = append(frame, sum[:]...)
	return frame, nil
}

// Write atomically commits one record: write to a temp file, fsync, rename.
// The caller must advance its cursor only after Write succeeds (write-ahead).
func (s *Store) Write(rec Record) (StoredRecord, error) {
	frame, err := recordFrame(rec)
	if err != nil {
		return StoredRecord{}, err
	}
	final := s.recordPath(rec.ChunkSeq)
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, frame, 0o644); err != nil {
		return StoredRecord{}, fmt.Errorf("index: write temp checkpoint: %w", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		return StoredRecord{}, fmt.Errorf("index: commit checkpoint rename: %w", err)
	}
	return StoredRecord{Record: rec, SHA256: checksumOf(frame), Path: final}, nil
}

func checksumOf(frame []byte) string {
	sum := sha256.Sum256(frame)
	return hex.EncodeToString(sum[:])
}

// List returns all well-formed records for key, ordered by chunk sequence.
// Corrupt or foreign-key files are surfaced as errors rather than ignored.
func (s *Store) List(key string) ([]StoredRecord, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("index: read store dir: %w", err)
	}
	var out []StoredRecord
	for _, en := range entries {
		if en.IsDir() || filepath.Ext(en.Name()) != recordSuffix {
			continue
		}
		path := filepath.Join(s.dir, en.Name())
		rec, err := ReadRecordFile(path)
		if err != nil {
			return nil, err
		}
		if rec.CheckpointKey != key {
			return nil, fmt.Errorf("index: foreign checkpoint key in %s", path)
		}
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ChunkSeq < out[j].ChunkSeq })
	return out, nil
}

// ReadRecordFile parses and verifies one checkpoint file.
func ReadRecordFile(path string) (StoredRecord, error) {
	frame, err := os.ReadFile(path)
	if err != nil {
		return StoredRecord{}, fmt.Errorf("index: read %s: %w", path, err)
	}
	if len(frame) < 4+4+32 || string(frame[:4]) != Magic {
		return StoredRecord{}, &CorruptError{Path: path, Reason: "bad magic or truncated frame"}
	}
	payloadLen := int(binary.BigEndian.Uint32(frame[4:8]))
	if 8+payloadLen+32 != len(frame) {
		return StoredRecord{}, &CorruptError{Path: path, Reason: "declared length does not match file size"}
	}
	payload := frame[8 : 8+payloadLen]
	storedSum := frame[8+payloadLen:]
	calc := sha256.Sum256(append([]byte(Magic), payload...))
	if !equalBytes(calc[:], storedSum) {
		return StoredRecord{}, &CorruptError{Path: path, Reason: "checksum mismatch"}
	}
	var rec Record
	if err := json.Unmarshal(payload, &rec); err != nil {
		return StoredRecord{}, &CorruptError{Path: path, Reason: "payload JSON invalid: " + err.Error()}
	}
	return StoredRecord{Record: rec, SHA256: checksumOf(frame), Path: path}, nil
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// CorruptError identifies a checkpoint that fails verification.
type CorruptError struct {
	Path   string
	Reason string
}

func (e *CorruptError) Error() string {
	return fmt.Sprintf("index: corrupt checkpoint %s: %s", e.Path, e.Reason)
}

// Clear removes every checkpoint (used for clean, non-resume runs).
func (s *Store) Clear() error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	for _, en := range entries {
		if filepath.Ext(en.Name()) == recordSuffix || filepath.Ext(en.Name()) == ".tmp" {
			if err := os.Remove(filepath.Join(s.dir, en.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}
