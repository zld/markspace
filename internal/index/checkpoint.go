// Package index maintains local, crash-recoverable chunk checkpoints.
package index

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"mediaflow/internal/fault"
	"mediaflow/internal/safepath"
)

const (
	StatusPending  = "pending"
	StatusWriting  = "writing"
	StatusComplete = "complete"
	SchemaVersion  = 1
)

// ChunkRecord binds one output chunk to its source/config range and checksum.
type ChunkRecord struct {
	Index       int    `json:"index"`
	StartSample int    `json:"start_sample"`
	EndSample   int    `json:"end_sample"` // exclusive
	ByteLength  int64  `json:"byte_length"`
	SHA256      string `json:"sha256"`
	Status      string `json:"status"`
	Attempt     int    `json:"attempt"`
}

// Index is the durable checkpoint for one batch key and item.
type Index struct {
	SchemaVersion int           `json:"schema_version"`
	BatchKey      string        `json:"batch_key"`
	ItemID        string        `json:"item_id"`
	InputDigest   string        `json:"input_digest"`
	ConfigDigest  string        `json:"config_digest"`
	ChunkCount    int           `json:"chunk_count"`
	Chunks        []ChunkRecord `json:"chunks"`
	Finalized     bool          `json:"finalized"`
	OutputDigest  string        `json:"output_digest,omitempty"`
	path          string
}

// PathFor returns a collision-resistant checkpoint path confined under root.
func PathFor(workDir, batchKey, itemID string) (string, error) {
	h := sha256.Sum256([]byte(batchKey + "\x00" + itemID))
	name := hex.EncodeToString(h[:16]) + ".json"
	return safepath.Resolve(workDir, "checkpoints", name)
}

// Load reads a checkpoint. A missing checkpoint returns nil, nil.
func Load(path string) (*Index, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fault.Wrap(fault.ClassIO, err, "read checkpoint %s", filepath.Base(path))
	}
	var idx Index
	if err := json.Unmarshal(b, &idx); err != nil {
		return nil, fault.Wrap(fault.ClassCheckpoint, err, "parse checkpoint")
	}
	idx.path = path
	return &idx, nil
}

// New creates an in-memory checkpoint with all chunks pending.
func New(path, batchKey, itemID, inputDigest, configDigest string, ranges [][2]int) *Index {
	chunks := make([]ChunkRecord, len(ranges))
	for i, r := range ranges {
		chunks[i] = ChunkRecord{
			Index: i, StartSample: r[0], EndSample: r[1], Status: StatusPending,
		}
	}
	return &Index{
		SchemaVersion: SchemaVersion, BatchKey: batchKey, ItemID: itemID,
		InputDigest: inputDigest, ConfigDigest: configDigest,
		ChunkCount: len(chunks), Chunks: chunks, path: path,
	}
}

// Save atomically persists the checkpoint.
func (x *Index) Save() error {
	if x.path == "" {
		return fault.New(fault.ClassCheckpoint, "checkpoint path is not set")
	}
	if err := os.MkdirAll(filepath.Dir(x.path), 0o755); err != nil {
		return fault.Wrap(fault.ClassIO, err, "create checkpoint directory")
	}
	b, err := json.MarshalIndent(x, "", "  ")
	if err != nil {
		return fault.Wrap(fault.ClassCheckpoint, err, "encode checkpoint")
	}
	tmp := x.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return fault.Wrap(fault.ClassIO, err, "write checkpoint temp file")
	}
	if err := os.Rename(tmp, x.path); err != nil {
		return fault.Wrap(fault.ClassIO, err, "commit checkpoint")
	}
	return nil
}

// CompatibilityError explains why an old checkpoint cannot be reused.
type CompatibilityError struct {
	Reasons []string
}

func (e *CompatibilityError) Error() string { return "checkpoint compatibility conflict" }

// CheckCompatibility refuses reuse when identity-bearing fields changed.
func (x *Index) CheckCompatibility(batchKey, inputDigest, configDigest string) error {
	var reasons []string
	if x.BatchKey != batchKey {
		reasons = append(reasons, "batch_key changed")
	}
	if x.InputDigest != inputDigest {
		reasons = append(reasons, "input digest changed: old="+short(x.InputDigest)+" new="+short(inputDigest))
	}
	if x.ConfigDigest != configDigest {
		reasons = append(reasons, "config digest changed: old="+short(x.ConfigDigest)+" new="+short(configDigest))
	}
	if len(reasons) > 0 {
		return &CompatibilityError{Reasons: reasons}
	}
	return nil
}

// RollbackIncomplete resets writing chunks after a crash and returns their indexes.
func (x *Index) RollbackIncomplete() []int {
	var rolled []int
	for i := range x.Chunks {
		if x.Chunks[i].Status == StatusWriting {
			x.Chunks[i].Status = StatusPending
			x.Chunks[i].SHA256 = ""
			x.Chunks[i].ByteLength = 0
			rolled = append(rolled, i)
		}
	}
	x.Finalized = false
	x.OutputDigest = ""
	return rolled
}

// Begin marks a chunk as writing and increments its attempt counter.
func (x *Index) Begin(index int) error {
	if index < 0 || index >= len(x.Chunks) {
		return fault.New(fault.ClassCheckpoint, "chunk index %d out of range", index)
	}
	x.Chunks[index].Status = StatusWriting
	x.Chunks[index].Attempt++
	x.Finalized = false
	x.OutputDigest = ""
	return x.Save()
}

// MarkComplete records a verified chunk.
func (x *Index) MarkComplete(rec ChunkRecord) error {
	if rec.Index < 0 || rec.Index >= len(x.Chunks) {
		return fault.New(fault.ClassCheckpoint, "chunk index %d out of range", rec.Index)
	}
	rec.Status = StatusComplete
	x.Chunks[rec.Index] = rec
	return x.Save()
}

// CompleteCount returns verified-complete chunk count.
func (x *Index) CompleteCount() int {
	n := 0
	for _, c := range x.Chunks {
		if c.Status == StatusComplete {
			n++
		}
	}
	return n
}

// AllComplete reports whether every chunk is complete.
func (x *Index) AllComplete() bool { return x.CompleteCount() == len(x.Chunks) }

// SetFinalOutput records the final assembled output digest after verification.
func (x *Index) SetFinalOutput(digest string) error {
	x.Finalized = true
	x.OutputDigest = digest
	return x.Save()
}

func short(s string) string {
	if len(s) <= 18 {
		return s
	}
	return s[:18] + "…"
}

// DigestBytes returns the stable sha256: prefix digest of b.
func DigestBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}
