// Package journal provides a write-ahead log and atomic staged commits.
// Every state transition is first persisted to a CRC-checked WAL; the
// committed snapshot directory only advances once the stage's COMMIT record
// is durable, so a crash before commit rolls back while a crash after commit
// is replayed on restart.
package journal

import (
	"encoding/binary"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"sync"

	"mediaroute/internal/clock"
)

// WAL record kinds.
const (
	RecBegin  uint8 = 1
	RecData   uint8 = 2
	RecCommit uint8 = 3
	RecAbort  uint8 = 4
)

const (
	walName    = "journal.wal"
	walTmpName = "journal.wal.tmp"
	segDir     = "segments"
)

// Record is one WAL entry. Kind identifies the stage transition; Key is the
// segment or artifact name and Value its payload.
type Record struct {
	Kind  uint8
	Key   string
	Value []byte
}

// WAL is an append-only, CRC-protected write-ahead log.
type WAL struct {
	mu sync.Mutex
	f  *os.File
}

// CreateWAL opens (or creates) the WAL inside dir.
func CreateWAL(dir string) (*WAL, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, clock.E(clock.PhaseJournal, 0, -1, true, "cannot create journal dir", err)
	}
	f, err := os.OpenFile(filepath.Join(dir, walName),
		os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return nil, clock.E(clock.PhaseJournal, 0, -1, true, "cannot open WAL", err)
	}
	return &WAL{f: f}, nil
}

// Append writes and fsyncs one record.
func (w *WAL) Append(rec Record) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := writeRecord(w.f, rec); err != nil {
		return err
	}
	if err := w.f.Sync(); err != nil {
		return clock.E(clock.PhaseJournal, 0, -1, true, "WAL fsync failed", err)
	}
	return nil
}

// Close releases the file handle.
func (w *WAL) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}

func writeRecord(w io.Writer, rec Record) error {
	if len(rec.Key) > 1<<16-1 || len(rec.Value) > 1<<32-1 {
		return clock.E(clock.PhaseJournal, 0, -1, false, "WAL record too large", nil)
	}
	header := make([]byte, 14)
	header[0] = rec.Kind
	binary.BigEndian.PutUint16(header[1:3], uint16(len(rec.Key)))
	binary.BigEndian.PutUint32(header[3:7], uint32(len(rec.Value)))
	// CRC covers kind + key + value; bytes 11..13 are reserved.
	binary.BigEndian.PutUint32(header[7:11], crc32.ChecksumIEEE(
		append(append([]byte{rec.Kind}, rec.Key...), rec.Value...)))
	if _, err := w.Write(header); err != nil {
		return clock.E(clock.PhaseJournal, 0, -1, true, "WAL header write failed", err)
	}
	if _, err := io.WriteString(w, rec.Key); err != nil {
		return clock.E(clock.PhaseJournal, 0, -1, true, "WAL key write failed", err)
	}
	if _, err := w.Write(rec.Value); err != nil {
		return clock.E(clock.PhaseJournal, 0, -1, true, "WAL value write failed", err)
	}
	return nil
}

// ReadWAL replays a WAL. Records after the first CRC or framing failure are
// treated as an uncommitted tail and excluded; the valid prefix plus the
// truncation offset is returned so the caller can physically isolate it.
func ReadWAL(dir string) (valid []Record, truncatedAt int64, err error) {
	f, err := os.Open(filepath.Join(dir, walName))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, nil
		}
		return nil, 0, clock.E(clock.PhaseJournal, 0, -1, true, "cannot read WAL", err)
	}
	defer f.Close()

	var offset int64
	for {
		header := make([]byte, 14)
		n, rerr := io.ReadFull(f, header)
		if rerr == io.EOF && n == 0 {
			return valid, 0, nil // clean end of log
		}
		if rerr != nil {
			return valid, offset, clock.E(clock.PhaseJournal, 0, -1, true,
				"truncated WAL record header; isolating uncommitted tail", rerr)
		}
		keyLen := binary.BigEndian.Uint16(header[1:3])
		valLen := binary.BigEndian.Uint32(header[3:7])
		wantCRC := binary.BigEndian.Uint32(header[7:11])
		body := make([]byte, int(keyLen)+int(valLen))
		if _, rerr = io.ReadFull(f, body); rerr != nil {
			return valid, offset, clock.E(clock.PhaseJournal, 0, -1, true,
				"truncated WAL body; isolating uncommitted tail", rerr)
		}
		gotCRC := crc32.ChecksumIEEE(append([]byte{header[0]}, body...))
		if gotCRC != wantCRC {
			return valid, offset, clock.E(clock.PhaseJournal, 0, -1, false,
				"WAL CRC mismatch; isolating uncommitted tail", nil)
		}
		valid = append(valid, Record{
			Kind:  header[0],
			Key:   string(body[:keyLen]),
			Value: body[keyLen:],
		})
		offset += 14 + int64(len(body))
	}
}

// IsolateTail truncates the WAL to goodLen, the byte offset of the last valid
// record, so a corrupt or partial uncommitted tail can never be replayed.
func IsolateTail(dir string, goodLen int64) error {
	path := filepath.Join(dir, walName)
	if err := os.Truncate(path, goodLen); err != nil {
		return clock.E(clock.PhaseJournal, 0, -1, true, "WAL tail isolation failed", err)
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		return clock.E(clock.PhaseJournal, 0, -1, true, "WAL reopen after isolation failed", err)
	}
	defer f.Close()
	return f.Sync()
}

// Reset removes the WAL and any staged artifacts, used after successful
// replay or explicit abort.
func Reset(dir string) error {
	if err := os.RemoveAll(filepath.Join(dir, walName)); err != nil {
		return clock.E(clock.PhaseJournal, 0, -1, true, "WAL reset failed", err)
	}
	if err := os.RemoveAll(filepath.Join(dir, walTmpName)); err != nil {
		return clock.E(clock.PhaseJournal, 0, -1, true, "staging cleanup failed", err)
	}
	return nil
}
