package recovery

import (
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"sync"

	"archivecheck/internal/asset"
	"archivecheck/internal/fault"
	"archivecheck/internal/pipeline"
)

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

// Resume rewinds every named stage to the lowest verified boundary across
// them and returns that boundary. A stage that ran ahead of a slower peer has
// verified bytes past the restart point, but those bytes cannot be kept: the
// slower stage must be able to redo its work from a consistent prefix. The
// rewound stages are marked running.
func (s *Session) Resume(stages ...string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(stages) == 0 {
		return 0, fault.New(fault.StageRecovery, fault.KindInvalid, s.cp.Source, 0, "resume needs at least one stage")
	}
	lowest := s.cp.ResumeOffset(stages...)
	for _, name := range stages {
		st := s.cp.Stage(name)
		if st.Offset == lowest {
			st.Status = StatusRunning
			continue
		}
		h := sha256.New()
		h.Write(s.data[:lowest])
		s.hashers[name] = h
		st.Offset = lowest
		st.Bytes = lowest
		st.Digest = hex.EncodeToString(h.Sum(nil))
		st.Status = StatusRunning
	}
	return lowest, nil
}

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
