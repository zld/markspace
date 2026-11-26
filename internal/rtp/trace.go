// Package rtp reads length-prefixed RTP traces and reorders them with a
// bounded 64-packet sequence window. The on-disk trace format is:
//
//	magic "RTP1" | record (uint32 BE length | RTP packet bytes) | ...
//
// The reader never interprets packet payloads as commands and rejects
// over-long records so a corrupt length field cannot exhaust memory.
package rtp

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"io"

	"github.com/pion/rtp"

	"rtpregress/internal/clock"
)

// ErrTraceTruncated marks a trace whose final record is incomplete. It is
// recoverable: every complete record before it has already been returned.
var ErrTraceTruncated = errors.New("rtp: truncated final record")

// Magic is the four-byte trace file prefix.
var Magic = [4]byte{'R', 'T', 'P', '1'}

// MaxRecordLen bounds a single length-prefixed record. TS-over-RTP carries
// at most seven 188-byte TS packets plus RTP headers, so 4 KiB is ample.
const MaxRecordLen = 4096

// Record is one parsed trace entry plus its zero-based record offset.
type Record struct {
	Packet *rtp.Packet
	Offset int64 // byte offset of the record's length prefix
	Index  int64 // zero-based record index
}

// Reader streams records from an underlying io.Reader.
type Reader struct {
	br        *bufio.Reader
	offset    int64
	index     int64
	started   bool
	truncated bool
	truncOff  int64
}

// NewReader wraps r. The magic header is consumed lazily on the first Read.
func NewReader(r io.Reader) *Reader {
	return &Reader{br: bufio.NewReaderSize(r, 64*1024)}
}

// Truncated reports whether the input ended inside a final record, and the
// byte offset at which the truncation began. Recovered records remain valid.
func (rd *Reader) Truncated() (offset int64, yes bool) {
	return rd.truncOff, rd.truncated
}

func (rd *Reader) checkMagic() error {
	var magic [4]byte
	if _, err := io.ReadFull(rd.br, magic[:]); err != nil {
		return clock.New(clock.CodeRTPSync, "rtp", rd.offset, 0, 0,
			"reading trace magic: %v", err)
	}
	rd.offset += 4
	if magic != Magic {
		return clock.New(clock.CodeRTPSync, "rtp", 0, 0, 0,
			"bad trace magic %x, want %x", magic, Magic)
	}
	rd.started = true
	return nil
}

// Read returns the next record or io.EOF at clean end of input. A truncated
// final record yields ErrTraceTruncated after every complete record preceding
// it has been returned normally.
func (rd *Reader) Read() (*Record, error) {
	if !rd.started {
		if err := rd.checkMagic(); err != nil {
			return nil, err
		}
	}
	recOffset := rd.offset
	var lenBuf [4]byte
	if _, err := io.ReadFull(rd.br, lenBuf[:]); err != nil {
		if err == io.ErrUnexpectedEOF {
			rd.truncated, rd.truncOff = true, recOffset
			return nil, ErrTraceTruncated
		}
		return nil, err // clean io.EOF propagates unchanged
	}
	rd.offset += 4
	n := int64(binary.BigEndian.Uint32(lenBuf[:]))
	if n <= 0 || n > MaxRecordLen {
		return nil, clock.New(clock.CodeLimitExceeded, "rtp", recOffset, 0, 0,
			"record length %d outside (0,%d]", n, MaxRecordLen)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(rd.br, buf); err != nil {
		rd.truncated, rd.truncOff = true, recOffset
		return nil, ErrTraceTruncated
	}
	rd.offset += n
	pkt := &rtp.Packet{}
	if err := pkt.Unmarshal(buf); err != nil {
		return nil, clock.New(clock.CodeRTPSync, "rtp", recOffset, 0, 0,
			"RTP unmarshal failed: %v", err)
	}
	rec := &Record{Packet: pkt, Offset: recOffset, Index: rd.index}
	rd.index++
	return rec, nil
}

// ReadAll drains the reader until EOF, honoring ctx cancellation.
func ReadAll(ctx context.Context, r io.Reader) ([]*Record, error) {
	recs, _, err := ReadAllWithTruncation(ctx, r)
	return recs, err
}

// ReadAllWithTruncation is ReadAll plus the truncation flag and offset. A
// truncated tail is recoverable: complete records are returned and truncated
// is true, while the error stays nil. Other faults still return an error.
func ReadAllWithTruncation(ctx context.Context, r io.Reader) (recs []*Record, truncOffset int64, err error) {
	rd := NewReader(r)
	var out []*Record
	for {
		if cerr := ctx.Err(); cerr != nil {
			return out, rd.truncOff, clock.New(clock.CodeCanceled, "rtp", rd.offset, 0, 0,
				"read canceled: %v", cerr)
		}
		rec, rerr := rd.Read()
		if rerr == io.EOF {
			return out, -1, nil
		}
		if rerr == ErrTraceTruncated {
			return out, rd.truncOff, nil
		}
		if rerr != nil {
			return out, rd.truncOff, rerr
		}
		out = append(out, rec)
	}
}
