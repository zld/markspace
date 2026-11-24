// Package checkpoint persists batch progress in fixed-size block batches and
// guards resume safety. Every checkpoint records the input identity (size and
// SHA-256), the last committed input offset, the number of committed blocks
// and an aggregate checksum. A corrupt index or a changed input is rejected
// with a structured, recoverable fault that names the checkpoint offset.
package checkpoint

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"

	"mediapack/internal/fault"
)

// IndexVersion is the on-disk format version.
const IndexVersion = 1

const indexFileName = "mediapack.index.json"

// State is one committed checkpoint.
type State struct {
	Version        int    `json:"version"`
	InputPath      string `json:"input_path"`
	InputSize      int64  `json:"input_size"`
	InputSHA256    string `json:"input_sha256"`
	CommittedBytes int64  `json:"committed_bytes"` // last committed input offset
	BlockCount     int    `json:"block_count"`
	// BatchSize is the fixed number of blocks between commits.
	BatchSize int `json:"batch_size"`
	// PayloadSHA256 covers the committed blocks' per-block hashes in order.
	PayloadSHA256 string `json:"payload_sha256"`
	// SelfSHA256 authenticates every other field; a mismatch means the
	// index is corrupt and resume must be refused.
	SelfSHA256 string `json:"self_sha256"`
}

// Store is the directory-backed local index.
type Store struct {
	dir       string
	batch     int
	path      string
	input     string
	inputSum  [32]byte
	inputSize int64 // -1 when unknown; then size is derived via stat
	// running accumulator over per-block payload hashes.
	root hash.Hash
}

// OpenStore prepares an index directory. It does not read any existing state;
// call Load separately so a fresh run and a resume share one code path.
func OpenStore(dir string, batchSize int, inputPath string, inputSum [32]byte) (*Store, error) {
	return OpenStoreWithSize(dir, batchSize, inputPath, inputSum, -1)
}

// OpenStoreWithSize is OpenStore with an explicit input size, used when the
// caller holds the bytes in memory rather than a guaranteed on-disk file.
func OpenStoreWithSize(dir string, batchSize int, inputPath string,
	inputSum [32]byte, inputSize int64) (*Store, error) {
	if batchSize <= 0 {
		return nil, fault.New(fault.KindCheckpointCorrupt, -1,
			"batch size must be positive, got %d", batchSize)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fault.New(fault.KindIO, -1, "mkdir index: %v", err)
	}
	return &Store{
		dir:       dir,
		batch:     batchSize,
		path:      filepath.Join(dir, indexFileName),
		input:     inputPath,
		inputSum:  inputSum,
		inputSize: inputSize,
		root:      sha256.New(),
	}, nil
}

// Load reads and validates an existing checkpoint. A missing index is not an
// error: the returned state is nil and the caller starts from zero. Corrupt
// content or an input mismatch returns a recoverable fault whose offset names
// the checkpoint byte position.
func (s *Store) Load() (*State, error) {
	raw, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fault.New(fault.KindIO, -1, "read index: %v", err)
	}
	var st State
	if jerr := json.Unmarshal(raw, &st); jerr != nil {
		return nil, fault.New(fault.KindCheckpointCorrupt, int64(len(raw)),
			"index is not valid JSON: %v", jerr).AsRecoverable()
	}
	// Authenticate: recompute over all fields except the self-checksum.
	want := st.SelfSHA256
	st.SelfSHA256 = ""
	canonical, _ := json.Marshal(st)
	got := sha256.Sum256(canonical)
	if want != hex.EncodeToString(got[:]) {
		// Name the offset where integrity failed: end of the record.
		return nil, fault.New(fault.KindCheckpointCorrupt, int64(len(raw)),
			"checkpoint self-checksum mismatch at checkpoint offset %d",
			st.CommittedBytes).AsRecoverable()
	}
	if st.Version != IndexVersion {
		return nil, fault.New(fault.KindCheckpointCorrupt, 0,
			"unsupported index version %d", st.Version).AsRecoverable()
	}
	if st.InputSize < 0 || st.BlockCount < 0 || st.CommittedBytes < 0 {
		return nil, fault.New(fault.KindCheckpointCorrupt, st.CommittedBytes,
			"checkpoint contains negative counters").AsRecoverable()
	}
	// Reject resume when the input changed.
	if st.InputSize != s.expectedSize() || st.InputSHA256 != hex.EncodeToString(s.inputSum[:]) {
		return nil, fault.New(fault.KindInputChanged, st.CommittedBytes,
			"input changed since checkpoint (stored size=%d sha=%s)",
			st.InputSize, st.InputSHA256).AsRecoverable()
	}
	return &st, nil
}

// Commit atomically writes a checkpoint after blocksSinceCommit blocks have
// been processed. payloadHashes are the per-block SHA-256 values in order;
// they extend the running checksum.
func (s *Store) Commit(committedBytes int64, totalBlocks int,
	payloadHashes [][32]byte) (*State, error) {
	for _, h := range payloadHashes {
		if _, err := s.root.Write(h[:]); err != nil {
			return nil, fault.New(fault.KindIO, -1, "checksum: %v", err)
		}
	}
	st := &State{
		Version:        IndexVersion,
		InputPath:      s.input,
		InputSize:      s.expectedSize(),
		InputSHA256:    hex.EncodeToString(s.inputSum[:]),
		CommittedBytes: committedBytes,
		BlockCount:     totalBlocks,
		BatchSize:      s.batch,
		PayloadSHA256:  hex.EncodeToString(s.root.Sum(nil)),
	}
	st.SelfSHA256 = s.selfChecksum(st)
	if err := s.atomicWrite(st); err != nil {
		return nil, err
	}
	return st, nil
}

// BatchSize exposes the configured commit interval.
func (s *Store) BatchSize() int { return s.batch }

// Path returns the index file location, used in error messages.
func (s *Store) Path() string { return s.path }

func (s *Store) expectedSize() int64 {
	if s.inputSize >= 0 {
		return s.inputSize
	}
	info, err := os.Stat(s.input)
	if err != nil {
		return -1
	}
	return info.Size()
}

func (s *Store) selfChecksum(st *State) string {
	cp := *st
	cp.SelfSHA256 = ""
	raw, _ := json.Marshal(cp)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (s *Store) atomicWrite(st *State) error {
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fault.New(fault.KindCheckpointCorrupt, -1, "marshal: %v", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return fault.New(fault.KindIO, -1, "write index: %v", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fault.New(fault.KindIO, -1, "rename index: %v", err)
	}
	return nil
}

// FileSHA256 computes the identity digest of an input file.
func FileSHA256(path string) ([32]byte, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return [32]byte{}, 0, fault.New(fault.KindIO, -1, "open input: %v", err)
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return [32]byte{}, 0, fault.New(fault.KindIO, -1, "hash input: %v", err)
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum, n, nil
}

// String renders a compact state line for terminal output.
func (st *State) String() string {
	return fmt.Sprintf("checkpoint{blocks=%d offset=%d batch=%d sha=%s}",
		st.BlockCount, st.CommittedBytes, st.BatchSize, st.PayloadSHA256[:12])
}
