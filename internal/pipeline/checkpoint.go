package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"

	"mediarun/internal/fault"
)

// Checkpoint records the last stage that completed with a consistent output.
// It is written atomically after each stage so an interrupted run resumes at
// the next stage instead of repeating work or duplicating writes.
type Checkpoint struct {
	Version    int               `json:"version"`
	ConfigHash string            `json:"config_hash"`
	InputHash  string            `json:"input_hash"`
	Stage      string            `json:"stage"`
	Done       []string          `json:"done"`
	Hashes     map[string]string `json:"hashes"`
	// Invalidated is set in memory when a checkpoint existed but could not be
	// trusted. It is never serialized; it only drives the resume report.
	Invalidated bool `json:"-"`
	// Exists reports whether a checkpoint file was present.
	Exists bool `json:"-"`
}

const checkpointVersion = 1

// checkpointName is the file written under the run directory.
const checkpointName = "checkpoint.json"

// ManifestName is the manifest file name.
const ManifestName = "manifest.json"

// SegmentsDir holds remuxed MP4 files.
const SegmentsDir = "segments"

// IndexDir holds per-segment keyframe indexes.
const IndexDir = "index"

// stageOrder is the resumption order. A later stage may only run when every
// earlier stage is recorded as done.
var stageOrder = []string{
	"parse",
	"jitter", // jitter ordering and loss recovery share one pass
	"recovery",
	"sync",
	"mux",
	"index",
	"manifest",
}

func stageIndex(name string) int {
	for i, s := range stageOrder {
		if s == name {
			return i
		}
	}
	return -1
}

// LoadCheckpoint returns the checkpoint at dir, or an empty one when absent.
// A checkpoint only applies when both the configuration and the validated
// input digests match; either difference invalidates every stage, because a
// stale checkpoint would otherwise reuse outputs built from other bytes.
func LoadCheckpoint(dir, configHash, inputHash string) (*Checkpoint, bool) {
	path := filepath.Join(dir, checkpointName)
	data, err := os.ReadFile(path)
	if err != nil {
		return newCheckpoint(configHash, inputHash), false
	}
	var cp Checkpoint
	if err := json.Unmarshal(data, &cp); err != nil || cp.Version != checkpointVersion {
		fresh := newCheckpoint(configHash, inputHash)
		fresh.Invalidated = true
		return fresh, false
	}
	cp.Exists = true
	if cp.ConfigHash != configHash || cp.InputHash != inputHash {
		fresh := newCheckpoint(configHash, inputHash)
		fresh.Invalidated = true
		return fresh, false
	}
	if cp.Hashes == nil {
		cp.Hashes = map[string]string{}
	}
	return &cp, true
}

func newCheckpoint(configHash, inputHash string) *Checkpoint {
	return &Checkpoint{
		Version:    checkpointVersion,
		ConfigHash: configHash,
		InputHash:  inputHash,
		Hashes:     map[string]string{},
	}
}

// Mark records a completed stage and its output digest, then persists the
// checkpoint atomically.
func (c *Checkpoint) Mark(dir, stage, outputHash string) error {
	if stageIndex(stage) < 0 {
		return fault.New(fault.StagePipeline, fault.KindConfig, "unknown stage %q", stage)
	}
	present := false
	for _, s := range c.Done {
		if s == stage {
			present = true
			break
		}
	}
	if !present {
		c.Done = append(c.Done, stage)
	}
	c.Stage = stage
	c.Hashes[stage] = outputHash
	return c.write(dir)
}

// SegmentHash returns the recorded output digest for one segment, if any.
func (c *Checkpoint) SegmentHash(index int) (string, bool) {
	h, ok := c.Hashes[segmentKey(index)]
	return h, ok
}

// RecordSegment stores a verified segment digest before the next checkpoint
// write, so a later run can prove a cached file is the one it produced.
func (c *Checkpoint) RecordSegment(index int, digest string) {
	if c.Hashes == nil {
		c.Hashes = map[string]string{}
	}
	c.Hashes[segmentKey(index)] = digest
}

func segmentKey(index int) string { return "segment#" + itoa(index) }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// IsDone reports whether the stage completed under the current config.
func (c *Checkpoint) IsDone(stage string) bool {
	for _, s := range c.Done {
		if s == stage {
			return true
		}
	}
	return false
}

// ResumeStage returns the first stage in order that is not done.
func (c *Checkpoint) ResumeStage() string {
	for _, s := range stageOrder {
		if !c.IsDone(s) {
			return s
		}
	}
	return "complete"
}

func (c *Checkpoint) write(dir string) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fault.Wrap(fault.StagePipeline, fault.KindIO, err, "marshal checkpoint")
	}
	data = append(data, '\n')
	return atomicWrite(filepath.Join(dir, checkpointName), data)
}

func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fault.Wrap(fault.StagePipeline, fault.KindIO, err, "mkdir %s", dir)
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fault.Wrap(fault.StagePipeline, fault.KindIO, err, "temp in %s", dir)
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return fault.Wrap(fault.StagePipeline, fault.KindIO, err, "write %s", name)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(name)
		return fault.Wrap(fault.StagePipeline, fault.KindIO, err, "sync %s", name)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return fault.Wrap(fault.StagePipeline, fault.KindIO, err, "close %s", name)
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return fault.Wrap(fault.StagePipeline, fault.KindIO, err, "rename %s", path)
	}
	return nil
}

// stageHash folds a stage's inputs and config into a single digest. It is the
// key for idempotent retries: identical inputs and config reproduce the hash
// and therefore the outputs.
func stageHash(stage string, configHash string, inputs ...string) string {
	h := sha256.New()
	io.WriteString(h, stage)
	h.Write([]byte{0})
	io.WriteString(h, configHash)
	for _, in := range inputs {
		h.Write([]byte{0})
		if len(in) == 64 {
			if _, err := hex.DecodeString(in); err == nil {
				h.Write([]byte{1})
				h.Write([]byte(in))
				continue
			}
		}
		io.WriteString(h, in)
	}
	return hex.EncodeToString(h.Sum(nil))
}
