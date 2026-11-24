package txn

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"

	"medpipe/internal/fault"
)

// VerifyFunc checks a candidate output before commit or recovery.
type VerifyFunc func(path string) *fault.Error

// Marker is the durable successful-commit marker.
type Marker struct {
	Version       int    `json:"version"`
	OutputPath    string `json:"output_path"`
	OutputSHA256  string `json:"output_sha256"`
	InputSHA256   string `json:"input_sha256"`
	MappingSHA256 string `json:"mapping_sha256"`
	OutputSize    int64  `json:"output_size"`
}

// Manager owns a directory and its WAL.
type Manager struct {
	Dir           string
	WALPath       string
	WAL           *WAL
	RollbackCount int
}

// Transaction tracks one output replacement.
type Transaction struct {
	mgr          *Manager
	ID           string
	CurrentStage string
	InputSHA     string
	MappingSHA   string
	FinalPath    string
	TempPath     string
	MarkerPath   string
	OutputSHA    string
	OutputSize   int64
	terminal     bool
}

// NewManager opens the WAL in dir.
func NewManager(dir string) (*Manager, *fault.Error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fault.Wrap(fault.ErrWAL, "txn.dir", 0, dir, err)
	}
	walPath := filepath.Join(dir, WALName)
	w, fe := OpenWAL(walPath)
	if fe != nil {
		return nil, fe
	}
	return &Manager{Dir: dir, WALPath: walPath, WAL: w}, nil
}

// Close releases the WAL.
func (m *Manager) Close() {
	if m != nil && m.WAL != nil {
		_ = m.WAL.Close()
	}
}

// Begin starts a transaction and records its input/mapping digests before any
// mapping or output stage runs.
func (m *Manager) Begin(finalName, inputSHA, mappingSHA string) (*Transaction, *fault.Error) {
	name, fe := safeName(finalName)
	if fe != nil {
		return nil, fe
	}
	idSum := sha256.Sum256([]byte(name + "|" + inputSHA + "|" + mappingSHA))
	id := "tx-" + hex.EncodeToString(idSum[:6])
	finalPath := filepath.Join(m.Dir, name)
	t := &Transaction{
		mgr:        m,
		ID:         id,
		InputSHA:   inputSHA,
		MappingSHA: mappingSHA,
		FinalPath:  finalPath,
		TempPath:   finalPath + ".tmp",
		MarkerPath: finalPath + ".commit.json",
	}
	if _, err := m.WAL.Append(Record{
		TxnID: id, Stage: "begin", State: StateStarted,
		InputSHA256: inputSHA, MappingSHA256: mappingSHA,
		FinalPath: finalPath, TempPath: t.TempPath,
	}); err != nil {
		return nil, err
	}
	// Remove a stale temp from an older failed attempt. The final output is
	// never removed here.
	_ = os.Remove(t.TempPath)
	return t, nil
}

// Stage writes a stage-start WAL record.
func (t *Transaction) Stage(stage, message string) *fault.Error {
	t.CurrentStage = stage
	_, fe := t.mgr.WAL.Append(Record{
		TxnID: t.ID, Stage: stage, State: StateStarted, Message: message,
		InputSHA256: t.InputSHA, MappingSHA256: t.MappingSHA,
		FinalPath: t.FinalPath, TempPath: t.TempPath,
	})
	return fe
}

// Prepare records the verified temporary-output digest.
func (t *Transaction) Prepare(outputSHA string, size int64) *fault.Error {
	t.OutputSHA = outputSHA
	t.OutputSize = size
	_, fe := t.mgr.WAL.Append(Record{
		TxnID: t.ID, Stage: "output_verified", State: StatePrepared,
		InputSHA256: t.InputSHA, MappingSHA256: t.MappingSHA,
		FinalPath: t.FinalPath, TempPath: t.TempPath,
		OutputSHA256: outputSHA, OutputSize: size,
	})
	return fe
}

