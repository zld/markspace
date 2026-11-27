// Package ingest streams length-prefixed packet records from an input
// file, tracking the raw byte offset of every record so later stages can
// report exactly where corruption or truncation was first confirmed.
package ingest

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"mediarebuild/internal/model"
)

// MaxRecordSize caps a single record at 4 MiB.
const MaxRecordSize = 4 << 20

// Record is one decoded input record with its raw byte offset.
type Record struct {
	Offset  int64  // offset of the 4-byte length header
	Payload []byte // record body (exactly the declared length)
}

// Reader streams records from r. Format per record:
//
//	uint32 big-endian length N, followed by N payload bytes.
type Reader struct {
	r   io.Reader
	off int64 // offset of the next unread byte
	buf [4]byte
}

// NewReader wraps r. off is the byte offset of r's current position within
// the original input (0 for a fresh file).
func NewReader(r io.Reader, off int64) *Reader { return &Reader{r: r, off: off} }

// Next reads one record. It returns io.EOF at a clean record boundary.
// A truncated header or payload, or an over-limit length, yields a Diag
// carrying the first confirmable offset; the reader is not silently
// skipped past bad bytes.
func (rd *Reader) Next(ctx context.Context) (Record, error) {
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	start := rd.off
	n, err := io.ReadFull(rd.r, rd.buf[:])
	rd.off += int64(n)
	if err != nil {
		if errors.Is(err, io.EOF) && n == 0 {
			return Record{}, io.EOF
		}
		// Truncated length header: first confirmable offset is start.
		return Record{}, diagTrunc(start, fmt.Sprintf("truncated record header: got %d of 4 bytes", n))
	}
	length := binary.BigEndian.Uint32(rd.buf[:])
	if length > MaxRecordSize {
		return Record{}, diagTrunc(start, fmt.Sprintf("record length %d exceeds %d byte limit", length, MaxRecordSize))
	}
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	payload := make([]byte, length)
	m, err := io.ReadFull(rd.r, payload)
	rd.off += int64(m)
	if err != nil {
		// Truncated payload: the record header at start declared bytes
		// that do not exist; report the record offset.
		return Record{}, diagTrunc(start,
			fmt.Sprintf("truncated record payload: declared %d bytes, got %d", length, m))
	}
	return Record{Offset: start, Payload: payload}, nil
}

func diagTrunc(off int64, msg string) error {
	d := model.NewDiag(model.StageIngest, "truncated-record", off, false, msg)
	d.EndOffset = off
	return d
}
