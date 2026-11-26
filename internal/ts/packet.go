// Package ts demultiplexes MPEG-TS into tracks and access units without
// decoding. Every sample records the raw file offset of its first payload
// byte, so a remuxer can copy bytes verbatim.
package ts

import (
	"encoding/binary"
	"fmt"
	"io"

	"liveregress/internal/fault"
)

// PacketSize is the fixed MPEG-TS packet length.
const PacketSize = 188

// syncByte begins every transport packet.
const syncByte = 0x47

// Packet is a parsed 188-byte transport packet.
type Packet struct {
	Index          int64 // zero-based packet index
	Offset         int64 // raw byte offset of the sync byte
	PID            uint16
	PUSI           bool
	TransportError bool
	Scrambled      bool
	HasPayload     bool
	Adaptation     bool
	Continuity     uint8
	Payload        []byte // payload bytes, no adaptation field
	PayloadOffset  int64  // raw offset of Payload[0] in the file
	Discontinuity  bool
}

// parsePacket decodes one packet from buf, which must be exactly PacketSize.
func parsePacket(buf []byte, index, offset int64) (Packet, error) {
	p := Packet{Index: index, Offset: offset, PayloadOffset: offset + 4}
	if len(buf) != PacketSize {
		return p, fmt.Errorf("short packet: %d bytes", len(buf))
	}
	if buf[0] != syncByte {
		return p, fmt.Errorf("bad sync byte 0x%02x at %d", buf[0], offset)
	}
	if buf[1]&0x80 != 0 {
		p.TransportError = true
	}
	p.PUSI = buf[1]&0x40 != 0
	p.PID = binary.BigEndian.Uint16(buf[1:3]) & 0x1FFF
	if buf[1]&0x20 != 0 {
		p.Scrambled = true
	}
	afc := (buf[3] >> 4) & 0x03
	p.Continuity = buf[3] & 0x0F
	pos := 4
	switch afc {
	case 0:
		// reserved: no payload, no adaptation
	case 1:
		p.HasPayload = true
	case 2:
		p.Adaptation = true
	case 3:
		p.Adaptation = true
		p.HasPayload = true
	}
	if p.Adaptation {
		if pos >= PacketSize {
			return p, fmt.Errorf("adaptation field missing at %d", offset)
		}
		afLen := int(buf[pos])
		pos++
		if pos+afLen > PacketSize {
			return p, fmt.Errorf("adaptation field overruns packet at %d", offset)
		}
		if afLen > 0 {
			p.Discontinuity = buf[pos]&0x80 != 0
		}
		pos += afLen
	}
	if p.HasPayload {
		if pos > PacketSize {
			return p, fmt.Errorf("payload offset past packet at %d", offset)
		}
		p.Payload = buf[pos:PacketSize]
		p.PayloadOffset = offset + int64(pos)
	}
	return p, nil
}

// PacketReader streams transport packets with a bounded resynchronisation
// window. It never buffers more than one packet.
type PacketReader struct {
	r       io.Reader
	path    string
	buf     []byte
	pos     int64
	index   int64
	scratch [PacketSize]byte
	eofSeen bool
}

// NewPacketReader wraps r for path. A 188-byte aligned stream is expected;
// leading garbage is skipped only up to MaxResyncBytes and is reported.
func NewPacketReader(r io.Reader, path string) *PacketReader {
	return &PacketReader{r: r, path: path, buf: make([]byte, 0, PacketSize*2)}
}

// MaxResyncBytes bounds how far the reader scans for a sync byte at start.
const MaxResyncBytes = 188 * 4

// Next returns the next valid packet. A tail shorter than a packet yields a
// recoverable truncate fault; unreadable input is a read fault.
func (pr *PacketReader) Next() (*Packet, error) {
	n, err := io.ReadFull(pr.r, pr.scratch[:])
	if err != nil {
		if err == io.EOF {
			return nil, io.EOF
		}
		if err == io.ErrUnexpectedEOF {
			return nil, fault.New(fault.KindTruncate, pr.path, pr.pos,
				fmt.Sprintf("tail of %d bytes is shorter than a TS packet", n)).Recover(true)
		}
		return nil, fault.Wrap(fault.KindPath, pr.path, pr.pos, "read ts packet", err)
	}
	p, perr := parsePacket(pr.scratch[:], pr.index, pr.pos)
	if perr != nil {
		return nil, fault.New(fault.KindCorrupt, pr.path, pr.pos, perr.Error()).Recover(false)
	}
	pr.pos += PacketSize
	pr.index++
	return &p, nil
}

// Offset returns the raw offset just past the last parsed packet.
func (pr *PacketReader) Offset() int64 { return pr.pos }