// Commit atomically renames the temp file, fsyncs the directory, writes the
// commit marker, and records the terminal WAL state.
func (t *Transaction) Commit() (*Marker, *fault.Error) {
	if t.terminal {
		return nil, fault.New(fault.ErrCommit, "txn.commit", 0, "transaction already terminal")
	}
	if t.OutputSHA == "" {
		sum, size, fe := HashFile(t.TempPath)
		if fe != nil {
			return nil, fe
		}
		t.OutputSHA, t.OutputSize = sum, size
	}
	if _, fe := t.mgr.WAL.Append(Record{TxnID: t.ID, Stage: "commit_rename", State: StateStarted, FinalPath: t.FinalPath, TempPath: t.TempPath, OutputSHA256: t.OutputSHA}); fe != nil {
		return nil, fe
	}
	if err := os.Rename(t.TempPath, t.FinalPath); err != nil {
		return nil, fault.Wrap(fault.ErrCommit, "txn.rename", 0, "atomic rename", err)
	}
	if fe := syncDir(t.mgr.Dir); fe != nil {
		return nil, fe
	}
	marker := Marker{Version: 1, OutputPath: t.FinalPath, OutputSHA256: t.OutputSHA, InputSHA256: t.InputSHA, MappingSHA256: t.MappingSHA, OutputSize: t.OutputSize}
	if fe := WriteMarker(t.MarkerPath, marker); fe != nil {
		return nil, fe
	}
	if _, fe := t.mgr.WAL.Append(Record{
		TxnID: t.ID, Stage: "committed", State: StateCommitted,
		FinalPath: t.FinalPath, CommitMarker: t.MarkerPath,
		OutputSHA256: t.OutputSHA, OutputSize: t.OutputSize,
	}); fe != nil {
		return nil, fe
	}
	t.terminal = true
	return &marker, nil
}

// Rollback removes the temporary output and records terminal state.
func (t *Transaction) Rollback(reason string) *fault.Error {
	if t != nil && t.terminal {
		return nil
	}
	if t != nil {
		_ = os.Remove(t.TempPath)
		_, _ = t.mgr.WAL.Append(Record{
			TxnID: t.ID, Stage: "rollback", State: StateRolledBack,
			FinalPath: t.FinalPath, TempPath: t.TempPath, Message: reason,
		})
		t.terminal = true
		t.mgr.RollbackCount++
	}
	return nil
}

// CommittedMarker returns the valid marker for finalName when its digests
// match. It enables deterministic no-rewrite duplicate runs.
func (m *Manager) CommittedMarker(finalName, inputSHA, mappingSHA string) (*Marker, bool) {
	name, err := safeName(finalName)
	if err != nil {
		return nil, false
	}
	markerPath := filepath.Join(m.Dir, name+".commit.json")
	marker, fe := ReadMarker(markerPath)
	if fe != nil || marker.InputSHA256 != inputSHA || marker.MappingSHA256 != mappingSHA {
		return nil, false
	}
	sum, _, herr := HashFile(marker.OutputPath)
	if herr != nil || sum != marker.OutputSHA256 {
		return nil, false
	}
	return marker, true
}

// HashFile computes SHA-256 and size.
func HashFile(path string) (string, int64, *fault.Error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, fault.Wrap(fault.ErrDigestMismatch, "txn.hash.open", 0, path, err)
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", n, fault.Wrap(fault.ErrDigestMismatch, "txn.hash.read", 0, path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func safeName(name string) (string, *fault.Error) {
	if name == "" || filepath.Base(name) != name || name == "." || name == ".." {
		return "", fault.New(fault.ErrCommit, "txn.path", 0, "output name must be a simple file name")
	}
	return name, nil
}

func syncDir(dir string) *fault.Error {
	d, err := os.Open(dir)
	if err != nil {
		return fault.Wrap(fault.ErrCommit, "txn.fsync_dir.open", 0, dir, err)
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return fault.Wrap(fault.ErrCommit, "txn.fsync_dir.sync", 0, dir, err)
	}
	return nil
}
