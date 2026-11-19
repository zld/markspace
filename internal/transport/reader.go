// Package transport reads 188-byte MPEG-TS packets. Packet framing and header
// demultiplexing are delegated to go-astits; the original bytes are retained
// so the slicer can write packets back byte-for-byte.
package transport

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/asticode/go-astits"
)

// PacketSize is the only packet size accepted by this tool.
const PacketSize = 188

const syncByte = 0x47

var (
	// ErrBadSync means a packet did not begin with 0x47.
	ErrBadSync = errors.New("transport: missing sync byte")
	// ErrPartialTail means the input was not a whole number of packets.
	ErrPartialTail = errors.New("transport: trailing partial packet")
)

// Packet is one TS packet with its raw bytes retained for byte-accurate
// slicing and with the header fields the inspector needs.
type Packet struct {
	Raw       [PacketSize]byte
	Offset    int64 // byte offset of this packet in the input
	PID       uint16
	CC        uint8
	PUSI      bool
	TEI       bool
	HasAF     bool
	HasPay    bool
	Scrambled bool
	// Discontinuity is the adaptation_field discontinuity_indicator.
	Discontinuity bool
	// AFOnly is true for an adaptation-field packet without payload.
	AFOnly  bool
	Payload []byte
}

// Reader reads packets one at a time. A trailing short read is returned as
// PartialTailError together with the number of leftover bytes.
type Reader struct {
	dmx  *astits.Demuxer
	feed *packetFeeder
	off  int64
}

// NewReader wraps r.
func NewReader(r io.Reader) *Reader {
	f := &packetFeeder{r: r}
	dmx := astits.NewDemuxer(context.Background(), f, astits.DemuxerOptPacketSize(PacketSize))
	return &Reader{dmx: dmx, feed: f}
}

// Next returns the next packet.
func (r *Reader) Next() (*Packet, error) {
	p, err := r.dmx.NextPacket()
	if err != nil {
		if errors.Is(err, astits.ErrNoMorePackets) {
			if r.feed.tail > 0 {
				return nil, &PartialTailError{Offset: r.off, Bytes: r.feed.tail, Err: ErrPartialTail}
			}
			return nil, io.EOF
		}
		if errors.Is(err, astits.ErrPacketMustStartWithASyncByte) {
			return nil, &SyncError{Offset: r.off, Byte: r.feed.last[0], Err: ErrBadSync}
		}
		return nil, fmt.Errorf("transport: demuxing packet at offset %d: %w", r.off, err)
	}

	// Raw packet bytes as delivered to go-astits. The feeder keeps them so
	// this tool never re-reads the input, and slicing stays byte-accurate.
	pkt := &Packet{Offset: r.off}
	copy(pkt.Raw[:], r.feed.last[:])
	pkt.TEI = p.Header.TransportErrorIndicator
	pkt.PUSI = p.Header.PayloadUnitStartIndicator
	pkt.PID = p.Header.PID
	pkt.Scrambled = p.Header.TransportScramblingControl != astits.ScramblingControlNotScrambled
	pkt.CC = p.Header.ContinuityCounter
	pkt.HasAF = p.Header.HasAdaptationField
	pkt.HasPay = p.Header.HasPayload && len(p.Payload) > 0
	pkt.AFOnly = pkt.HasAF && !pkt.HasPay
	if p.AdaptationField != nil {
		pkt.Discontinuity = p.AdaptationField.DiscontinuityIndicator
	}
	if pkt.HasPay {
		pkt.Payload = append([]byte(nil), p.Payload...)
	}
	r.off += PacketSize
	return pkt, nil
}

// Offset is the offset of the next unread packet.
func (r *Reader) Offset() int64 { return r.off }

// packetFeeder hands go-astits exactly one 188-byte packet per Read and keeps
// the bytes of the most recent delivery. That lets the caller preserve raw
// packets without a second pass over the input.
type packetFeeder struct {
	r    io.Reader
	buf  [PacketSize]byte
	last [PacketSize]byte
	tail int
	err  error
}

func (f *packetFeeder) Read(p []byte) (int, error) {
	if f.err != nil {
		return 0, f.err
	}
	if len(p) != PacketSize {
		f.err = fmt.Errorf("transport: go-astits requested %d bytes, want %d", len(p), PacketSize)
		return 0, f.err
	}
	n, err := io.ReadFull(f.r, f.buf[:])
	if n > 0 {
		copy(p, f.buf[:n])
		copy(f.last[:], f.buf[:n])
	}
	switch {
	case err == nil:
		return PacketSize, nil
	case err == io.ErrUnexpectedEOF:
		// Keep the leftover count, surface a clean EOF to go-astits.
		f.tail = n
		f.err = io.EOF
		return 0, io.EOF
	default:
		f.err = err
		return 0, err
	}
}

// PartialTailError reports leftover bytes that cannot form a packet.
type PartialTailError struct {
	Offset int64
	Bytes  int
	Err    error
}

func (e *PartialTailError) Error() string {
	return fmt.Sprintf("%v: %d bytes at offset %d", e.Err, e.Bytes, e.Offset)
}

func (e *PartialTailError) Unwrap() error { return e.Err }

// SyncError reports a packet whose first byte was not 0x47.
type SyncError struct {
	Offset int64
	Byte   byte
	Err    error
}

func (e *SyncError) Error() string {
	return fmt.Sprintf("%v: 0x%02x at offset %d", ErrBadSync, e.Byte, e.Offset)
}

func (e *SyncError) Unwrap() error { return ErrBadSync }
