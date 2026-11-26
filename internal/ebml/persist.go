package ebml

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
)

// On-disk index layout:
//
//	magic(4)="MTID" version(1)=1 sha256(32) count(uint32 LE)
//	records: fixed 40-byte little-endian IndexEntry values
//	trailer: sha256(32) over magic..end-of-records, then magic(4)="DONE"
//
// A missing trailer or a checksum mismatch means the index is corrupt and
// must be rebuilt. Partial files never carry the DONE marker.
const (
	indexMagic   = "MTID"
	indexDone    = "DONE"
	indexVersion = byte(1)
	entrySize    = 40
	headerSize   = 4 + 1 + 32 + 4
	trailerSize  = 32 + 4
)

// IndexEntry is the fixed-width on-disk representation of one block.
type IndexEntry struct {
	TrackNo    uint64
	TimeNs     int64
	ElemOffset int64
	DataOffset int64
	DataSize   int64
}

func (e IndexEntry) marshal(b []byte) {
	binary.LittleEndian.PutUint64(b[0:8], e.TrackNo)
	binary.LittleEndian.PutUint64(b[8:16], uint64(e.TimeNs))
	binary.LittleEndian.PutUint64(b[16:24], uint64(e.ElemOffset))
	binary.LittleEndian.PutUint64(b[24:32], uint64(e.DataOffset))
	binary.LittleEndian.PutUint64(b[32:40], uint64(e.DataSize))
}

func unmarshalIndexEntry(b []byte) IndexEntry {
	return IndexEntry{
		TrackNo:    binary.LittleEndian.Uint64(b[0:8]),
		TimeNs:     int64(binary.LittleEndian.Uint64(b[8:16])),
		ElemOffset: int64(binary.LittleEndian.Uint64(b[16:24])),
		DataOffset: int64(binary.LittleEndian.Uint64(b[24:32])),
		DataSize:   int64(binary.LittleEndian.Uint64(b[32:40])),
	}
}

// IndexBuilder streams entries into a temp file and finalizes it. An
// interrupted build leaves only the temp file, which is never read.
type IndexBuilder struct {
	tmp       *os.File
	tmpPath   string
	finalPath string
	sum       hash.Hash
	count     uint32
}

// NewIndexBuilder creates path+".tmp" and writes the header.
func NewIndexBuilder(path string, inputSHA []byte) (*IndexBuilder, error) {
	tmpPath := path + ".tmp"
	tmp, err := os.Create(tmpPath)
	if err != nil {
		return nil, err
	}
	hdr := make([]byte, 0, headerSize)
	hdr = append(hdr, indexMagic...)
	hdr = append(hdr, indexVersion)
	hdr = append(hdr, inputSHA...)
	hdr = binary.LittleEndian.AppendUint32(hdr, 0)
	b := &IndexBuilder{tmp: tmp, tmpPath: tmpPath, finalPath: path, sum: sha256.New()}
	if err := b.write(hdr); err != nil {
		return nil, b.abort(err)
	}
	return b, nil
}

// NewIndexBuilderAt creates a builder with an explicit temp path and final
// publication path, allowing callers to keep temp files outside the index
// directory tree.
func NewIndexBuilderAt(tmpPath, finalPath string, inputSHA []byte) (*IndexBuilder, error) {
	tmp, err := os.Create(tmpPath)
	if err != nil {
		return nil, err
	}
	hdr := make([]byte, 0, headerSize)
	hdr = append(hdr, indexMagic...)
	hdr = append(hdr, indexVersion)
	hdr = append(hdr, inputSHA...)
	hdr = binary.LittleEndian.AppendUint32(hdr, 0)
	b := &IndexBuilder{tmp: tmp, tmpPath: tmpPath, finalPath: finalPath, sum: sha256.New()}
	if err := b.write(hdr); err != nil {
		return nil, b.abort(err)
	}
	return b, nil
}

func (b *IndexBuilder) write(p []byte) error {
	if _, err := b.tmp.Write(p); err != nil {
		return err
	}
	_, err := b.sum.Write(p)
	return err
}

// Append records one block.
func (b *IndexBuilder) Append(e IndexEntry) error {
	var buf [entrySize]byte
	e.marshal(buf[:])
	b.count++
	return b.write(buf[:])
}

