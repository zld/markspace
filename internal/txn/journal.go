// Package txn implements the write-ahead journal and atomic stage commit
// protocol. A crash at any point leaves either the previous fully
// committed boundary or a replayable log: uncommitted stages are rolled
// back on restart, and re-running the same inputs yields the same
// commit id and summary hash.
package txn

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// Entry kinds on disk. The single-byte tag keeps the journal format
// trivially parseable and version-stable.
const (
	entryBegin    = 'B' // stage begun: name length + name
	entryChecksum = 'C' // stage checksum: 32-byte SHA-256
	entryCommit   = 'M' // stage committed: name
	entryRollback = 'R' // stage rolled back: name
)

// Journal is an append-only write-ahead log backed by one file plus a
// one-file committed-boundary marker. All writes are fsynced before a
// commit is acknowledged.
type Journal struct {
	mu       sync.Mutex
	path     string
	file     *os.File
	writer   *bufio.Writer
	committed map[string]bool
}

// OpenJournal opens (or creates) the journal inside dir and replays any
// entries left by a previous process. Stages found in BEGIN without a
// matching COMMIT are treated as interrupted and rolled back on disk.
func OpenJournal(dir string) (*Journal, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("txn: journal dir: %w", err)
	}
	path := filepath.Join(dir, "journal.wal")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("txn: open journal: %w", err)
	}
	j := &Journal{path: path, file: f, writer: bufio.NewWriter(f),
		committed: map[string]bool{}}
	if err := j.replay(); err != nil {
		_ = f.Close()
		return nil, err
	}
	// Re-open in append mode after replay consumed the reader position.
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("txn: close after replay: %w", err)
	}
	f, err = os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("txn: reopen journal: %w", err)
	}
	j.file = f
	j.writer = bufio.NewWriter(f)
	return j, nil
}

// replay reads every journal entry, reconstructs the committed set, and
// appends rollback markers for interrupted stages.
func (j *Journal) replay() error {
	if _, err := j.file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	r := bufio.NewReader(j.file)
	begun := map[string]bool{}
	for {
		tag, err := r.ReadByte()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("txn: journal read: %w", err)
		}
		name, err := readName(r)
		if err != nil {
			return fmt.Errorf("txn: journal name: %w", err)
		}
		switch tag {
		case entryBegin:
			begun[name] = true
		case entryChecksum:
			if _, err := io.CopyN(io.Discard, r, 32); err != nil {
				return fmt.Errorf("txn: journal checksum: %w", err)
			}
		case entryCommit:
			j.committed[name] = true
			delete(begun, name)
		case entryRollback:
			delete(begun, name)
		default:
			return fmt.Errorf("txn: unknown journal tag %q at stage %q", tag, name)
		}
	}
	// Any stage still in begun state never committed: record rollback.
	for name := range begun {
		if err := j.appendEntry(entryRollback, name, nil); err != nil {
			return err
		}
	}
	return j.flush(true)
}

// Begin records the start of a stage. Re-beginning an already committed
// stage is rejected so callers cannot silently re-run history.
func (j *Journal) Begin(stage string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.committed[stage] {
		return fmt.Errorf("txn: stage %q already committed", stage)
	}
	return j.appendEntry(entryBegin, stage, nil)
}

// WriteChecksum journals the stage output checksum before the commit.
func (j *Journal) WriteChecksum(stage string, sum []byte) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if len(sum) != 32 {
		return fmt.Errorf("txn: checksum must be 32 bytes, got %d", len(sum))
	}
	return j.appendEntry(entryChecksum, stage, sum)
}

// Commit atomically marks the stage boundary. The WAL append is flushed
// and fsynced, then the boundary marker file is renamed into place.
func (j *Journal) Commit(stage string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.appendEntry(entryCommit, stage, nil); err != nil {
		return err
	}
	if err := j.flush(true); err != nil {
		return err
	}
	marker := filepath.Join(filepath.Dir(j.path), "committed.id")
	tmp := marker + ".tmp"
	if err := os.WriteFile(tmp, []byte(stage+"\n"), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, marker); err != nil {
		return err
	}
	j.committed[stage] = true
	return nil
}

// Rollback records an explicit abort for a stage that failed validation.
func (j *Journal) Rollback(stage string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.appendEntry(entryRollback, stage, nil); err != nil {
		return err
	}
	return j.flush(true)
}

// Committed reports whether a stage boundary is durable.
func (j *Journal) Committed(stage string) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.committed[stage]
}

// Boundary returns the highest committed stage name, or "" if none.
func (j *Journal) Boundary() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	best := ""
	for name := range j.committed {
		if name > best {
			best = name
		}
	}
	return best
}

// Close flushes and releases the journal file.
func (j *Journal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.flush(true); err != nil {
		return err
	}
	return j.file.Close()
}

func (j *Journal) appendEntry(tag byte, name string, extra []byte) error {
	nameBytes := []byte(name)
	if len(nameBytes) > 1<<16-1 {
		return fmt.Errorf("txn: stage name too long: %d", len(nameBytes))
	}
	var lenBuf [2]byte
	binary.BigEndian.PutUint16(lenBuf[:], uint16(len(nameBytes)))
	if err := j.writer.WriteByte(tag); err != nil {
		return err
	}
	if _, err := j.writer.Write(lenBuf[:]); err != nil {
		return err
	}
	if _, err := j.writer.Write(nameBytes); err != nil {
		return err
	}
	if len(extra) > 0 {
		if _, err := j.writer.Write(extra); err != nil {
			return err
		}
	}
	return j.flush(false)
}

func (j *Journal) flush(fsync bool) error {
	if err := j.writer.Flush(); err != nil {
		return err
	}
	if fsync {
		return j.file.Sync()
	}
	return nil
}

func readName(r *bufio.Reader) (string, error) {
	var lenBuf [2]byte
	if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
		return "", err
	}
	n := binary.BigEndian.Uint16(lenBuf[:])
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", err
	}
	return string(buf), nil
}
