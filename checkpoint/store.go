// Package checkpoint persists pipeline progress so an interrupted publication
// can resume at the last fully committed block.
//
// A checkpoint records, per stage, the input digest, the highest committed
// block, the timeline position of that block and the output digest. Loading is
// validated against the current run: if the input digest or the stage chain is
// inconsistent, the caller is told to restart that stage from its beginning
// rather than trusting stale positions. Writes are atomic (temp file plus
// rename) so a crash never leaves a half-written checkpoint behind.
package checkpoint

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/zld/media-regress/internal/error"
)

// Stage names a checkpointable phase.
type Stage string

const (
	StageProbe    Stage = "probe"
	StageMetadata Stage = "metadata"
	StageAudio    Stage = "audio"
	StageStream   Stage = "stream"
	StageManifest Stage = "manifest"
)

// Stages is the pipeline order.
var Stages = []Stage{StageProbe, StageMetadata, StageAudio, StageStream, StageManifest}

// StageState is one stage's durable position.
type StageState struct {
	Stage          Stage  `json:"stage"`
	InputDigest    string `json:"input_digest"`
	CommittedBlock int    `json:"committed_block"` // highest fully committed block, -1 when none
	Timestamp      uint32 `json:"timestamp"`       // RTP timestamp of the committed block's first sample
	OutputDigest   string `json:"output_digest"`
	// Recompute marks that a later stage must redo from this stage's start.
	Recompute bool `json:"recompute"`
}

// File is the on-disk checkpoint.
type File struct {
	Version     int                   `json:"version"`
	RunID       string                `json:"run_id"`
	Source      string                `json:"source"`
	Input       string                `json:"input"`
	TotalBlocks int                   `json:"total_blocks"`
	Stages      map[Stage]*StageState `json:"stages"`
	// Order preserves stage insertion order for stable output.
	Order []Stage `json:"order"`
}

// Store persists one checkpoint file.
type Store struct {
	path string
	file *File
}

// NewFile builds an empty checkpoint.
func NewFile(runID, source, input string, total int) *File {
	return &File{
		Version:     1,
		RunID:       runID,
		Source:      source,
		Input:       input,
		TotalBlocks: total,
		Stages:      map[Stage]*StageState{},
	}
}

// Open loads an existing checkpoint or creates a new one at path.
func Open(path string, fresh *File) (*Store, error) {
	if path == "" {
		return nil, merr.New(merr.StageCheckpt, merr.KindParse, 0, 0, "checkpoint path is empty")
	}
	s := &Store{path: path, file: fresh}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, merr.Wrap(merr.StageCheckpt, merr.KindParse, 0, 0, err, "read checkpoint")
	}
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, merr.Wrap(merr.StageCheckpt, merr.KindCorrupt, 0, 0, err, "decode checkpoint")
	}
	if f.Version != 1 {
		return nil, merr.New(merr.StageCheckpt, merr.KindInconsistent, 0, 0,
			"unsupported checkpoint version %d", f.Version)
	}
	s.file = &f
	return s, nil
}

// File returns the in-memory checkpoint.
func (s *Store) File() *File { return s.file }

// Save writes the checkpoint atomically.
func (s *Store) Save() error {
	data, err := json.MarshalIndent(s.file, "", "  ")
	if err != nil {
		return merr.Wrap(merr.StageCheckpt, merr.KindParse, 0, 0, err, "encode checkpoint")
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return merr.Wrap(merr.StageCheckpt, merr.KindParse, 0, 0, err, "create checkpoint directory")
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return merr.Wrap(merr.StageCheckpt, merr.KindParse, 0, 0, err, "write checkpoint")
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return merr.Wrap(merr.StageCheckpt, merr.KindParse, 0, 0, err, "commit checkpoint")
	}
	return nil
}

