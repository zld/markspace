// Package ts parses MPEG-2 transport streams: fixed-size packets, PAT/PMT
// tables, PES envelopes and 33-bit presentation timestamps.
package ts

import (
	"errors"
	"fmt"
	"io"

	"hlscheck/internal/model"
)

// Packet is one decoded 188-byte transport stream packet.
type Packet struct {
	// Offset is the byte offset of the sync byte within the source stream.
	Offset int64
	// PID is the 13-bit packet identifier.
	PID int
	// PUSI is the payload unit start indicator.
	PUSI bool
	// AdaptationControl reports whether adaptation field / payload are present:
	// 1=payload only, 2=adaptation only, 3=both, 0=reserved.
	AdaptationControl int
	// ContinuityCounter is the 4-bit per-PID counter from the header.
	ContinuityCounter int
	// Payload holds the packet payload bytes (adaptation field removed).
	Payload []byte
	// Adaptation is the raw adaptation field, nil when absent.
	Adaptation []byte
	// Discontinuity is the adaptation field discontinuity_indicator flag.
	Discontinuity bool
	// RandomAccess is the adaptation field random_access_indicator flag.
	RandomAccess bool
	raw          [model.PacketSize]byte
}

// Well-known PIDs and table identifiers.
const (
	PIDPAT  = 0x0000
	PIDNull = 0x1FFF
)

var (
	// ErrSyncByte marks a packet whose first byte is not 0x47; its offset is
	// always reported so corrupt streams can be located precisely.
	ErrSyncByte = errors.New("invalid TS sync byte")
	// ErrShortPacket means the stream ended before a full 188-byte packet.
	ErrShortPacket = errors.New("truncated TS packet at end of stream")
)

// PacketReader turns an arbitrary io.Reader into a stream of fixed-size TS
// packets. It carries at most PacketSize bytes of buffered state, so memory
// use is independent of total stream duration.
type PacketReader struct {
	r      io.Reader
	offset int64
	// buf holds a partial packet carried across Read boundaries.
	buf []byte
}

// NewPacketReader wraps r. The reader consumes r sequentially and never
// seeks, which keeps memory bounded for arbitrarily long inputs.
func NewPacketReader(r io.Reader) *PacketReader {
	return &PacketReader{r: r}
}

// Offset returns the absolute byte offset of the next packet to be read.
func (pr *PacketReader) Offset() int64 { return pr.offset }

// fill reads until buf holds exactly n bytes, preserving any partial packet
// already buffered. io.EOF with zero new bytes signals a clean end.
func (pr *PacketReader) fill(n int) ([]byte, error) {
	for len(pr.buf) < n {
		// Grow into the fixed raw array backing storage via the caller; here
		// we only keep the residual slice, so capacity never exceeds 188.
		need := n - len(pr.buf)
		tmp := make([]byte, need)
		m, err := io.ReadFull(pr.r, tmp)
		pr.buf = append(pr.buf, tmp[:m]...)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				if len(pr.buf) == 0 {
					return pr.buf, io.EOF
				}
				return pr.buf, ErrShortPacket
			}
			return pr.buf, err
		}
	}
	return pr.buf, nil
}

// ReadPacket returns the next packet, transparently stitching packets split
// across underlying read chunks. A malformed sync byte is reported together
// with its absolute stream offset.
func (pr *PacketReader) ReadPacket() (*Packet, error) {
	buf, err := pr.fill(model.PacketSize)
	if err != nil {
		return nil, err
	}
	pktOffset := pr.offset
	var raw [model.PacketSize]byte
	copy(raw[:], buf)
	pr.buf = pr.buf[:0]
	pr.offset += model.PacketSize

	if raw[0] != model.SyncByte {
		return nil, fmt.Errorf("%w at offset %d: got 0x%02x want 0x%02x",
			ErrSyncByte, pktOffset, raw[0], model.SyncByte)
	}
	p := &Packet{Offset: pktOffset}
	p.decodeHeader(&raw)
	return p, nil
}

// decodeHeader parses the 4-byte TS header plus adaptation field and slices
// the payload out of the fixed raw packet array.
func (p *Packet) decodeHeader(raw *[model.PacketSize]byte) {
	p.raw = *raw
	h := uint32(raw[0])<<24 | uint32(raw[1])<<16 | uint32(raw[2])<<8 | uint32(raw[3])
	p.PUSI = raw[1]&0x40 != 0
	p.PID = int((h >> 8) & 0x1FFF)
	p.AdaptationControl = int((h >> 4) & 0x03)
	p.ContinuityCounter = int(h & 0x0F)

	pos := 4
	if p.AdaptationControl == 2 || p.AdaptationControl == 3 {
		if pos < model.PacketSize {
			al := int(raw[pos])
			pos++
			if al > 0 && pos+al <= model.PacketSize {
				p.Adaptation = raw[pos : pos+al]
				if al >= 1 {
					flags := raw[pos]
					p.Discontinuity = flags&0x80 != 0
					p.RandomAccess = flags&0x40 != 0
				}
				pos += al
			} else {
				// Malformed adaptation length: treat the rest as absent
				// rather than slicing out of bounds.
				pos = model.PacketSize
			}
		}
	}
	if (p.AdaptationControl == 1 || p.AdaptationControl == 3) && pos < model.PacketSize {
		p.Payload = raw[pos:]
	}
}

// Raw returns the full 188-byte packet for byte-exact segment writing.
func (p *Packet) Raw() []byte { return p.raw[:] }
