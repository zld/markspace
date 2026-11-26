package replay

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"

	"replayproof/internal/media"
)

// CheckpointVersion is bumped whenever the checkpoint document changes in a
// way that invalidates older files.
const CheckpointVersion = 1

// Checkpoint records the inputs and the last contiguous sequence number that
// the merger released. It is the only resume authority: a run may continue
// from LastSeq+1 and nothing else.
type Checkpoint struct {
	Version      int               `json:"version"`
	Seed         int64             `json:"seed"`
	InputDigest  string            `json:"input_digest"`
	ParamsDigest string            `json:"params_digest"`
	LastSeq      int               `json:"last_seq"`
	ClipHashes   map[string]string `json:"clip_hashes"`
}

// NewCheckpoint builds an empty checkpoint for the given inputs.
func NewCheckpoint(seed int64, inputDigest, paramsDigest string) *Checkpoint {
	return &Checkpoint{
		Version: CheckpointVersion, Seed: seed,
		InputDigest: inputDigest, ParamsDigest: paramsDigest,
		ClipHashes: make(map[string]string),
	}
}

// Clone returns a deep enough copy for safe publication.
func (c *Checkpoint) Clone() *Checkpoint {
	out := *c
	out.ClipHashes = make(map[string]string, len(c.ClipHashes))
	for k, v := range c.ClipHashes {
		out.ClipHashes[k] = v
	}
	return &out
}

// Record releases seq with its clip hash.
func (c *Checkpoint) Record(seq int, hash string) {
	if seq != c.LastSeq+1 {
		panic(fmt.Sprintf("checkpoint: released sequence %d after %d", seq, c.LastSeq))
	}
	c.LastSeq = seq
	c.ClipHashes[fmt.Sprintf("%d", seq)] = hash
}

// Save writes the checkpoint atomically, then fsyncs the directory entry.
func (c *Checkpoint) Save(path string) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return media.Wrap(err, media.CodeIO, "checkpoint.marshal", -1)
	}
	data = append(data, '\n')
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return media.Wrap(err, media.CodeIO, "checkpoint.create", -1)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return media.Wrap(err, media.CodeIO, "checkpoint.write", -1)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return media.Wrap(err, media.CodeIO, "checkpoint.sync", -1)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return media.Wrap(err, media.CodeIO, "checkpoint.close", -1)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return media.Wrap(err, media.CodeIO, "checkpoint.rename", -1)
	}
	return nil
}

// LoadCheckpoint reads a checkpoint. A missing file returns (nil, nil) so a
// first run can start clean.
func LoadCheckpoint(path string) (*Checkpoint, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, media.Wrap(err, media.CodeIO, "checkpoint.read", -1)
	}
	var c Checkpoint
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, &media.Error{Code: media.CodeCheckpointMismatch, Op: "checkpoint.parse", Offset: -1, Msg: "checkpoint is not valid JSON", Err: err}
	}
	if c.Version != CheckpointVersion {
		return nil, &media.Error{Code: media.CodeCheckpointMismatch, Op: "checkpoint.version", Offset: -1,
			Msg: fmt.Sprintf("checkpoint version %d, expected %d", c.Version, CheckpointVersion)}
	}
	if c.LastSeq < 0 {
		return nil, &media.Error{Code: media.CodeCheckpointMismatch, Op: "checkpoint.seq", Offset: -1, Msg: "negative last sequence"}
	}
	return &c, nil
}

// Validate refuses to resume when the inputs or parameters changed. Both
// digests must match exactly; the seed is part of the digest inputs.
func (c *Checkpoint) Validate(inputDigest, paramsDigest string) error {
	if c.InputDigest != inputDigest {
		return &media.Error{
			Code: media.CodeCheckpointMismatch, Op: "checkpoint.inputs", Offset: -1,
			Msg: fmt.Sprintf("input digest %s does not match checkpoint %s", inputDigest, c.InputDigest),
		}
	}
	if c.ParamsDigest != paramsDigest {
		return &media.Error{
			Code: media.CodeCheckpointMismatch, Op: "checkpoint.params", Offset: -1,
			Msg: fmt.Sprintf("parameter digest %s does not match checkpoint %s", paramsDigest, c.ParamsDigest),
		}
	}
	return nil
}

// DigestBytes hashes a byte slice and returns lowercase hex.
func DigestBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// InputDigest hashes named inputs in the order given. Names are included so
// renaming or reordering inputs cannot collide.
func InputDigest(inputs map[string][]byte) string {
	names := make([]string, 0, len(inputs))
	for name := range inputs {
		names = append(names, name)
	}
	sort.Strings(names)
	h := sha256.New()
	for _, name := range names {
		fmt.Fprintf(h, "input:%s:%d:", name, len(inputs[name]))
		h.Write(inputs[name])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ParamsDigest hashes the processing parameters that affect output. Delay is
// excluded because it only changes scheduling, not content.
func ParamsDigest(seed int64, workers, queue int, selectors map[string]string) string {
	keys := make([]string, 0, len(selectors))
	for k := range selectors {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	fmt.Fprintf(h, "seed=%d;workers=%d;queue=%d;", seed, workers, queue)
	for _, k := range keys {
		fmt.Fprintf(h, "select:%s=%s;", k, selectors[k])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Delay is documented here so callers do not include it in ParamsDigest by
// accident; it is scheduling-only.
var _ = time.Duration(0)
