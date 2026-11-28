package index

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ChunkResult is the durable outcome of processing one fixed-size work chunk.
// Summary is the chunk's contribution to the manifest digest; it is computed
// from published content only, so a resumed run reproduces it byte for byte.
type ChunkResult struct {
	Seq     int    `json:"seq"`
	Summary string `json:"summary"`
	// Segments counts segments published by this chunk.
	Segments int `json:"segments"`
	// Unrecoverable counts packets dropped beyond the loss-recovery window.
	Unrecoverable int `json:"unrecoverable"`
}

// CheckpointHeader pins the run to the exact inputs it started with.
type CheckpointHeader struct {
	// Inputs maps an input path to its content fingerprint at commit time.
	Inputs map[string]Fingerprint `json:"inputs"`
	// ChunkSize is recorded so a resumed run cannot silently change the
	// partition and produce a different digest.
	ChunkSize int `json:"chunk_size"`
	// ConfigSum binds the checkpoint to every processing parameter that could
	// change output (loss window, rendition set, thresholds, seed).
	ConfigSum string `json:"config_sum"`
	Created   int64  `json:"created_unix"`
}

// Checkpoint is the persisted progress of an offline job.
type Checkpoint struct {
	Header CheckpointHeader `json:"header"`
	Chunks []ChunkResult    `json:"chunks"`
}

// CheckpointStore persists a checkpoint next to the job.
type CheckpointStore struct {
	path string
}

// NewCheckpointStore returns a store writing to path.
func NewCheckpointStore(path string) *CheckpointStore { return &CheckpointStore{path: path} }

// Path returns the checkpoint file location.
func (s *CheckpointStore) Path() string { return s.path }

// ConfigSum hashes the parameters that affect output. It is deliberately
// order-sensitive over the supplied strings.
func ConfigSum(parts ...string) string {
	h := sha256.New()
	var lenBuf [8]byte
	for _, p := range parts {
		binary.BigEndian.PutUint64(lenBuf[:], uint64(len(p)))
		h.Write(lenBuf[:])
		h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Load reads the checkpoint. A missing file yields an empty checkpoint so a
// first run and a resumed run use one code path.
func (s *CheckpointStore) Load() (*Checkpoint, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return &Checkpoint{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read checkpoint: %w", err)
	}
	var cp Checkpoint
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cp); err != nil {
		return nil, fmt.Errorf("checkpoint %s is corrupt: %w", s.path, err)
	}
	if cp.Header.Inputs == nil {
		cp.Header.Inputs = map[string]Fingerprint{}
	}
	return &cp, nil
}

// Errors returned by Commit when a resumed run no longer matches its inputs.
var (
	ErrChunkSizeChanged = errors.New("chunk size differs from checkpoint")
	ErrConfigChanged    = errors.New("processing configuration differs from checkpoint")
)

// Commit validates the header against the current run and appends result. The
// checkpoint is rewritten atomically, so after a crash the store holds either
// the previous complete checkpoint or the new one, never a torn file.
//
// Inputs are re-fingerprinted by the caller immediately before Commit; if any
// input changed since the run started, Commit refuses to advance. This is the
// guard against committing chunks computed from bytes that are no longer on
// disk.
func (s *CheckpointStore) Commit(header CheckpointHeader, result ChunkResult) (*Checkpoint, error) {
	cp, err := s.Load()
	if err != nil {
		return nil, err
	}
	if len(cp.Chunks) > 0 {
		if cp.Header.ChunkSize != header.ChunkSize {
			return nil, fmt.Errorf("%w: stored %d, current %d", ErrChunkSizeChanged, cp.Header.ChunkSize, header.ChunkSize)
		}
		if cp.Header.ConfigSum != header.ConfigSum {
			return nil, fmt.Errorf("%w: stored %s, current %s", ErrConfigChanged, cp.Header.ConfigSum, header.ConfigSum)
		}
		for path, fp := range cp.Header.Inputs {
			cur, ok := header.Inputs[path]
			if !ok {
				return nil, fmt.Errorf("input %s was present at checkpoint but is missing now", path)
			}
			if !cur.Equal(fp) {
				return nil, fmt.Errorf("input %s changed since checkpoint (content %s -> %s)",
					path, short(fp.Content), short(cur.Content))
			}
		}
	} else {
		cp.Header = header
		cp.Header.Created = time.Now().Unix()
	}
	for _, c := range cp.Chunks {
		if c.Seq == result.Seq {
			if c.Summary == result.Summary && c.Segments == result.Segments {
				return cp, nil // already committed by an earlier attempt
			}
			return nil, fmt.Errorf("chunk %d already committed with a different result", result.Seq)
		}
	}
	cp.Chunks = append(cp.Chunks, result)
	sortChunks(cp.Chunks)
	if err := s.save(cp); err != nil {
		return nil, err
	}
	return cp, nil
}

func sortChunks(c []ChunkResult) {
	for i := 1; i < len(c); i++ {
		for j := i; j > 0 && c[j].Seq < c[j-1].Seq; j-- {
			c[j], c[j-1] = c[j-1], c[j]
		}
	}
}

// Committed returns the set of chunk sequence numbers already durable.
func (cp *Checkpoint) Committed() map[int]ChunkResult {
	out := make(map[int]ChunkResult, len(cp.Chunks))
	for _, c := range cp.Chunks {
		out[c.Seq] = c
	}
	return out
}

// MaxCommitted returns the highest contiguous committed sequence starting at 0,
// or -1 when chunk 0 is not committed. Work after a gap must be recomputed: a
// later chunk may depend on state built by the missing one.
func (cp *Checkpoint) MaxCommitted() int {
	committed := cp.Committed()
	last := -1
	for {
		if _, ok := committed[last+1]; !ok {
			return last
		}
		last++
	}
}

// Digest folds committed chunk summaries into the manifest digest. The result
// depends only on chunk contents and order, never on how many runs produced
// them, which is what makes a resumed manifest match a continuous one.
func (cp *Checkpoint) Digest() string {
	h := sha256.New()
	var seq [8]byte
	for _, c := range cp.Chunks {
		binary.BigEndian.PutUint64(seq[:], uint64(c.Seq))
		h.Write(seq[:])
		h.Write([]byte(c.Summary))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func short(sum string) string {
	if len(sum) > 12 {
		return sum[:12]
	}
	return sum
}

func (s *CheckpointStore) save(cp *Checkpoint) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(s.path, data)
}
