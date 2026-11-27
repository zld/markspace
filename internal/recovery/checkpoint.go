// Package recovery persists per-stage progress so an interrupted archive run
// can continue from the last verified byte boundary. A checkpoint is only
// valid for the exact source digest it was written for: any change to the
// source rejects the old checkpoint instead of producing a mixed result.
package recovery

import (
	"encoding/json"
	"os"
	"sort"

	"archivecheck/internal/asset"
	"archivecheck/internal/fault"
	"archivecheck/internal/pipeline"
)

// Status values for a stage.
const (
	StatusPending = "pending"
	StatusRunning = "running"
	StatusDone    = "done"
	StatusFailed  = "failed"
)

// Version is the checkpoint schema version.
const Version = 1

// StageState is the durable record for one stage.
type StageState struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	// Offset is the number of bytes verified complete; processing may resume
	// here.
	Offset int64 `json:"offset"`
	Chunks int   `json:"chunks"`
	Bytes  int64 `json:"bytes"`
	// Digest is the SHA-256 of the source prefix [0,Offset). It verifies that
	// the boundary still refers to the same bytes.
	Digest string `json:"digest"`
	Detail string `json:"detail,omitempty"`
}

// Checkpoint is the durable state of one run over one source.
type Checkpoint struct {
	Version      int                    `json:"version"`
	Source       string                 `json:"source"`
	SourceDigest string                 `json:"sourceDigest"`
	SourceSize   int64                  `json:"sourceSize"`
	ChunkSize    int                    `json:"chunkSize"`
	QueueSize    int                    `json:"queueSize"`
	Status       string                 `json:"status"`
	Stages       map[string]*StageState `json:"stages"`

	path string
}

// New creates an empty checkpoint for src.
func New(src *asset.Source, cfg pipeline.Config) *Checkpoint {
	return &Checkpoint{
		Version:      Version,
		Source:       src.Name,
		SourceDigest: src.Digest,
		SourceSize:   src.Size,
		ChunkSize:    cfg.ChunkSize,
		QueueSize:    cfg.QueueSize,
		Status:       StatusRunning,
		Stages:       map[string]*StageState{},
	}
}

// Config returns the pipeline shape the checkpoint was written with.
func (c *Checkpoint) Config() pipeline.Config {
	return pipeline.Config{ChunkSize: c.ChunkSize, QueueSize: c.QueueSize}
}

// Verify rejects a checkpoint that does not belong to src, or that was written
// with a different pipeline shape.
func (c *Checkpoint) Verify(src *asset.Source, cfg pipeline.Config) error {
	if c.Version != Version {
		return fault.New(fault.StageRecovery, fault.KindInvalid, src.Name, 0, "checkpoint version %d is not supported", c.Version)
	}
	if c.SourceDigest != src.Digest {
		return fault.New(fault.StageRecovery, fault.KindConflict, src.Name, 0,
			"source digest changed from %s to %s; refusing the old checkpoint", c.SourceDigest, src.Digest)
	}
	if c.SourceSize != src.Size {
		return fault.New(fault.StageRecovery, fault.KindConflict, src.Name, 0,
			"source size changed from %d to %d", c.SourceSize, src.Size)
	}
	if c.ChunkSize != cfg.ChunkSize || c.QueueSize != cfg.QueueSize {
		return fault.New(fault.StageRecovery, fault.KindConflict, src.Name, 0,
			"checkpoint was written with chunk=%d queue=%d, not chunk=%d queue=%d",
			c.ChunkSize, c.QueueSize, cfg.ChunkSize, cfg.QueueSize)
	}
	return nil
}

// Stage returns (creating on demand) the state for a stage name.
func (c *Checkpoint) Stage(name string) *StageState {
	if c.Stages == nil {
		c.Stages = map[string]*StageState{}
	}
	st, ok := c.Stages[name]
	if !ok {
		st = &StageState{Name: name, Status: StatusPending}
		c.Stages[name] = st
	}
	return st
}

// ResumeOffset is the lowest verified boundary across the given stages, or 0
// when nothing has been verified yet.
func (c *Checkpoint) ResumeOffset(stages ...string) int64 {
	if len(stages) == 0 {
		return 0
	}
	var lowest int64 = -1
	for _, name := range stages {
		off := c.Stage(name).Offset
		if lowest < 0 || off < lowest {
			lowest = off
		}
	}
	if lowest < 0 {
		return 0
	}
	return lowest
}

// Complete reports whether every stage finished.
func (c *Checkpoint) Complete(stages ...string) bool {
	for _, name := range stages {
		if c.Stage(name).Status != StatusDone {
			return false
		}
	}
	return true
}

// diskCheckpoint mirrors Checkpoint on disk, with stages stored as a sorted
// array so the file is byte-stable across runs.
type diskCheckpoint struct {
	Version      int           `json:"version"`
	Source       string        `json:"source"`
	SourceDigest string        `json:"sourceDigest"`
	SourceSize   int64         `json:"sourceSize"`
	ChunkSize    int           `json:"chunkSize"`
	QueueSize    int           `json:"queueSize"`
	Status       string        `json:"status"`
	Stages       []*StageState `json:"stages"`
}

// Load reads a checkpoint file. A missing file is reported as a normal "no
// checkpoint" answer rather than an error.
func Load(path string) (*Checkpoint, bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fault.Wrap(err, fault.StageRecovery, fault.KindMissing, path, 0, "read checkpoint")
	}
	var d diskCheckpoint
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, false, fault.Wrap(err, fault.StageRecovery, fault.KindCorrupt, path, 0, "decode checkpoint")
	}
	cp := &Checkpoint{
		Version: d.Version, Source: d.Source, SourceDigest: d.SourceDigest,
		SourceSize: d.SourceSize, ChunkSize: d.ChunkSize, QueueSize: d.QueueSize, Status: d.Status,
		Stages: map[string]*StageState{},
	}
	for _, st := range d.Stages {
		if st.Name == "" {
			return nil, false, fault.New(fault.StageRecovery, fault.KindCorrupt, path, 0, "checkpoint entry without a stage name")
		}
		cp.Stages[st.Name] = st
	}
	cp.path = path
	return cp, true, nil
}

// Save writes the checkpoint atomically. The file is fsynced and renamed into
// place so an interrupted write cannot leave a half checkpoint behind.
func (c *Checkpoint) Save(path string) (string, error) {
	data, err := c.Marshal()
	if err != nil {
		return "", err
	}
	sum, err := asset.WriteAtomic(path, data)
	if err != nil {
		return "", fault.Wrap(err, fault.StageRecovery, fault.KindCorrupt, path, 0, "write checkpoint")
	}
	c.path = path
	return sum, nil
}

// Marshal renders the checkpoint in a stable, sorted form.
func (c *Checkpoint) Marshal() ([]byte, error) {
	d := diskCheckpoint{
		Version: c.Version, Source: c.Source, SourceDigest: c.SourceDigest,
		SourceSize: c.SourceSize, ChunkSize: c.ChunkSize, QueueSize: c.QueueSize, Status: c.Status,
		Stages: []*StageState{},
	}
	for _, name := range sortedNames(c.Stages) {
		d.Stages = append(d.Stages, c.Stages[name])
	}
	return json.MarshalIndent(d, "", "  ")
}

func sortedNames(m map[string]*StageState) []string {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
