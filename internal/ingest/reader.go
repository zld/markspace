package ingest

import (
	"bufio"
	"encoding/binary"
	"errors"
	"io"
)

// Frame type tags occupy the first byte of every length-framed record.
const (
	TagRTP  byte = 1
	TagRTCP byte = 2
)

// Frame is one length-prefixed record together with its raw stream offset.
// FrameOffset points at the tag byte; PayloadOffset points past the header.
type Frame struct {
	Type          byte
	FrameOffset   int64
	PayloadOffset int64
	Payload       []byte
}

// Reader scans a stream framed as: [1-byte tag][4-byte big-endian length][payload].
// All offsets are relative to the start of the stream and are attached to
// downstream parse errors for byte-accurate corruption location.
type Reader struct {
	r   *bufio.Reader
	pos int64
}

// NewReader wraps an io.Reader. A buffered reader keeps per-frame allocations
// bounded even when the caller hands over an unbuffered file handle.
func NewReader(r io.Reader) *Reader {
	return &Reader{r: bufio.NewReaderSize(r, 1<<16)}
}

// Next returns the next frame or an error. io.EOF is returned unwrapped at a
// clean stream boundary; every other failure is a *ParseError with an offset.
func (rd *Reader) Next() (*Frame, error) {
	tag, err := rd.r.ReadByte()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, io.EOF
		}
		return nil, &ParseError{Code: ErrTruncated, Offset: rd.pos, Stage: StageParse, Err: err}
	}
	frameStart := rd.pos
	rd.pos++

	if tag != TagRTP && tag != TagRTCP {
		return nil, &ParseError{Code: ErrUnknownFrame, Offset: frameStart, Stage: StageParse}
	}

	var lenBuf [4]byte
	n, err := io.ReadFull(rd.r, lenBuf[:])
	rd.pos += int64(n)
	if err != nil {
		return nil, &ParseError{Code: ErrTruncated, Offset: frameStart, Stage: StageParse,
			Err: errors.New("length header incomplete")}
	}
	length := binary.BigEndian.Uint32(lenBuf[:])
	if length == 0 {
		return nil, &ParseError{Code: ErrBadLength, Offset: frameStart, Stage: StageParse,
			Err: errors.New("zero-length frame")}
	}
	if length > MaxFramePayload {
		return nil, &ParseError{Code: ErrPayloadTooLarge, Offset: frameStart, Stage: StageParse,
			Err: errors.New("frame exceeds 64 KiB")}
	}

	payload := make([]byte, length)
	n, err = io.ReadFull(rd.r, payload)
	rd.pos += int64(n)
	if err != nil {
		return nil, &ParseError{Code: ErrTruncated, Offset: frameStart, Stage: StageParse,
			Err: errors.New("payload shorter than declared length")}
	}
	return &Frame{
		Type:          tag,
		FrameOffset:   frameStart,
		PayloadOffset: rd.pos - int64(length),
		Payload:       payload,
	}, nil
}

// Offset returns the number of stream bytes consumed so far.
func (rd *Reader) Offset() int64 { return rd.pos }

// WriteFrame encodes one record using the stream framing convention. It is
// shared by the fixture generator and the tests so both sides stay in sync.
func WriteFrame(w io.Writer, tag byte, payload []byte) error {
	header := [5]byte{tag}
	binary.BigEndian.PutUint32(header[1:], uint32(len(payload)))
	if _, err := w.Write(header[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}
