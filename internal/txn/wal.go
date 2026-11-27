// Package txn implements write-ahead logging and atomic, verifiable
// checkpoints for pipeline stages. Every stage output is first recorded in a
// WAL with the input hash, processing cursor and stage summary; only after
// the output is fully validated is a checkpoint committed atomically. On
// restart, committed stages are replayed and uncommitted artifacts are
// discarded, so a repeated run produces identical summaries and hashes.
package txn

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	mediaerr "mediacheck/internal/errors"
)

// Stage names used inside the WAL. They mirror the pipeline ordering.
const (
	StageScan     = "scan"
	StageParse    = "parse"
	StageIndex    = "index"
	StageReport   = "report"
)

// MaxWALBytes bounds a single WAL file so a corrupt log cannot exhaust disk.
const MaxWALBytes = 256 << 20 // 256 MiB

// Entry is one write-ahead record: a stage output plus the context needed to
// verify it on recovery.
type Entry struct {
	Stage       string          `json:"stage"`
	Track       string          `json:"track,omitempty"`
	InputSHA256 string          `json:"input_sha256"`
	Cursor      int64           `json:"cursor"`       // bytes consumed when written
	OutputFile  string          `json:"output_file"`  // relative path of the artifact
	OutputSize  int64           `json:"output_size"`
	OutputSHA   string          `json:"output_sha256"`
	Summary     json.RawMessage `json:"summary"`      // stage-specific digest
	Committed   bool            `json:"committed"`
	Seq         int64           `json:"seq"`
}

// HashBytes returns the lowercase hex SHA-256 of data.
func HashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// WAL is an append-only, fsynced journal backed by one JSON-lines file.
type WAL struct {
	mu        sync.Mutex
	path      string
	dir       string
	entries   []Entry
	seq       int64
	committed map[string]bool // key stage/track -> committed
	temps     map[int64]string // seq -> staged temp path awaiting rename
}

// OpenWAL opens (or creates) the WAL inside dir and replays its records.
// Uncommitted entries are dropped and their orphaned output files removed.
func OpenWAL(dir string) (*WAL, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, mediaerr.Wrap(err, mediaerr.StageTxn, "", "mkdir wal")
	}
	w := &WAL{
		path:      filepath.Join(dir, "wal.jsonl"),
		dir:       dir,
		committed: map[string]bool{},
		temps:     map[int64]string{},
	}
	if err := w.replay(); err != nil {
		return nil, err
	}
	return w, nil
}

func keyOf(stage, track string) string { return stage + "/" + track }

// replay reads the WAL, retains only committed entries, and deletes the
// output artifacts of anything left uncommitted.
func (w *WAL) replay() error {
	raw, err := os.ReadFile(w.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return mediaerr.Wrap(err, mediaerr.StageTxn, "", "read wal")
	}
	if len(raw) > MaxWALBytes {
		return mediaerr.NewCoded(mediaerr.StageTxn, "", "txn.wal-too-large",
			"WAL %d bytes exceeds cap %d", len(raw), MaxWALBytes)
	}
	var (
		rawRecords []Entry
		orphans    []string
	)
	lines := splitLines(raw)
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}
		var e Entry
		if err := json.Unmarshal(line, &e); err != nil {
			return mediaerr.NewCoded(mediaerr.StageTxn, "", "txn.wal-corrupt",
				"unparseable WAL record: %v", err)
		}
		w.seq = e.Seq
		rawRecords = append(rawRecords, e)
	}
	// A file is protected if any record for it is committed; only files that
	// never reached a committed record are orphaned temp outputs.
	protected := map[string]bool{}
	for _, e := range rawRecords {
		if e.Committed {
			protected[e.OutputFile] = true
		}
	}
	for _, e := range rawRecords {
		if e.Committed {
			w.entries = append(w.entries, e)
			w.committed[keyOf(e.Stage, e.Track)] = true
		} else if !protected[e.OutputFile] {
			orphans = append(orphans, e.OutputFile)
		}
	}
	for _, rel := range orphans {
		if rel == "" {
			continue
		}
		_ = os.Remove(filepath.Join(w.dir, rel))      // staged final name
		_ = os.Remove(filepath.Join(w.dir, rel+".tmp")) // leftover temp
	}
	return nil
}

