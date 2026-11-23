// Package txn implements the write-ahead journal and atomic commit protocol.
// Every result is appended to the WAL before validation; a commit marker is
// written and fsynced before the record becomes visible. Replaying a journal
// after a crash never double-commits and never changes the committed set.
package txn

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"sync"

	"mediacheck/internal/model"
	"mediacheck/internal/safeio"
)

// EntryState marks the journal lifecycle of one record.
type EntryState string

const (
	StatePrepared  EntryState = "prepared"
	StateCommitted EntryState = "committed"
	StateAborted   EntryState = "aborted"
)

// Entry is one journal line (one JSON object per line, newline-framed).
type Entry struct {
	TxnID       string                 `json:"txn_id"`
	Seq         int64                  `json:"seq"`
	Stage       model.Stage            `json:"stage"`
	State       EntryState             `json:"state"`
	Payload     map[string]interface{} `json:"payload"`
	ContentHash string                 `json:"content_hash,omitempty"`
}

// Journal is an append-only write-ahead log backed by one file.
type Journal struct {
	mu        sync.Mutex
	path      string
	file      *os.File
	writer    *bufio.Writer
	nextSeq   int64
	entries   map[string]*Entry // keyed by txn id
	order     []string          // txn ids in arrival order
	committed map[string]bool
}

const journalFileName = "mediacheck.wal"

// Open opens or creates the journal inside dir and replays it into memory.
func Open(dir string) (*Journal, error) {
	if dir == "" {
		return nil, model.ErrUnsafePath
	}
	path, err := safeio.ResolveContained(dir, journalFileName)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	j := &Journal{
		path: path, file: f, writer: bufio.NewWriter(f),
		nextSeq: 1, entries: map[string]*Entry{}, committed: map[string]bool{},
	}
	if err := j.replay(); err != nil {
		f.Close()
		return nil, err
	}
	return j, nil
}

// Path reports the journal file location.
func (j *Journal) Path() string { return j.path }

func (j *Journal) replay() error {
	rf, err := os.Open(j.path)
	if err != nil {
		return err
	}
	defer rf.Close()
	dec := json.NewDecoder(rf)
	for {
		var e Entry
		if err := dec.Decode(&e); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return model.ErrCorruptJournal
		}
		if e.TxnID == "" || e.Seq <= 0 {
			return model.ErrCorruptJournal
		}
		j.entries[e.TxnID] = &e
		if e.State == StateCommitted {
			j.committed[e.TxnID] = true
		}
		if e.Seq >= j.nextSeq {
			j.nextSeq = e.Seq + 1
		}
	}
	j.order = make([]string, 0, len(j.entries))
	for id := range j.entries {
		j.order = append(j.order, id)
	}
	sort.Slice(j.order, func(a, b int) bool {
		return j.entries[j.order[a]].Seq < j.entries[j.order[b]].Seq
	})
	return nil
}

// Append writes a new journal entry and fsyncs it before returning.
func (j *Journal) Append(e *Entry) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if e.TxnID == "" {
		return model.ErrCorruptJournal
	}
	if _, exists := j.entries[e.TxnID]; exists && e.State == StatePrepared {
		// Idempotent re-prepare after a crash: do not duplicate.
		return nil
	}
	e.Seq = j.nextSeq
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	if _, err := j.writer.Write(line); err != nil {
		return err
	}
	if err := j.writer.Flush(); err != nil {
		return err
	}
	if err := j.file.Sync(); err != nil {
		return err
	}
	j.nextSeq++
	j.entries[e.TxnID] = e
	if e.State == StateCommitted {
		j.committed[e.TxnID] = true
	}
	j.order = append(j.order, e.TxnID)
	return nil
}

// IsCommitted reports whether a transaction already reached commit.
func (j *Journal) IsCommitted(txnID string) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.committed[txnID]
}

// ContentHash returns the recorded content hash for a transaction id, or "".
func (j *Journal) ContentHash(txnID string) string {
	j.mu.Lock()
	defer j.mu.Unlock()
	if e, ok := j.entries[txnID]; ok {
		return e.ContentHash
	}
	return ""
}

// CommittedIDs returns committed transaction ids in stable sequence order.
func (j *Journal) CommittedIDs() []string {
	j.mu.Lock()
	defer j.mu.Unlock()
	var out []string
	for _, id := range j.order {
		if j.committed[id] {
			out = append(out, id)
		}
	}
	return out
}

// Pending returns prepared-but-not-committed entries (rolled back on resume).
func (j *Journal) Pending() []Entry {
	j.mu.Lock()
	defer j.mu.Unlock()
	var out []Entry
	for _, id := range j.order {
		e := j.entries[id]
		if e.State == StatePrepared {
			out = append(out, *e)
		}
	}
	return out
}

// Close flushes and closes the underlying file.
func (j *Journal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.writer.Flush(); err != nil {
		return err
	}
	return j.file.Close()
}

// String helps diagnostics.
func (e *Entry) String() string {
	return fmt.Sprintf("%s#%d/%s/%s", e.TxnID, e.Seq, e.Stage, e.State)
}
