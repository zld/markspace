// Package txn implements the write-ahead log and atomic commit protocol used
// by every pipeline stage. Each stage writes BEGIN/CHECK/COMMIT records
// before its output becomes visible; an interrupted stage is rolled back to
// the last committed boundary on recovery.
package txn

import (
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"

	"medkernel/media"
)

// RecordType enumerates WAL record kinds.
type RecordType uint8

const (
	RecBegin    RecordType = 1
	RecCheck    RecordType = 2
	RecCommit   RecordType = 3
	RecRollback RecordType = 4
)

// JournalName is the fixed WAL filename inside a run directory.
const JournalName = "journal.wal"

// Record is one framed journal entry.
type Record struct {
	Seq   int64
	Type  RecordType
	Stage string
	Data  []byte // JSON-encoded payload
}

// Journal is an append-only WAL. Writes are framed and CRC-protected so a
// torn tail (process killed mid-write) is detected and ignored on replay.
type Journal struct {
	path string
	f    *os.File
	seq  int64
}

// OpenJournal opens or creates the WAL inside dir.
func OpenJournal(dir string) (*Journal, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, media.Wrap(media.ClassIO, "txn.journal", err, "create run dir")
	}
	path := filepath.Join(dir, JournalName)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, media.Wrap(media.ClassIO, "txn.journal", err, "open journal")
	}
	return &Journal{path: path, f: f}, nil
}

// Path returns the WAL location.
func (j *Journal) Path() string { return j.path }

// Append frames, writes and fsyncs one record.
func (j *Journal) Append(t RecordType, stage string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return media.Wrap(media.ClassIO, "txn.journal", err, "encode record")
	}
	j.seq++
	rec := Record{Seq: j.seq, Type: t, Stage: stage, Data: data}
	frame, err := encodeRecord(rec)
	if err != nil {
		return err
	}
	if _, err := j.f.Write(frame); err != nil {
		return media.Wrap(media.ClassIO, "txn.journal", err, "append record")
	}
	if err := j.f.Sync(); err != nil {
		return media.Wrap(media.ClassIO, "txn.journal", err, "fsync journal")
	}
	return nil
}

// Close flushes and releases the WAL file.
func (j *Journal) Close() error {
	if j == nil || j.f == nil {
		return nil
	}
	err := j.f.Close()
	j.f = nil
	if err != nil {
		return media.Wrap(media.ClassIO, "txn.journal", err, "close journal")
	}
	return nil
}

// Replay reads every intact record. A torn or corrupt trailing frame ends the
// replay cleanly instead of failing the whole recovery.
func Replay(dir string) ([]Record, error) {
	f, err := os.Open(filepath.Join(dir, JournalName))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, media.Wrap(media.ClassIO, "txn.journal", err, "replay open")
	}
	defer f.Close()
	var out []Record
	for {
		rec, err := decodeRecord(f)
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
		if err != nil {
			// Corrupt frame: stop here; everything before it stays valid.
			if me := media.AsMedia(err); me != nil && me.Class == media.ClassIO {
				break
			}
			return out, err
		}
		out = append(out, rec)
	}
	return out, nil
}

// Frame layout: magic(2) | type(1) | seq(8) | stageLen(1) | stage | jsonLen(4)
// | json | crc32(4 over all preceding bytes).
const frameMagic uint16 = 0x4A57 // "JW"

func encodeRecord(r Record) ([]byte, error) {
	if len(r.Stage) > 255 {
		return nil, media.New(media.ClassIO, "txn.journal", "stage name too long")
	}
	total := 2 + 1 + 8 + 1 + len(r.Stage) + 4 + len(r.Data) + 4
	buf := make([]byte, total)
	pos := 0
	binary.BigEndian.PutUint16(buf[pos:], frameMagic)
	pos += 2
	buf[pos] = byte(r.Type)
	pos++
	binary.BigEndian.PutUint64(buf[pos:], uint64(r.Seq))
	pos += 8
	buf[pos] = byte(len(r.Stage))
	pos++
	copy(buf[pos:], r.Stage)
	pos += len(r.Stage)
	binary.BigEndian.PutUint32(buf[pos:], uint32(len(r.Data)))
	pos += 4
	copy(buf[pos:], r.Data)
	pos += len(r.Data)
	binary.BigEndian.PutUint32(buf[pos:], crc32.ChecksumIEEE(buf[:pos]))
	return buf, nil
}

func decodeRecord(f *os.File) (Record, error) {
	header := make([]byte, 2+1+8+1)
	if _, err := io.ReadFull(f, header); err != nil {
		return Record{}, err
	}
	if binary.BigEndian.Uint16(header) != frameMagic {
		return Record{}, media.New(media.ClassIO, "txn.journal", "bad frame magic")
	}
	rec := Record{Type: RecordType(header[2]), Seq: int64(binary.BigEndian.Uint64(header[3:]))}
	stageLen := int(header[11])
	stage := make([]byte, stageLen)
	if _, err := io.ReadFull(f, stage); err != nil {
		return Record{}, media.Wrap(media.ClassIO, "txn.journal", err, "short stage")
	}
	rec.Stage = string(stage)
	lenBuf := make([]byte, 4)
	if _, err := io.ReadFull(f, lenBuf); err != nil {
		return Record{}, media.Wrap(media.ClassIO, "txn.journal", err, "short length")
	}
	dataLen := binary.BigEndian.Uint32(lenBuf)
	if dataLen > 16<<20 { // 16 MiB cap per record keeps memory bounded
		return Record{}, media.New(media.ClassIO, "txn.journal", "record too large")
	}
	body := make([]byte, dataLen+4)
	if _, err := io.ReadFull(f, body); err != nil {
		return Record{}, media.Wrap(media.ClassIO, "txn.journal", err, "short body")
	}
	got := binary.BigEndian.Uint32(body[dataLen:])
	want := crc32.ChecksumIEEE(append(append(append([]byte{}, header...), stage...),
		append(lenBuf, body[:dataLen]...)...))
	if got != want {
		return Record{}, media.New(media.ClassIO, "txn.journal", "crc mismatch")
	}
	rec.Data = body[:dataLen]
	return rec, nil
}
