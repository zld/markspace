// Package tsreader reads synchronized 188-byte MPEG-TS packets from a byte
// stream and exposes the transport-layer fields parsed by go-astits while
// keeping the original 188 raw bytes available for downstream slicing.
package tsreader

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/asticode/go-astits"
)

// PacketSize is the only supported MPEG-TS packet size.
const PacketSize = 188

// SyncByte is the first byte of every MPEG-TS packet.
const SyncByte byte = 0x47

// PIDPAT is the well-known PID carrying the Program Association Table.
const PIDPAT uint16 = 0x0000

// Packet is one transport packet together with the fields the pipeline needs.
// Raw is a freshly allocated 188-byte slice so callers may retain it.
type Packet struct {
	Index                     int64 // zero-based packet index in the input stream
	Raw                       []byte
	PID                       uint16
	ContinuityCounter         uint8
	TransportError            bool
	PayloadUnitStart          bool
	HasPayload                bool
	HasAdaptationField        bool
	AdaptationDiscontinuity   bool
	Payload                   []byte
}

// Reader produces packets from an underlying io.Reader. It never buffers more
// than one 188-byte packet itself; go-astits performs the header/adaptation/
// payload parsing via a single-packet adapter reader.
type Reader struct {
	src   io.Reader
	dmx   *astits.Demuxer
	adapt *onePacketReader
	index int64
}

// NewReader wraps r as a streaming TS packet reader.
func NewReader(r io.Reader) *Reader {
	adapt := &onePacketReader{}
	return &Reader{
		src:   r,
		adapt: adapt,
		dmx: astits.NewDemuxer(
			context.Background(),
			adapt,
			astits.DemuxerOptPacketSize(PacketSize),
		),
	}
}

// Next returns the next packet. io.EOF is returned at clean end of input.
// A truncated final packet or a missing sync byte is reported as an error so
// callers can distinguish unparseable input from a clean stream end.
func (r *Reader) Next() (*Packet, error) {
	raw := make([]byte, PacketSize)
	n, err := io.ReadFull(r.src, raw)
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, io.EOF
		}
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, fmt.Errorf("truncated TS packet at byte offset %d: got %d bytes, want %d", r.index*PacketSize, n, PacketSize)
		}
		return nil, fmt.Errorf("reading TS packet %d: %w", r.index, err)
	}
	if raw[0] != SyncByte {
		return nil, fmt.Errorf("packet %d missing sync byte 0x47, got 0x%02x", r.index, raw[0])
	}

	r.adapt.set(raw)
	parsed, perr := r.dmx.NextPacket()
	if perr != nil {
		return nil, fmt.Errorf("parsing TS packet %d: %w", r.index, perr)
	}

	pkt := &Packet{
		Index:            r.index,
		Raw:              raw,
		PID:              parsed.Header.PID,
		ContinuityCounter: parsed.Header.ContinuityCounter,
		TransportError:   parsed.Header.TransportErrorIndicator,
		PayloadUnitStart: parsed.Header.PayloadUnitStartIndicator,
		HasPayload:       parsed.Header.HasPayload,
		HasAdaptationField: parsed.Header.HasAdaptationField,
		Payload:          parsed.Payload,
	}
	if parsed.AdaptationField != nil {
		pkt.AdaptationDiscontinuity = parsed.AdaptationField.DiscontinuityIndicator
	}
	r.index++
	return pkt, nil
}

// Index returns the number of packets consumed so far.
func (r *Reader) Index() int64 { return r.index }

// onePacketReader hands a single packet to go-astits and then returns EOF,
// so each Demuxer.NextPacket call parses exactly the packet we just read.
type onePacketReader struct {
	buf []byte
	off int
}

func (o *onePacketReader) set(b []byte) {
	o.buf = b
	o.off = 0
}

func (o *onePacketReader) Read(p []byte) (int, error) {
	if o.off >= len(o.buf) {
		return 0, io.EOF
	}
	n := copy(p, o.buf[o.off:])
	o.off += n
	return n, nil
}