// Append writes a stage output to the WAL and materializes its artifact. The
// output is staged under a temporary name and only renamed to its final name
// at Commit, so an interrupted Append never leaves a half-written artifact at
// the committed path.
func (w *WAL) Append(e Entry, payload []byte) (Entry, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if len(payload) > MaxWALBytes {
		return Entry{}, mediaerr.NewCoded(mediaerr.StageTxn, "",
			"txn.output-too-large", "artifact exceeds cap")
	}
	w.seq++
	e.Seq = w.seq
	e.Committed = false
	e.OutputSize = int64(len(payload))
	e.OutputSHA = HashBytes(payload)
	if e.OutputFile == "" {
		e.OutputFile = fmt.Sprintf("%s-%d.out", e.Stage, e.Seq)
	}
	tmp := e.OutputFile + ".tmp"
	final := filepath.Join(w.dir, e.OutputFile)
	tmpPath := filepath.Join(w.dir, tmp)
	if err := os.MkdirAll(filepath.Dir(final), 0o755); err != nil {
		return Entry{}, mediaerr.Wrap(err, mediaerr.StageTxn, e.Track, "mkdir output")
	}
	if err := os.WriteFile(tmpPath, payload, 0o644); err != nil {
		return Entry{}, mediaerr.Wrap(err, mediaerr.StageTxn, e.Track, "write temp output")
	}
	if err := w.fsyncDir(w.dir); err != nil {
		return Entry{}, err
	}
	// Journal the intent before the commit can happen.
	if err := w.writeRecord(e); err != nil {
		_ = os.Remove(tmpPath)
		return Entry{}, err
	}
	// Keep the temp path on the entry until Commit renames it.
	w.temps[e.Seq] = tmpPath
	w.entries = append(w.entries, e)
	return e, nil
}

// Commit validates the staged artifact against the recorded hash, atomically
// renames it into place, and marks the WAL entry committed. A hash mismatch
// or rename failure leaves no committed state and returns a txn error.
func (w *WAL) Commit(stage, track string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	idx := w.findLast(stage, track)
	if idx < 0 {
		return mediaerr.NewCoded(mediaerr.StageTxn, track, "txn.no-entry",
			"no WAL entry to commit for %s/%s", stage, track)
	}
	e := &w.entries[idx]
	if e.Committed {
		return nil // idempotent: committed stages are not re-committed
	}
	tmpPath, ok := w.temps[e.Seq]
	if !ok {
		// Recovery path: temp already gone; verify the final file instead.
		return w.verifyFinal(e)
	}
	delete(w.temps, e.Seq)
	data, err := os.ReadFile(tmpPath)
	if err != nil {
		return mediaerr.Wrap(err, mediaerr.StageTxn, track, "read staged output")
	}
	if got := HashBytes(data); got != e.OutputSHA {
		_ = os.Remove(tmpPath)
		return mediaerr.NewCoded(mediaerr.StageTxn, track, "txn.hash-mismatch",
			"staged output hash %s != recorded %s", got, e.OutputSHA)
	}
	final := filepath.Join(w.dir, e.OutputFile)
	if err := os.Rename(tmpPath, final); err != nil {
		return mediaerr.Wrap(err, mediaerr.StageTxn, track, "atomic rename")
	}
	if err := w.fsyncDir(w.dir); err != nil {
		return err
	}
	e.Committed = true
	w.committed[keyOf(stage, track)] = true
	return w.writeRecord(*e)
}

// verifyFinal is used on recovery when only the committed output remains.
func (w *WAL) verifyFinal(e *Entry) error {
	data, err := os.ReadFile(filepath.Join(w.dir, e.OutputFile))
	if err != nil {
		return mediaerr.Wrap(err, mediaerr.StageTxn, e.Track, "read committed output")
	}
	if HashBytes(data) != e.OutputSHA {
		return mediaerr.NewCoded(mediaerr.StageTxn, e.Track, "txn.hash-mismatch",
			"committed output failed hash verification")
	}
	return nil
}

// IsCommitted reports whether a stage/track already has a durable checkpoint.
func (w *WAL) IsCommitted(stage, track string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.committed[keyOf(stage, track)]
}

// HasCommitted reports whether any stage reached a durable commit, which is
// the resume signal even before a final checkpoint is finalized.
func (w *WAL) HasCommitted() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.committed) > 0
}

// CommittedEntries returns a sorted snapshot of committed WAL records.
func (w *WAL) CommittedEntries() []Entry {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]Entry, 0, len(w.entries))
	for _, e := range w.entries {
		if e.Committed {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out
}

func (w *WAL) findLast(stage, track string) int {
	for i := len(w.entries) - 1; i >= 0; i-- {
		if w.entries[i].Stage == stage && w.entries[i].Track == track {
			return i
		}
	}
	return -1
}

func (w *WAL) writeRecord(e Entry) error {
	line, err := json.Marshal(e)
	if err != nil {
		return mediaerr.Wrap(err, mediaerr.StageTxn, e.Track, "marshal wal entry")
	}
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return mediaerr.Wrap(err, mediaerr.StageTxn, e.Track, "open wal")
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return mediaerr.Wrap(err, mediaerr.StageTxn, e.Track, "write wal")
	}
	if err := f.Sync(); err != nil {
		return mediaerr.Wrap(err, mediaerr.StageTxn, e.Track, "fsync wal")
	}
	return nil
}

func (w *WAL) fsyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return mediaerr.Wrap(err, mediaerr.StageTxn, "", "open dir for fsync")
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return mediaerr.Wrap(err, mediaerr.StageTxn, "", "fsync dir")
	}
	return nil
}

func splitLines(b []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, c := range b {
		if c == '\n' {
			out = append(out, b[start:i])
			start = i + 1
		}
	}
	if start < len(b) {
		out = append(out, b[start:])
	}
	return out
}