// Record stores a stage position and appends the stage to the order.
func (s *Store) Record(state StageState) {
	if s.file.Stages == nil {
		s.file.Stages = map[Stage]*StageState{}
	}
	if _, ok := s.file.Stages[state.Stage]; !ok {
		s.file.Order = append(s.file.Order, state.Stage)
	}
	cp := state
	s.file.Stages[state.Stage] = &cp
}

// Stage returns a stage position, if recorded.
func (s *Store) Stage(stage Stage) (*StageState, bool) {
	st, ok := s.file.Stages[stage]
	return st, ok
}

// resumeError reports why a checkpoint cannot be trusted.
func resumeError(format string, args ...interface{}) error {
	return merr.New(merr.StageCheckpt, merr.KindResume, 0, 0, format, args...)
}

// Resume describes where processing restarts after loading a checkpoint.
type Resume struct {
	// FromStage is the earliest stage that must run.
	FromStage Stage
	// StartBlock is the first block to process in the audio/stream stages.
	StartBlock int
	// RecomputeFrom is the stage from which outputs must be regenerated.
	RecomputeFrom Stage
	// Recomputed reports whether any earlier stage must be redone.
	Recomputed bool
}

// Plan validates the checkpoint against the current input and returns the
// resume position. A digest mismatch is not silently accepted: the stage whose
// input no longer matches, and every later stage, restart from the beginning.
func Plan(f *File, input string) (Resume, error) {
	total := f.TotalBlocks
	res := Resume{FromStage: StageProbe, StartBlock: 0, RecomputeFrom: StageProbe}
	if f.Input != input {
		// The content changed: nothing earlier can be reused.
		return Resume{FromStage: StageProbe, StartBlock: 0, RecomputeFrom: StageProbe, Recomputed: true}, nil
	}
	// Validate every recorded position before trusting any of them, so a
	// corrupt later stage cannot hide behind an earlier gap.
	for _, stage := range Stages {
		st, ok := f.Stages[stage]
		if !ok {
			continue
		}
		if stage == StageAudio || stage == StageStream {
			if st.CommittedBlock < -1 || st.CommittedBlock >= total {
				return Resume{}, resumeError("checkpoint stage %s has committed block %d outside 0..%d",
					stage, st.CommittedBlock, total-1)
			}
		}
	}
	lastGood := -1
	from := StageProbe
	for i, stage := range Stages {
		st, ok := f.Stages[stage]
		if !ok {
			from = stage
			break
		}
		if st.InputDigest != input {
			from = stage
			break
		}
		if stage == StageAudio || stage == StageStream {
			lastGood = st.CommittedBlock
		}
		if st.Recompute {
			from = stage
			break
		}
		if i == len(Stages)-1 {
			from = StageManifest
		}
	}
	res.FromStage = from
	res.RecomputeFrom = from
	if lastGood >= 0 {
		res.StartBlock = lastGood + 1
	}
	res.Recomputed = from != StageManifest
	return res, nil
}

// SortedStages returns recorded stages in canonical pipeline order.
func (f *File) SortedStages() []StageState {
	var out []StageState
	for _, s := range Stages {
		if st, ok := f.Stages[s]; ok {
			out = append(out, *st)
		}
	}
	return out
}

// Summary renders the checkpoint for terminal output.
func (f *File) Summary() string {
	out := fmt.Sprintf("run=%s source=%s blocks=%d", f.RunID, f.Source, f.TotalBlocks)
	for _, st := range f.SortedStages() {
		out += fmt.Sprintf(" %s:%d", st.Stage, st.CommittedBlock)
	}
	return out
}

// LastCommitted returns the highest committed block across audio/stream.
func (f *File) LastCommitted() int {
	best := -1
	for _, s := range []Stage{StageAudio, StageStream} {
		if st, ok := f.Stages[s]; ok && st.CommittedBlock > best {
			best = st.CommittedBlock
		}
	}
	return best
}

// sortedKeys is a test helper exposed for diagnostics formatting.
func sortedKeys(m map[Stage]*StageState) []Stage {
	keys := make([]Stage, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}
