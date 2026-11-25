// Package ts implements streaming MPEG-TS parsing: fixed-size packet reads
// with byte-offset tracking, per-PID continuity monitoring and PAT/PMT
// version management. It never fabricates media: every defect is reported
// with the absolute input offset at which it was observed.
package ts

import (
	"context"
	"io"

	"mediad/internal/model"
)

// Reader pulls 188-byte packets from an input stream. It is single-goroutine:
// concurrency between producers and consumers is handled by flow.Stream.
type Reader struct {
	r      io.Reader
	offset int64
}

// NewReader wraps r. The current offset starts at zero and advances by the
// number of bytes actually consumed, so every reported offset is absolute.
func NewReader(r io.Reader) *Reader { return &Reader{r: r} }

// Offset returns the number of bytes consumed from the input so far.
func (rd *Reader) Offset() int64 { return rd.offset }

// ReadPacket reads and parses one packet. It returns io.EOF at a clean
// boundary. A short tail yields model.ErrTruncated wrapping a CorruptError
// whose Offset points at the first unconsumed byte; a bad sync byte yields
// model.ErrSyncByte the same way.
func (rd *Reader) ReadPacket(ctx context.Context) (*model.Packet, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var raw [model.PacketSize]byte
	n, err := io.ReadFull(rd.r, raw[:])
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		if n == 0 {
			return nil, io.EOF // clean boundary
		}
		off := rd.offset
		rd.offset += int64(n)
		return nil, &model.CorruptError{
			Kind: "truncated", Offset: off,
			Detail: "input ended with a partial packet",
		}
	}
	if err != nil {
		return nil, err
	}
	off := rd.offset
	rd.offset += model.PacketSize

	if raw[0] != model.SyncByte {
		return nil, &model.CorruptError{
			Kind: "sync", Offset: off,
			Detail: "first byte is not 0x47",
		}
	}
	pkt := ParsePacket(raw, off)
	return pkt, nil
}

// ParsePacket decodes the fixed header, adaptation field and PCR of one
// packet whose sync byte has already been validated. It is exported so
// pipeline stages that re-encounter a raw packet can decode it uniformly.
func ParsePacket(raw [model.PacketSize]byte, off int64) *model.Packet {
	return parsePacket(raw, off)
}

// parsePacket decodes the fixed header, adaptation field and PCR of one
// packet whose sync byte has already been validated.
func parsePacket(raw [model.PacketSize]byte, off int64) *model.Packet {
	p := &model.Packet{Offset: off, PCR: -1, Raw: raw}
	p.PID = (uint16(raw[1]&0x1F) << 8) | uint16(raw[2])
	p.PayloadStart = raw[1]&0x40 != 0
	afc := (raw[3] >> 4) & 0x03
	p.Continuity = raw[3] & 0x0F
	p.HasAdaptation = afc == model.AFCAdaptationOnly || afc == model.AFCAdaptAndPayload
	p.HasPayload = afc == model.AFCPayloadOnly || afc == model.AFCAdaptAndPayload

	pos := 4
	if p.HasAdaptation {
		afLen := int(raw[pos])
		if afLen > 0 && pos+1+afLen <= model.PacketSize {
			flags := raw[pos+1]
			if flags&0x10 != 0 && afLen >= 7 {
				p.HasPCR = true
				p.PCR = decodePCR(raw[pos+2 : pos+8])
			}
		}
		// Skip the whole adaptation field to reach the payload.
		pos += 1 + afLen
	}
	if p.HasPayload && pos < model.PacketSize {
		p.Payload = raw[pos:model.PacketSize]
	}
	return p
}

// decodePCR reads a 6-byte PCR field (33-bit base + 9-bit extension) and
// returns the value in 27 MHz units: base*300 + extension.
func decodePCR(b []byte) int64 {
	base := (int64(b[0]) << 25) | (int64(b[1]) << 17) |
		(int64(b[2]) << 9) | (int64(b[3]) << 1) | int64(b[4]>>7)
	ext := (int64(b[4]&0x01) << 8) | int64(b[5])
	return base*300 + ext
}
