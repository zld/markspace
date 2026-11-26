// Package commit implements the crash-consistent artifact protocol and its
// append-only write-ahead journal:
//
//	write temp -> record temp path/length/hash (prepared, durable) ->
//	atomic rename -> record committed status (durable manifest).
//
// A simulated crash is injected between "prepared" and "committed"; recovery
// replays the journal and verifies hashes before adopting any product.
package commit

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"

	"mediapack/internal/manifest"
	"mediapack/internal/safepath"
)

// JournalName is the append-only WAL file inside the work directory.
const JournalName = "journal.jsonl"

// CrashExitCode is the dedicated process exit code for the simulated crash.
const CrashExitCode = 20

// ErrSimulatedCrash is returned at the crash-before-commit injection point.
var ErrSimulatedCrash = errors.New("simulated crash: artifact written and prepared, status not committed")

// Op is a journal operation kind.
type Op string

const (
	OpPrepared  Op = "prepared"
	OpCommitted Op = "committed"
	OpRecovered Op = "recovered"
)

// Entry is one journal record.
type Entry struct {
	Seq         int64  `json:"seq"`
	Op          Op     `json:"op"`
	ID          string `json:"id"`
	RelPath     string `json:"rel_path"`
	TempRelPath string `json:"temp_rel_path,omitempty"`
	Length      int64  `json:"length"`
	SHA256      string `json:"sha256"`
}

// Journal is a JSONL append-only write-ahead log.
type Journal struct {
	mu   sync.Mutex
	f    *os.File
	path string
	seq  int64
}

// OpenJournal opens (creating if needed) and locks the journal for append.
func OpenJournal(workDir string) (*Journal, error) {
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return nil, err
	}
	p, err := safepath.JoinUnder(workDir, JournalName)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	j := &Journal{f: f, path: p}
	entries, _ := Replay(workDir)
	for _, e := range entries {
		if e.Seq > j.seq {
			j.seq = e.Seq
		}
	}
	return j, nil
}

// Append durably writes one entry (sequence assigned automatically).
func (j *Journal) Append(e Entry) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.seq++
	e.Seq = j.seq
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if _, err := j.f.Write(append(b, '\n')); err != nil {
		return err
	}
	return j.f.Sync()
}

// Close closes the journal file.
func (j *Journal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.f == nil {
		return nil
	}
	err := j.f.Close()
	j.f = nil
	return err
}

// Replay reads all well-formed journal entries, ignoring a torn tail line.
func Replay(workDir string) ([]Entry, error) {
	p, err := safepath.JoinUnder(workDir, JournalName)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var out []Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		var e Entry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			continue // torn/corrupt tail record: ignore
		}
		out = append(out, e)
	}
	return out, nil
}

// PreparedButUncommitted returns IDs with a prepared record and no later
// committed/recovered terminal record.
func PreparedButUncommitted(entries []Entry) []Entry {
	terminal := map[string]bool{}
	var lastPrep map[string]Entry = map[string]Entry{}
	for _, e := range entries {
		switch e.Op {
		case OpPrepared:
			lastPrep[e.ID] = e
			terminal[e.ID] = false
		case OpCommitted, OpRecovered:
			terminal[e.ID] = true
		}
	}
	var out []Entry
	for id, e := range lastPrep {
		if !terminal[id] {
			out = append(out, e)
		}
	}
	return out
}

// Prepare streams content into the artifact temp file, hashes it, and durably
// records temp path/length/hash in both journal and manifest (status prepared).
func Prepare(workDir string, m *manifest.Manifest, j *Journal, a *manifest.Artifact, r io.Reader) error {
	if a.TempRelPath == "" {
		a.TempRelPath = a.RelPath + ".part"
	}
	tmpFull, err := manifest.Resolve(workDir, a.TempRelPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(tmpFull), 0o755); err != nil {
		return err
	}
	f, err := os.Create(tmpFull)
	if err != nil {
		return err
	}
	h := newSHA()
	n, err := io.Copy(io.MultiWriter(f, h), r)
	syncErr := f.Sync()
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	sum := hexSum(h)
	a.Length, a.SHA256, a.TempSHA256 = n, sum, sum
	a.Status = manifest.StatusPrepared
	m.UpsertArtifact(*a)
	if j != nil {
		if err := j.Append(Entry{Op: OpPrepared, ID: a.ID, RelPath: a.RelPath,
			TempRelPath: a.TempRelPath, Length: n, SHA256: sum}); err != nil {
			return err
		}
	}
	return m.Save(workDir)
}

// Commit atomically adopts the prepared temp file into its final path,
// verifies the recorded hash, and persists committed status. It also handles
// a crash after rename but before status commit by adopting an existing final.
func Commit(workDir string, m *manifest.Manifest, j *Journal, id string) (*manifest.Artifact, bool, error) {
	a := m.FindArtifact(id)
	if a == nil {
		return nil, false, errors.New("commit: unknown artifact " + id)
	}
	if a.Status == manifest.StatusCommitted {
		finalFull, err := manifest.Resolve(workDir, a.RelPath)
		if err != nil {
			return nil, false, err
		}
		if ok, _ := verifyFile(finalFull, a.SHA256, a.Length); ok {
			return a, false, nil // already published; do not duplicate
		}
	}

	tmpRel := a.TempRelPath
	if tmpRel == "" {
		tmpRel = a.RelPath + ".part"
	}
	tmpFull, _ := manifest.Resolve(workDir, tmpRel)
	finalFull, err := manifest.Resolve(workDir, a.RelPath)
	if err != nil {
		return nil, false, err
	}
	adopted := false
	if ok, _ := verifyFile(tmpFull, a.TempSHA256, a.Length); ok {
		if err := os.MkdirAll(filepath.Dir(finalFull), 0o755); err != nil {
			return nil, false, err
		}
		if err := os.Rename(tmpFull, finalFull); err != nil {
			return nil, false, err
		}
		adopted = true
	} else if ok, _ := verifyFile(finalFull, a.SHA256, a.Length); ok {
		// Rename already happened before the simulated/real status commit.
		adopted = true
	} else {
		return nil, false, errors.New("commit: no hash-valid temp or final product for " + id)
	}
	if ok, err := verifyFile(finalFull, a.SHA256, a.Length); !ok {
		if err != nil {
			return nil, false, err
		}
		return nil, false, errors.New("commit: final hash mismatch for " + id)
	}

	a.Status = manifest.StatusCommitted
	a.TempRelPath = ""
	m.UpsertArtifact(*a)
	if j != nil {
		_ = j.Append(Entry{Op: OpCommitted, ID: a.ID, RelPath: a.RelPath,
			Length: a.Length, SHA256: a.SHA256})
	}
	if err := m.Save(workDir); err != nil {
		return nil, adopted, err
	}
	return a, adopted, nil
}
