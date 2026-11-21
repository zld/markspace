// Package tsreader reads MPEG-TS transport packets (188 bytes) one at a time.
//
// Framing (sync byte detection, truncated tail handling) is implemented here so
// that structural damage is reported precisely. The exported transport header
// types and constants of github.com/asticode/go-astits are reused when
// interpreting the three-byte transport header and the payload offset.
package tsreader

import (
	"errors"
	"io"

	"github.com/asticode/go-astits"
)

// PacketSize is the fixed MPEG-TS transport packet size in bytes.
const PacketSize = astits.MpegTsPacketSize // 188

// SyncByte marks the start of every transport packet.
const SyncByte byte = 0x47

var (
	// ErrSyncByte reports a packet whose first byte is not 0x47.
	ErrSyncByte = errors.New("tsreader: missing sync byte 0x47")
	// ErrTruncated reports a trailing buffer shorter than 188 bytes.
	ErrTruncated = errors.New("tsreader: truncated trailing packet")
)

// Packet is one transport packet together with its framing metadata.
type Packet struct {
	// Index is the zero-based position of the packet in the input stream,
	// counting damaged frames as well.
	Index int64
	// Raw is the full 188-byte packet, retained verbatim for the slicer.
	Raw [PacketSize]byte
	// Header is interpreted through go-astits' exported header type.
	Header astits.PacketHeader
	// Payload is the slice of Raw carrying the packet payload (may be empty).
	Payload []byte
	// SyncOK is false when the sync byte was missing. Such a frame is still
	// counted and delivered so downstream counters stay aligned with input.
	SyncOK bool
}

// Reader pulls framed packets from an underlying byte stream.
type Reader struct {
	r        io.Reader
	buf      [PacketSize]byte
	next     int64
	trunc    int  // number of bytes of a partial trailing frame
	truncErr bool // true once ErrTruncated has been returned
}

// NewReader wraps r as a transport packet reader. Both regular files and
// standard input are supported.
func NewReader(r io.Reader) *Reader {
	return &Reader{r: r}
}

// TruncatedBytes returns the length of a partial trailing frame, if any.
func (r *Reader) TruncatedBytes() int { return r.trunc }

// Next returns the next packet. A damaged frame is returned together with a
// sentinel error; the caller may keep reading. io.EOF marks a clean end of
// stream, ErrTruncated a partial trailing frame.
func (r *Reader) Next() (*Packet, error) {
	n, err := io.ReadFull(r.r, r.buf[:])
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, io.EOF // no bytes at all: clean end of stream
		}
		if errors.Is(err, io.ErrUnexpectedEOF) {
			r.trunc = n
			r.truncErr = true
			return nil, ErrTruncated
		}
		return nil, err
	}

	p := &Packet{Index: r.next, SyncOK: r.buf[0] == SyncByte}
	r.next++
	copy(p.Raw[:], r.buf[:])

	// The transport header can still be interpreted for framing statistics;
	// callers must check SyncOK before trusting the payload.
	p.Header = parseHeader(r.buf[:])
	p.Payload = extractPayload(r.buf[:], p.Header)

	if !p.SyncOK {
		return p, ErrSyncByte
	}
	return p, nil
}

// parseHeader reads the three-byte transport header using the same bit layout
// as go-astits' internal parser and returns its exported header type.
func parseHeader(b []byte) astits.PacketHeader {
	return astits.PacketHeader{
		TransportErrorIndicator:   b[0]&0x80 > 0,
		PayloadUnitStartIndicator: b[0]&0x40 > 0,
		TransportPriority:         b[0]&0x20 > 0,
		PID:                       uint16(b[0]&0x1f)<<8 | uint16(b[1]),
		TransportScramblingControl: uint8(b[2]) >> 6 & 0x3,
		HasAdaptationField:         b[2]&0x20 > 0,
		HasPayload:                 b[2]&0x10 > 0,
		ContinuityCounter:          b[2] & 0x0f,
	}
}

// extractPayload returns the payload portion of a packet, accounting for an
// optional adaptation field. The result aliases the caller-provided buffer.
func extractPayload(b []byte, h astits.PacketHeader) []byte {
	offset := 3
	if h.HasAdaptationField {
		// Adaptation field length occupies the byte right after the header.
		offset += 1 + int(b[3])
	}
	if !h.HasPayload || offset > PacketSize {
		return nil
	}
	return b[offset:]
}