// Finalize writes the checksum trailer, flushes and closes the temp file.
// The file stays at its temp path; callers atomically rename it into place.
func (b *IndexBuilder) Finalize() error {
	// Backfill the entry count into the header (last 4 bytes of the header),
	// then recompute the checksum over the finalized header + records so the
	// hash covers the exact published bytes.
	var countBuf [4]byte
	binary.LittleEndian.PutUint32(countBuf[:], b.count)
	if _, err := b.tmp.WriteAt(countBuf[:], int64(headerSize-4)); err != nil {
		return b.abort(err)
	}
	sum := sha256.New()
	if _, err := b.tmp.Seek(0, io.SeekStart); err != nil {
		return b.abort(err)
	}
	if _, err := io.CopyN(sum, b.tmp, int64(headerSize)+int64(b.count)*entrySize); err != nil {
		return b.abort(err)
	}
	trailer := make([]byte, 0, trailerSize)
	trailer = sum.Sum(trailer)
	trailer = append(trailer, indexDone...)
	if _, err := b.tmp.Write(trailer); err != nil {
		return b.abort(err)
	}
	if err := b.tmp.Sync(); err != nil {
		return b.abort(err)
	}
	return b.tmp.Close()
}

// Commit finalizes and atomically publishes the index at the final path.
func (b *IndexBuilder) Commit() error {
	if err := b.Finalize(); err != nil {
		return err
	}
	if err := os.Rename(b.tmpPath, b.finalPath); err != nil {
		return b.abort(err)
	}
	return nil
}

func (b *IndexBuilder) abort(err error) error {
	b.tmp.Close()
	os.Remove(b.tmpPath)
	return err
}

// AbortForTest closes and removes the temp file without publishing,
// simulating a crashed build. Test-only helper.
func (b *IndexBuilder) AbortForTest() error {
	b.tmp.Close()
	return os.Remove(b.tmpPath)
}

// TempPath returns the temp file path the builder writes to.
func (b *IndexBuilder) TempPath() string { return b.tmpPath }

// Index is a verified, read-only block index backed by a memory-mapped-style
// file. Entries are read on demand via ReadAt, so opening a large index does
// not load it into memory.
type Index struct {
	f     *os.File
	count uint32
}

var (
	// ErrIndexCorrupt means structure or checksum verification failed.
	ErrIndexCorrupt = errors.New("index corrupt")
	// ErrIndexMismatch means the stored input SHA-256 differs from the file.
	ErrIndexMismatch = errors.New("index input mismatch")
)

// OpenIndex validates header, checksum and trailer, then returns a reader.
func OpenIndex(path string, inputSHA []byte) (*Index, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			f.Close()
		}
	}()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if fi.Size() < int64(headerSize+trailerSize) {
		return nil, fmt.Errorf("%w: truncated file", ErrIndexCorrupt)
	}
	hdr := make([]byte, headerSize)
	if _, err := io.ReadFull(f, hdr); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrIndexCorrupt, err)
	}
	if string(hdr[0:4]) != indexMagic || hdr[4] != indexVersion {
		return nil, fmt.Errorf("%w: bad magic/version", ErrIndexCorrupt)
	}
	storedSHA := hdr[5 : 5+32]
	count := binary.LittleEndian.Uint32(hdr[headerSize-4:])
	bodyLen := int64(count) * entrySize
	if fi.Size() != int64(headerSize)+bodyLen+int64(trailerSize) {
		return nil, fmt.Errorf("%w: size/count mismatch", ErrIndexCorrupt)
	}
	// Checksum over header + records.
	sum := sha256.New()
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	if _, err := io.CopyN(sum, f, int64(headerSize)+bodyLen); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrIndexCorrupt, err)
	}
	trailer := make([]byte, trailerSize)
	if _, err := io.ReadFull(f, trailer); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrIndexCorrupt, err)
	}
	if string(trailer[32:]) != indexDone || !equalBytes(sum.Sum(nil), trailer[:32]) {
		return nil, fmt.Errorf("%w: bad trailer/checksum", ErrIndexCorrupt)
	}
	if !equalBytes(storedSHA, inputSHA) {
		return nil, ErrIndexMismatch
	}
	ok = true
	return &Index{f: f, count: count}, nil
}

// Count returns the number of indexed blocks.
func (x *Index) Count() uint32 { return x.count }

// Entry reads record i (zero-based).
func (x *Index) Entry(i uint32) (IndexEntry, error) {
	if i >= x.count {
		return IndexEntry{}, fmt.Errorf("entry %d out of range", i)
	}
	var buf [entrySize]byte
	off := int64(headerSize) + int64(i)*entrySize
	if _, err := x.f.ReadAt(buf[:], off); err != nil {
		return IndexEntry{}, err
	}
	return unmarshalIndexEntry(buf[:]), nil
}

// Close releases the backing file.
func (x *Index) Close() error { return x.f.Close() }

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
