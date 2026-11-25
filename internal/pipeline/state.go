// Package pipeline turns validated, aligned media into crash-safe,
// idempotent, byte-deterministic fMP4 artifacts and manifests.
package pipeline

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"regress-kernel/internal/media"
)

// Stage is one recoverable processing phase; ordering is fixed.
type Stage string

const (
	StageIngest  Stage = "ingest"
	StageAlign   Stage = "align"
	StagePublish Stage = "publish"
	StageMerge   Stage = "merge"
)

// Stages in the mandatory execution order.
var Stages = []Stage{StageIngest, StageAlign, StagePublish, StageMerge}

// StageIndex returns the ordinal of a stage, or -1 when unknown.
func StageIndex(s Stage) int {
	for i, want := range Stages {
		if s == want {
			return i
		}
	}
	return -1
}

// Terminal task states.
const (
	StateRunning     = "running"
	StateFailed      = "failed"
	StateQuarantined = "quarantined"
	StateCommitted   = "committed"
)

// ArtifactRecord is the state-side description of one published segment.
type ArtifactRecord struct {
	Name      string `json:"name"`
	StartTick int64  `json:"start_tick"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
}

// TaskState is the durable state machine document of one stable task.
type TaskState struct {
	TaskKey      string                 `json:"task_key"`
	Rendition    string                 `json:"rendition"`
	Status       string                 `json:"status"`
	Stage        Stage                  `json:"stage"`
	UpdatedAt    string                 `json:"updated_at"` // deterministic label, no wall clock
	InputSummary InputSummary           `json:"input_summary"`
	Artifacts    []ArtifactRecord       `json:"artifacts"`
	Faults       []media.FrameFault     `json:"faults,omitempty"`
	Failure      string                 `json:"failure,omitempty"`
	Extras       map[string]interface{} `json:"extras,omitempty"`
}

// InputSummary is the stable content digest of what the task consumed.
type InputSummary struct {
	FrameCount   int    `json:"frame_count"`
	InputBytes   int64  `json:"input_bytes"`
	FrameHash    string `json:"frame_hash"`
	AudioHash    string `json:"audio_hash"`
	LossInjected int    `json:"loss_injected"`
}

// Workspace is the single rooted output directory with path-escape protection.
type Workspace struct{ root string }

// OpenWorkspace resolves and creates the workspace layout.
func OpenWorkspace(root string) (*Workspace, error) {
	abs, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return nil, err
	}
	for _, dir := range []string{abs, filepath.Join(abs, "tasks"),
		filepath.Join(abs, "segments"), filepath.Join(abs, "quarantine"),
		filepath.Join(abs, "checkpoints")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	return &Workspace{root: abs}, nil
}

// Root exposes the absolute workspace path.
func (w *Workspace) Root() string { return w.root }

// SafeJoin joins name under the workspace and rejects any escape attempt.
func (w *Workspace) SafeJoin(parts ...string) (string, error) {
	cleaned := make([]string, 0, len(parts)+1)
	cleaned = append(cleaned, w.root)
	for _, p := range parts {
		if filepath.IsAbs(p) {
			return "", fmt.Errorf("%w: absolute segment %q", media.ErrPathEscape, p)
		}
		cleaned = append(cleaned, filepath.Clean(p))
	}
	full := filepath.Join(cleaned...)
	rel, err := filepath.Rel(w.root, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %q", media.ErrPathEscape, filepath.Join(parts...))
	}
	return full, nil
}

func (w *Workspace) taskDir(key string) (string, error) {
	return w.SafeJoin("tasks", sanitizeKey(key))
}

func (w *Workspace) statePath(key string) (string, error) {
	dir, err := w.taskDir(key)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "state.json"), nil
}

// sanitizeKey keeps every task-derived filesystem element in a safe charset.
func sanitizeKey(key string) string {
	var b strings.Builder
	for _, r := range key {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "_"
	}
	return b.String()
}

// LoadState reads a task state; a missing file means a fresh task.
func (w *Workspace) LoadState(key string) (*TaskState, bool, error) {
	path, err := w.statePath(key)
	if err != nil {
		return nil, false, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var st TaskState
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, false, fmt.Errorf("%w: %v", media.ErrCheckpoint, err)
	}
	return &st, true, nil
}

// SaveState atomically replaces the task state document (temp file + rename).
func (w *Workspace) SaveState(st *TaskState) error {
	path, err := w.statePath(st.TaskKey)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Quarantine moves the whole task directory aside and marks the state.
func (w *Workspace) Quarantine(st *TaskState, reason string) (string, error) {
	st.Status = StateQuarantined
	st.Failure = reason
	if err := w.SaveState(st); err != nil {
		return "", err
	}
	src, err := w.taskDir(st.TaskKey)
	if err != nil {
		return "", err
	}
	dst, err := w.SafeJoin("quarantine", sanitizeKey(st.TaskKey))
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(src); err != nil {
		return dst, nil // nothing on disk to move; state already isolated
	}
	return dst, os.Rename(src, dst)
}

// SortedTaskKeys lists task directories in stable order.
func (w *Workspace) SortedTaskKeys() ([]string, error) {
	dir := filepath.Join(w.root, "tasks")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			keys = append(keys, e.Name())
		}
	}
	sort.Strings(keys)
	return keys, nil
}
