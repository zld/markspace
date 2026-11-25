package txn

import (
	"os"
	"path/filepath"
	"time"

	"mediad/internal/timeline"
)

// StageFunc produces a stage output at the given temp path. It must be
// deterministic: re-running the same input yields identical bytes.
type StageFunc func(tempPath string) error

// StageOutput pairs a stage with its final committed output filename.
type StageOutput struct {
	Stage string
	Name  string // committed file name inside the WAL directory
}

// Manager runs stages against a WAL, committing each atomically.
type Manager struct {
	wal      *WAL
	dir      string
	outputs  []StageOutput
}

// NewManager binds a WAL and the ordered stage outputs it will manage.
func NewManager(wal *WAL, outputs []StageOutput) *Manager {
	return &Manager{wal: wal, dir: filepath.Dir(wal.Path()), outputs: outputs}
}

// outputFor finds the declared output filename for a stage.
func (m *Manager) outputFor(stage string) (string, bool) {
	for _, o := range m.outputs {
		if o.Stage == stage {
			return o.Name, true
		}
	}
	return "", false
}

// committedPath returns the final path of a stage output.
func (m *Manager) committedPath(name string) string {
	return filepath.Join(m.dir, name)
}

// IsCommitted reports whether a stage's output is already present and matches
// the WAL's committed record (used to skip work on resume).
func (m *Manager) IsCommitted(stage string) (bool, error) {
	name, ok := m.outputFor(stage)
	if !ok {
		return false, nil
	}
	got, err := FileSHA256(m.committedPath(name))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, r := range m.wal.Records() {
		if r.Stage == stage && r.Committed && r.TempSHA == got {
			return true, nil
		}
	}
	return false, nil
}

// RunStage writes a WAL intent record, executes fn into a private temp file,
// then atomically renames it into place and writes a committed WAL record.
// A failure before the rename leaves no half-finished committed output.
func (m *Manager) RunStage(stage string, fn StageFunc) error {
	if done, err := m.IsCommitted(stage); err != nil {
		return err
	} else if done {
		return nil // safe to reuse on resume
	}

	temp := m.wal.TempPath(stage)
	// Remove any stale temp from a previous interrupted attempt.
	if err := os.Remove(temp); err != nil && !os.IsNotExist(err) {
		return err
	}

	if _, err := m.wal.Append(Record{Stage: stage, TempFile: filepath.Base(temp), TimeMS: nowMS()}); err != nil {
		return err
	}
	if err := fn(temp); err != nil {
		_ = os.Remove(temp) // roll back the uncommitted temp output
		return err
	}
	// Enforce owner-only permissions on the produced output.
	if err := os.Chmod(temp, privateFileMode); err != nil {
		_ = os.Remove(temp)
		return err
	}
	sum, err := FileSHA256(temp)
	if err != nil {
		_ = os.Remove(temp)
		return err
	}

	name, _ := m.outputFor(stage)
	final := m.committedPath(name)
	if err := atomicRename(temp, final); err != nil {
		_ = os.Remove(temp)
		return err
	}
	if _, err := m.wal.Append(Record{
		Stage: stage, TempFile: name, TempSHA: sum, Committed: true, TimeMS: nowMS(),
	}); err != nil {
		return err
	}
	return nil
}

// Rollback removes a stage's committed output and returns it to a re-runnable
// state. Earlier stages are left intact.
func (m *Manager) Rollback(stage string) error {
	if name, ok := m.outputFor(stage); ok {
		if err := os.Remove(m.committedPath(name)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// Recover validates that resuming is safe: the input digest must match the
// WAL binding and every committed output must hash to its WAL record. Any
// mismatch is an ErrRecovery (exit 3); it never silently reuses stale state.
func (m *Manager) Recover(currentInputSHA string) (string, error) {
	if currentInputSHA != m.wal.InputSHA() {
		return "", timeline.New(timeline.ErrRecovery, m.wal.Path(), -1,
			"input digest changed: WAL %s vs current %s", m.wal.InputSHA(), currentInputSHA)
	}
	last := m.wal.LastCommittedStage()
	for _, r := range m.wal.Records() {
		if !r.Committed {
			continue
		}
		got, err := FileSHA256(m.committedPath(r.TempFile))
		if err != nil {
			return last, timeline.New(timeline.ErrRecovery, m.committedPath(r.TempFile), -1,
				"committed output missing: %v", err)
		}
		if got != r.TempSHA {
			return last, timeline.New(timeline.ErrRecovery, m.committedPath(r.TempFile), -1,
				"committed output checksum mismatch for stage %s", r.Stage)
		}
	}
	return last, nil
}

func nowMS() int64 { return time.Now().UnixMilli() }
