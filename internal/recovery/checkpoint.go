// Package recovery persists per-stage progress so an interrupted archive run
// can continue from the last verified byte boundary. A checkpoint is only
// valid for the exact source digest it was written for: any change to the
// source rejects the old checkpoint instead of producing a mixed result.
package recovery

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"os"
	"sort"
	"sync"

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

// Session coordinates a checkpoint with an in-memory source. Every commit
// extends the stage's running prefix digest, and reopening a session re-hashes
// each stage's committed prefix to prove the boundary is still valid.
type Session struct {
	mu      sync.Mutex
	cp      *Checkpoint
	data    []byte
	hashers map[string]hash.Hash
}

// Open verifies the checkpoint against src and cfg, then rebuilds each
// stage's digest state by re-hashing its committed prefix. A digest mismatch
// means the bytes before the boundary changed, which is treated exactly like a
// changed source digest: the checkpoint is rejected.
func Open(src *asset.Source, cp *Checkpoint, cfg pipeline.Config, stages ...string) (*Session, error) {
	if cp == nil {
		return nil, fault.New(fault.StageRecovery, fault.KindInvalid, src.Name, 0, "nil checkpoint")
	}
	if err := cp.Verify(src, cfg); err != nil {
		return nil, err
	}
	s := &Session{cp: cp, data: src.Bytes(), hashers: map[string]hash.Hash{}}
	for _, name := range stages {
		st := cp.Stage(name)
		h := sha256.New()
		if st.Offset > 0 {
			if st.Offset > int64(len(s.data)) {
				return nil, fault.New(fault.StageRecovery, fault.KindConflict, src.Name, st.Offset,
					"stage %q resumes at %d, past the %d byte source", name, st.Offset, len(s.data))
			}
			h.Write(s.data[:st.Offset])
			if st.Digest != "" && hex.EncodeToString(h.Sum(nil)) != st.Digest {
				return nil, fault.New(fault.StageRecovery, fault.KindConflict, src.Name, st.Offset,
					"stage %q prefix digest does not match its boundary", name)
			}
		}
		s.hashers[name] = h
	}
	return s, nil
}

// Boundary returns a stage's resume offset.
func (s *Session) Boundary(stage string) int64 { return s.cp.Stage(stage).Offset }

// MarkRunning records that a stage started (or restarted) processing.
func (s *Session) MarkRunning(stage string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.cp.Stage(stage)
	st.Status = StatusRunning
}

// Commit advances a stage to endOffset, hashing the newly completed bytes.
// Commits must be strictly increasing; a regression is rejected rather than
// silently accepted.
func (s *Session) Commit(stage string, endOffset int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.cp.Stage(stage)
	if endOffset < st.Offset {
		return fault.New(fault.StageRecovery, fault.KindInvalid, s.cp.Source, endOffset,
			"stage %q commit regressed from %d to %d", stage, st.Offset, endOffset)
	}
	if endOffset > int64(len(s.data)) {
		return fault.New(fault.StageRecovery, fault.KindBoundary, s.cp.Source, endOffset,
			"stage %q commit at %d is past the %d byte source", stage, endOffset, len(s.data))
	}
	h := s.hashers[stage]
	if h == nil {
		h = sha256.New()
		h.Write(s.data[:st.Offset])
		s.hashers[stage] = h
	}
	advanced := endOffset - st.Offset
	h.Write(s.data[st.Offset:endOffset])
	st.Offset = endOffset
	st.Bytes += advanced
	st.Chunks++
	st.Digest = hex.EncodeToString(h.Sum(nil))
	return nil
}

// CommitChunk is a convenience wrapper for a pipeline chunk.
func (s *Session) CommitChunk(stage string, c pipeline.Chunk) error {
	return s.Commit(stage, c.Offset+int64(len(c.Data)))
}

// Finish marks a stage complete.
func (s *Session) Finish(stage string, detail string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.cp.Stage(stage)
	st.Status = StatusDone
	st.Detail = detail
}

// Fail marks a stage failed with a reason.
func (s *Session) Fail(stage string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.cp.Stage(stage)
	st.Status = StatusFailed
	if err != nil {
		fe := fault.As(err)
		if fe != nil {
			st.Offset = fe.Offset
			st.Detail = fe.Error()
		} else {
			st.Detail = err.Error()
		}
	}
}

// Checkpoint returns the underlying checkpoint for saving.
func (s *Session) Checkpoint() *Checkpoint { return s.cp }

// Snapshot renders a sorted, human-readable stage table.
func (s *Session) Snapshot() []StageState {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]StageState, 0, len(s.cp.Stages))
	for _, name := range sortedNames(s.cp.Stages) {
		out = append(out, *s.cp.Stages[name])
	}
	return out
}

// ResumePlan reports the lowest boundary and each stage's own offset, which
// together describe where a restart begins.
func (s *Session) ResumePlan(stages ...string) (int64, map[string]int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	per := make(map[string]int64, len(stages))
	for _, name := range stages {
		per[name] = s.cp.Stage(name).Offset
	}
	return s.cp.ResumeOffset(stages...), per
}

// String renders a checkpoint summary for logs.
func (c *Checkpoint) String() string {
	return fmt.Sprintf("checkpoint(source=%s digest=%s status=%s stages=%d)", c.Source, short(c.SourceDigest), c.Status, len(c.Stages))
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
