package pipeline

import (
	"encoding/json"
	"os"
	"sync"
)

// SegmentRecord is one fully committed output segment.
type SegmentRecord struct {
	Index        int      `json:"index"`
	SourceRange  [2]int64 `json:"source_range"` // input byte span
	InputDigest  string   `json:"input_digest"`
	OutputFile   string   `json:"output_file"`
	OutputDigest string   `json:"output_digest"`
	CacheHash    string   `json:"cache_hash"`
	DurationMS   int64    `json:"duration_ms"`
}

// Checkpoint is the crash-safe, idempotent resume state. A segment is added
// only after its artifact and digest are durably written; the manifest itself
// is replaced atomically, so an interrupted commit leaves the previous state.
type Checkpoint struct {
	JobID       string          `json:"job_id"`
	Params      string          `json:"params"`
	InputDigest string          `json:"input_digest"`
	Segments    []SegmentRecord `json:"segments"`
	FinalDigest string          `json:"final_digest,omitempty"`
	Completed   bool            `json:"completed"`
}

// CheckpointStore serializes access and keeps one manifest on disk.
type CheckpointStore struct {
	mu   sync.Mutex
	path string
}

// NewCheckpointStore binds a manifest path inside a validated directory.
func NewCheckpointStore(path string) (*CheckpointStore, error) {
	if _, err := SafeJoin(filepathDir(path)); err != nil {
		return nil, err
	}
	return &CheckpointStore{path: path}, nil
}

// Load reads an existing checkpoint. A missing file yields an empty one with
// ok=false, which is the normal state for a first run.
func (s *CheckpointStore) Load() (cp Checkpoint, ok bool, err error) {
	raw, readErr := os.ReadFile(s.path)
	if os.IsNotExist(readErr) {
		return Checkpoint{}, false, nil
	} else if readErr != nil {
		return Checkpoint{}, false, readErr
	}
	if err := json.Unmarshal(raw, &cp); err != nil {
		return Checkpoint{}, false, err
	}
	return cp, true, nil
}

// IsSegmentDone reports whether a segment index is already committed.
func (cp *Checkpoint) IsSegmentDone(index int) bool {
	for _, seg := range cp.Segments {
		if seg.Index == index {
			return true
		}
	}
	return false
}

// SegmentCount returns how many segments are durably committed.
func (cp *Checkpoint) SegmentCount() int { return len(cp.Segments) }

// CommitSegment appends one segment and atomically rewrites the manifest.
// Callers must finish writing the output file first; this never writes media.
func (s *CheckpointStore) CommitSegment(cp *Checkpoint, seg SegmentRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cp.IsSegmentDone(seg.Index) {
		return nil // idempotent: re-committing the same segment is a no-op
	}
	cp.Segments = append(cp.Segments, seg)
	return s.persist(cp)
}

// MarkComplete records the final combined digest and closes the job.
func (s *CheckpointStore) MarkComplete(cp *Checkpoint, finalDigest string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp.FinalDigest = finalDigest
	cp.Completed = true
	return s.persist(cp)
}

func (s *CheckpointStore) persist(cp *Checkpoint) error {
	data, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(s.path, append(data, '\n'), 0o644)
}
