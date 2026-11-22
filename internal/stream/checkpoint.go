package stream

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"hash"
	"os"
	"path/filepath"

	"mediacore/internal/clock"
)

// CheckpointVersion is bumped on layout changes.
const CheckpointVersion uint32 = 1

// Checkpoint is the resumable state persisted atomically after every batch.
type Checkpoint struct {
	Version       uint32 `json:"version"`
	InputDigest   string `json:"input_digest"`
	NextSample    uint32 `json:"next_sample"`
	SamplesDone   uint64 `json:"samples_done"`
	BytesRead     int64  `json:"bytes_read"`
	LogicalBytes  int64  `json:"logical_bytes"`
	BatchesDone   int    `json:"batches_done"`
	OutputDigest  string `json:"output_digest"`
}

// LoadCheckpoint reads a checkpoint; a missing file yields an empty
// checkpoint at sample zero rather than an error.
func LoadCheckpoint(path, inputDigest string) (*Checkpoint, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &Checkpoint{Version: CheckpointVersion, InputDigest: inputDigest}, nil
	}
	if err != nil {
		return nil, clock.ErrRecoverablef("stream.checkpoint", "read: %v", err)
	}
	var cp Checkpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return nil, clock.ErrRecoverablef("stream.checkpoint", "corrupt json: %v", err)
	}
	if cp.Version != CheckpointVersion || cp.InputDigest != inputDigest {
		return nil, clock.ErrRecoverablef("stream.checkpoint", "stale checkpoint (version/digest mismatch)")
	}
	return &cp, nil
}

// Save writes the checkpoint to a temp file and renames it into place.
func (c *Checkpoint) Save(path string) error {
	c.Version = CheckpointVersion
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return clock.ErrInvariantf("stream.checkpoint", "marshal: %v", err)
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".cp-*")
	if err != nil {
		return clock.ErrRecoverablef("stream.checkpoint", "temp: %v", err)
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return clock.ErrRecoverablef("stream.checkpoint", "write: %v", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return clock.ErrRecoverablef("stream.checkpoint", "close: %v", err)
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return clock.ErrRecoverablef("stream.checkpoint", "rename: %v", err)
	}
	return nil
}

// outputHash keeps the running digest of the canonical output record stream.
type outputHash struct {
	h   hash.Hash
	buf [32]byte
}

func newOutputHash() *outputHash {
	return &outputHash{h: sha256.New()}
}

func (o *outputHash) Write(r RecordView) {
	binary.BigEndian.PutUint32(o.buf[0:4], r.SampleIndex)
	binary.BigEndian.PutUint64(o.buf[4:12], r.Offset)
	binary.BigEndian.PutUint32(o.buf[12:16], r.Size)
	binary.BigEndian.PutUint32(o.buf[16:20], r.WireSize)
	binary.BigEndian.PutUint64(o.buf[20:28], uint64(r.DTSNanos))
	var flag byte
	if r.Sync {
		flag = 1
	}
	o.buf[28] = flag
	// bytes 29..32 reserved zero
	o.h.Write(o.buf[:])
}

func (o *outputHash) Digest() string {
	return hex.EncodeToString(o.h.Sum(nil))
}
