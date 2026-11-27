// Package ingest reads the self-generated record stream.
//
// A stream is a 16 byte header followed by framed records:
//
//	magic "RTRB" | version 1 | reserved 2 bytes | start unix-nanos (8 bytes, BE)
//	record: kind 1 byte | flags 1 byte | payload length (4 bytes, BE) | payload
//
// Kind 1 holds an RTP packet, kind 2 an RTCP compound packet and kind 3 one
// client event line. Every record knows its absolute byte offset so downstream
// errors can point at the exact position in the input.
//
// Reading is bounded twice over: at most MaxStreamBytes are consumed from the
// file, and records are handed to a bounded channel so a slow consumer applies
// backpressure to the reader instead of growing memory.
package ingest

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"

	"rtp-rebuild/internal/fault"
)

const (
	// MaxStreamBytes is the fixed upper bound of bytes read from one stream.
	MaxStreamBytes = 4 << 20 // 4 MiB

	// HeaderSize is the size of the stream header.
	HeaderSize = 16

	// RecordHeaderSize is the size of one record header.
	RecordHeaderSize = 6

	// ChannelDepth bounds how many records may be queued in front of a slow
	// consumer before the reader blocks.
	ChannelDepth = 32
)

// Kind identifies the payload of a record.
type Kind uint8

const (
	KindRTP   Kind = 1
	KindRTCP  Kind = 2
	KindEvent Kind = 3
)

// String returns a short stable name.
func (k Kind) String() string {
	switch k {
	case KindRTP:
		return "rtp"
	case KindRTCP:
		return "rtcp"
	case KindEvent:
		return "event"
	default:
		return fmt.Sprintf("kind(%d)", uint8(k))
	}
}

// Record is one framed input item.
type Record struct {
	Kind   Kind
	Offset int64 // absolute byte offset of the record header
	Data   []byte
}

// PayloadOffset returns the absolute offset of the first payload byte.
func (r Record) PayloadOffset() int64 { return r.Offset + RecordHeaderSize }

// Header is the parsed file header.
type Header struct {
	StartUnixNano int64
}

// Stream is an opened input record stream.
type Stream struct {
	f      *os.File
	r      *bufio.Reader
	Header Header
	read   int64 // bytes consumed so far, including the file header
}

// Open opens path and reads the stream header.
func Open(path string) (*Stream, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fault.New(fault.StageIngest, "open "+path, err)
	}
	s := &Stream{f: f, r: bufio.NewReaderSize(f, 64<<10)}
	if err := s.readHeader(); err != nil {
		f.Close()
		return nil, err
	}
	return s, nil
}

// NewStream wraps an opener for tests and in-memory callers.
func NewStream(r io.Reader) (*Stream, error) {
	return newFromReader(bufio.NewReaderSize(r, 64<<10))
}

func newFromReader(br *bufio.Reader) (*Stream, error) {
	s := &Stream{r: br}
	if err := s.readHeader(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Stream) readHeader() error {
	buf := make([]byte, HeaderSize)
	if _, err := io.ReadFull(s.r, buf); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
			return fault.At(fault.StageIngest, "read header", s.read, fmt.Errorf("%w: stream header", fault.ErrTruncated))
		}
		return fault.At(fault.StageIngest, "read header", s.read, err)
	}
	if string(buf[0:4]) != "RTRB" {
		return fault.At(fault.StageIngest, "read header", 0, fmt.Errorf("%w: bad magic", fault.ErrCorrupt))
	}
	if buf[4] != 1 {
		return fault.At(fault.StageIngest, "read header", 4, fmt.Errorf("%w: version %d", fault.ErrUnsupported, buf[4]))
	}
	s.Header.StartUnixNano = int64(binary.BigEndian.Uint64(buf[8:16]))
	s.read = HeaderSize
	return nil
}

// ReadRecord reads the next record. It returns io.EOF at a clean end of stream.
func (s *Stream) ReadRecord() (Record, error) {
	hdr := make([]byte, RecordHeaderSize)
	n, err := io.ReadFull(s.r, hdr)
	if err != nil {
		if errors.Is(err, io.EOF) && n == 0 {
			return Record{}, io.EOF
		}
		return Record{}, fault.At(fault.StageIngest, "read record header", s.read, fmt.Errorf("%w: record header after %d bytes", fault.ErrTruncated, n))
	}
	kind := Kind(hdr[0])
	length := int64(binary.BigEndian.Uint32(hdr[2:6]))
	if kind < KindRTP || kind > KindEvent {
		return Record{}, fault.At(fault.StageIngest, "read record header", s.read, fmt.Errorf("%w: record kind %d", fault.ErrCorrupt, hdr[0]))
	}
	offset := s.read
	consumed := RecordHeaderSize + length
	if s.read+consumed > MaxStreamBytes {
		return Record{}, fault.At(fault.StageIngest, "read record", offset, fmt.Errorf("%w: stream exceeds %d bytes", fault.ErrLimit, MaxStreamBytes))
	}
	data := make([]byte, length)
	if _, err := io.ReadFull(s.r, data); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
			return Record{}, fault.At(fault.StageIngest, "read record payload", offset, fmt.Errorf("%w: declared %d payload bytes", fault.ErrTruncated, length))
		}
		return Record{}, fault.At(fault.StageIngest, "read record payload", offset, err)
	}
	s.read += consumed
	return Record{Kind: kind, Offset: offset, Data: data}, nil
}

// Close closes the underlying file, if any.
func (s *Stream) Close() error {
	if s.f != nil {
		return s.f.Close()
	}
	return nil
}

// Emit reads records until the stream ends, the byte cap is hit or ctx is
// cancelled. Records are delivered on a bounded channel and are never dropped
// silently: the first error is sent on the returned error channel once the
// record channel is closed.
//
// Cancellation closes the input file and stops the producer.
func (s *Stream) Emit(ctx context.Context) (<-chan Record, <-chan error) {
	records := make(chan Record, ChannelDepth)
	errs := make(chan error, 1)

	go func() {
		defer close(records)
		for {
			select {
			case <-ctx.Done():
				s.Close()
				errs <- fault.New(fault.StageIngest, "cancelled", ctx.Err())
				return
			default:
			}
			rec, err := s.ReadRecord()
			if errors.Is(err, io.EOF) {
				errs <- nil
				return
			}
			if err != nil {
				errs <- err
				return
			}
			select {
			case records <- rec:
			case <-ctx.Done():
				s.Close()
				errs <- fault.New(fault.StageIngest, "cancelled", ctx.Err())
				return
			}
		}
	}()
	return records, errs
}
