// Package source provides bounded, streaming readers for the two input
// streams: length-prefixed frame records and length-prefixed RTP audio
// packets. Both readers track absolute stream offsets so that downstream
// faults remain locatable, and both cap allocation through explicit limits.
package source

import (
	"encoding/binary"
	"io"

	"github.com/pion/rtp"

	"example.com/continuity/internal/fault"
	"example.com/continuity/internal/frame"
)

// Limits bound memory consumption of a single read and of the whole stream.
type Limits struct {
	// MaxFramePayload caps one frame payload in bytes.
	MaxFramePayload int
	// MaxPacketBytes caps one RTP packet (including header).
	MaxPacketBytes int
	// MaxFrames stops the stream after this many frames; 0 means unlimited.
	MaxFrames int
	// MaxTotalBytes stops once more than this many stream bytes are read;
	// 0 means unlimited.
	MaxTotalBytes int64
}

// DefaultLimits returns conservative bounded defaults.
func DefaultLimits() Limits {
	return Limits{
		MaxFramePayload: frame.MaxPayload,
		MaxPacketBytes:  64 << 10,
		MaxFrames:       1 << 20,
		MaxTotalBytes:   4 << 30,
	}
}

// FrameReader streams frame records from an io.Reader.
type FrameReader struct {
	r      io.Reader
	limits Limits
	offset int64 // bytes consumed from r
	frames int
	closed bool
}

// NewFrameReader wraps r with the given limits.
func NewFrameReader(r io.Reader, limits Limits) *FrameReader {
	if limits.MaxFramePayload <= 0 {
		limits = DefaultLimits()
	}
	return &FrameReader{r: r, limits: limits}
}

// Offset reports the number of bytes consumed so far.
func (fr *FrameReader) Offset() int64 { return fr.offset }

// Frames reports the number of successfully parsed records.
func (fr *FrameReader) Frames() int { return fr.frames }

// Next reads and parses the next record. io.EOF is returned unchanged at a
// clean stream end; every other failure is a structured *fault.Error.
func (fr *FrameReader) Next() (*frame.Record, error) {
	if fr.closed {
		return nil, io.EOF
	}
	if fr.limits.MaxFrames > 0 && fr.frames >= fr.limits.MaxFrames {
		return nil, fault.New(fault.StageSource, "frame-limit", fault.ErrLimitExceeded).
			AtOffset(fr.offset)
	}
	var lenBuf [4]byte
	prefixOff := fr.offset
	if _, err := io.ReadFull(fr.r, lenBuf[:]); err != nil {
		if err == io.EOF {
			fr.closed = true
			return nil, io.EOF // clean end between records
		}
		return nil, fault.New(fault.StageSource, "read-length", err).AtOffset(fr.offset)
	}
	fr.offset += 4
	n := int64(binary.BigEndian.Uint32(lenBuf[:]))
	if n > int64(fr.limits.MaxFramePayload)+256 {
		return nil, fault.New(fault.StageSource, "record-limit", fault.ErrLimitExceeded).
			AtOffset(prefixOff)
	}
	if fr.limits.MaxTotalBytes > 0 && fr.offset+n > fr.limits.MaxTotalBytes {
		return nil, fault.New(fault.StageSource, "byte-limit", fault.ErrLimitExceeded).
			AtOffset(prefixOff)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(fr.r, body); err != nil {
		return nil, fault.New(fault.StageSource, "read-body", fault.ErrTruncated).
			AtOffset(fr.offset)
	}
	rec, err := frame.ParseRecord(body, fr.offset, fr.limits.MaxFramePayload)
	fr.offset += n
	if err != nil {
		return nil, err
	}
	fr.frames++
	return rec, nil
}

// Packet is one parsed RTP packet with its stream location.
type Packet struct {
	*rtp.Packet
	// Offset is the stream byte offset of the packet's length prefix.
	Offset int64
}

// PacketReader streams length-prefixed RTP packets.
type PacketReader struct {
	r      io.Reader
	limit  int
	offset int64
	count  int
	closed bool
}

// NewPacketReader wraps r; maxPacketBytes<=0 selects a 64 KiB default.
func NewPacketReader(r io.Reader, maxPacketBytes int) *PacketReader {
	if maxPacketBytes <= 0 {
		maxPacketBytes = 64 << 10
	}
	return &PacketReader{r: r, limit: maxPacketBytes}
}

// Offset reports bytes consumed so far.
func (pr *PacketReader) Offset() int64 { return pr.offset }

// Count reports successfully parsed packets.
func (pr *PacketReader) Count() int { return pr.count }

// Next reads the next RTP packet. io.EOF marks a clean stream end.
func (pr *PacketReader) Next() (*Packet, error) {
	if pr.closed {
		return nil, io.EOF
	}
	var lenBuf [4]byte
	prefixOff := pr.offset
	if _, err := io.ReadFull(pr.r, lenBuf[:]); err != nil {
		if err == io.EOF {
			pr.closed = true
			return nil, io.EOF
		}
		return nil, fault.New(fault.StageSource, "read-rtp-length", err).AtOffset(pr.offset)
	}
	pr.offset += 4
	n := int(binary.BigEndian.Uint32(lenBuf[:]))
	if n > pr.limit || n == 0 {
		return nil, fault.New(fault.StageSource, "rtp-limit", fault.ErrLimitExceeded).
			AtOffset(prefixOff)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(pr.r, buf); err != nil {
		return nil, fault.New(fault.StageSource, "read-rtp-body", fault.ErrTruncated).
			AtOffset(pr.offset)
	}
	pr.offset += int64(n)
	pkt := &rtp.Packet{}
	if err := pkt.Unmarshal(buf); err != nil {
		return nil, fault.New(fault.StageSource, "rtp-parse", err).AtOffset(prefixOff)
	}
	pr.count++
	return &Packet{Packet: pkt, Offset: prefixOff}, nil
}
