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
	// Payload is the packet payload. For PUSI packets carrying PSI sections it
	// starts at the section bytes: the one-byte pointer_field is removed here
	// once so every downstream consumer sees identical alignment.
	Payload []byte
	// PointerField is the raw pointer_field value of a PUSI packet (-1 when
	// the packet has no payload or PUSI is clear).
	PointerField int
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

	p := &Packet{Index: r.next, SyncOK: r.buf[0] == SyncByte, PointerField: -1}
	r.next++
	copy(p.Raw[:], r.buf[:])

	// The transport header can still be interpreted for framing statistics;
	// callers must check SyncOK before trusting the payload.
	p.Header = parseHeader(r.buf[:])
	payload := extractPayload(r.buf[:], p.Header)

	// PSI section-start packets prefix the section with a one-byte
	// pointer_field. Strip it centrally so reassembly always sees the section
	// beginning at payload[0]. Non-zero pointers also carry tail bytes of a
	// previous section, which are not needed for forward reassembly.
	if p.Header.HasPayload && p.Header.PayloadUnitStartIndicator && len(payload) >= 1 {
		pointer := int(payload[0])
		p.PointerField = pointer
		if pointer+1 <= len(payload) {
			payload = payload[1+pointer:]
		} else {
			payload = nil
		}
	}
	p.Payload = payload

	if !p.SyncOK {
		return p, ErrSyncByte
	}
	return p, nil
}

// parseHeader reads the transport header of the full 188-byte packet b.
// Layout: b[0] sync 0x47; b[1] TEI/PUSI/priority + high 5 PID bits; b[2] low
// 8 PID bits; b[3] scrambling + adaptation/payload flags + 4-bit CC.
func parseHeader(b []byte) astits.PacketHeader {
	return astits.PacketHeader{
		TransportErrorIndicator:    b[1]&0x80 > 0,
		PayloadUnitStartIndicator:  b[1]&0x40 > 0,
		TransportPriority:          b[1]&0x20 > 0,
		PID:                        uint16(b[1]&0x1f)<<8 | uint16(b[2]),
		TransportScramblingControl: (b[3] >> 6) & 0x3,
		HasAdaptationField:         b[3]&0x20 > 0,
		HasPayload:                 b[3]&0x10 > 0,
		ContinuityCounter:          b[3] & 0x0f,
	}
}

// extractPayload returns the payload portion of a packet. Without an
// adaptation field the payload starts at b[4] (after the four-byte transport
// header). With one, b[4] is its length byte and the field follows.
func extractPayload(b []byte, h astits.PacketHeader) []byte {
	offset := 4
	if h.HasAdaptationField {
		offset += 1 + int(b[4])
	}
	if !h.HasPayload || offset > PacketSize {
		return nil
	}
	return b[offset:]
}
