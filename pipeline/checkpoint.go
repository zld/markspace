package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mediadist/segment"
	"mediadist/timeline"
)

// checkpointFile is the single state file inside the checkpoint directory.
const checkpointFile = "state.json"

// ErrPathEscape is returned when a requested path leaves the output root.
var ErrPathEscape = errors.New("path escapes the permitted output directory")

// Store persists per-stage state and the committed manifest. Every write is
// atomic (temp file + rename) so a crashed run never leaves a half-written
// checkpoint, and all paths are constrained to the output directory.
type Store struct {
	root string
}

// NewStore validates the checkpoint root and ensures it exists.
func NewStore(root string) (*Store, error) {
	clean, err := safePath(filepath.Dir(root), filepath.Base(root))
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(clean, 0o755); err != nil {
		return nil, fmt.Errorf("checkpoint mkdir: %w", err)
	}
	return &Store{root: clean}, nil
}

// safePath joins root and a relative request and rejects traversal.
func safePath(root, request string) (string, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	clean := filepath.Clean(filepath.Join(absRoot, request))
	rel, err := filepath.Rel(absRoot, clean)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("%w: %q", ErrPathEscape, request)
	}
	return clean, nil
}

// checkpoint is the on-disk state document.
type checkpoint struct {
	Version int          `json:"version"`
	Stages  []StageState `json:"stages"`
}

// Load reads the checkpoint; a missing file yields an empty state.
func (s *Store) Load() (*checkpoint, error) {
	p, err := safePath(s.root, checkpointFile)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return &checkpoint{Version: 1, Stages: freshStages()}, nil
	}
	if err != nil {
		return nil, err
	}
	var cp checkpoint
	if err := json.Unmarshal(raw, &cp); err != nil {
		// Corrupt checkpoint is a classified, recoverable condition.
		return nil, timeline.NewLocError(timeline.CodeCheckpoint, StageManifest, 0, -1,
			"load", fmt.Errorf("checkpoint unreadable: %w", err))
	}
	if cp.Version != 1 || len(cp.Stages) != len(OrderedStages) {
		return nil, timeline.NewLocError(timeline.CodeCheckpoint, StageManifest, 0, -1,
			"load", fmt.Errorf("checkpoint schema mismatch"))
	}
	return &cp, nil
}

func freshStages() []StageState {
	out := make([]StageState, 0, len(OrderedStages))
	for _, name := range OrderedStages {
		out = append(out, StageState{Name: name})
	}
	return out
}

// write atomically replaces the checkpoint file.
func (s *Store) write(cp *checkpoint) error {
	p, err := safePath(s.root, checkpointFile)
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// MarkCompleted flips a stage to completed and clears any failure marker.
func (s *Store) MarkCompleted(stage string) error {
	cp, err := s.Load()
	if err != nil {
		return err
	}
	for i := range cp.Stages {
		if cp.Stages[i].Name == stage {
			cp.Stages[i] = StageState{Name: stage, Completed: true}
		}
	}
	return s.write(cp)
}

// checkpointStage maps an internal error stage onto one of the four checkpoint
// slots. Timeline validation runs as part of the demux checkpoint stage.
func checkpointStage(stage string) string {
	if stage == StageTimeline {
		return StageDemux
	}
	return stage
}

// MarkFailed records a failed stage without writing any stage artifact.
func (s *Store) MarkFailed(stage, code, text string) error {
	stage = checkpointStage(stage)
	cp, err := s.Load()
	if err != nil {
		return err
	}
	for i := range cp.Stages {
		if cp.Stages[i].Name == stage {
			cp.Stages[i] = StageState{Name: stage, Completed: false, Failed: true,
				ErrorCode: code, ErrorText: text}
		}
	}
	return s.write(cp)
}

// Snapshot returns the current stage states in execution order.
func (s *Store) Snapshot() []StageState {
	cp, err := s.Load()
	if err != nil {
		return freshStages()
	}
	return cp.Stages
}

// FailedStage returns the first failed stage name and state, or "" if none.
func (s *Store) FailedStage() (string, StageState) {
	cp, err := s.Load()
	if err != nil {
		return "", StageState{}
	}
	for _, st := range cp.Stages {
		if st.Failed {
			return st.Name, st
		}
	}
	return "", StageState{}
}

// Reset clears progress so a repaired input reruns from the failed stage.
// Completed stages before the failure remain valid and are not recomputed.
func (s *Store) ResetFailed() error {
	cp, err := s.Load()
	if err != nil {
		return err
	}
	for i := range cp.Stages {
		if cp.Stages[i].Failed {
			cp.Stages[i] = StageState{Name: cp.Stages[i].Name}
		}
	}
	return s.write(cp)
}

// CommitManifest validates and atomically writes the manifest plus a hash file
// inside the output directory. It is the only place publishable artifacts land.
func (s *Store) CommitManifest(outDir string, m *segment.Manifest) error {
	dir, err := safePath(outDir, ".")
	if err != nil {
		return err
	}
	// Re-verify the content hash over canonical bytes before committing.
	canonical, err := m.CanonicalJSON()
	if err != nil {
		return err
	}
	sum := sha256.Sum256(canonical)
	if hex.EncodeToString(sum[:]) != m.SHA256 {
		return timeline.NewLocError(timeline.CodeCheckpoint, StageManifest, 0, -1,
			"commit", fmt.Errorf("manifest hash mismatch"))
	}
	// Persist the full manifest (hash field included) for downstream readers.
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	mPath, err := safePath(dir, "manifest.json")
	if err != nil {
		return err
	}
	tmp := mPath + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, mPath); err != nil {
		return err
	}
	hPath, err := safePath(dir, "manifest.sha256")
	if err != nil {
		return err
	}
	return os.WriteFile(hPath, []byte(m.SHA256+"\n"), 0o644)
}
