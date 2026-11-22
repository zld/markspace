// Package ingest parses RTP packets, reorders and deduplicates them within a
// configurable window, marks gaps explicitly, and estimates bandwidth.
package ingest

import (
	"encoding/binary"
	"fmt"

	"github.com/pion/rtp"

	"mediacore/media"
)

// Wire format for the project's input packets (all integers big-endian):
//
//	offset  size  field
//	0       2     RTP header length (typically 12)
//	2       2     payload length
//	4       ...   RTP packet bytes (github.com/pion/rtp)
//	4+rtp  4      stream id
//
// Wrapping the standard RTP packet lets the ingester exercise pion/rtp header
// and sequence parsing while still associating packets with a stream.
const (
	PacketPrefix  = 8
	MaxRTPSize    = 65535
	MaxPayloadCap = 1 << 20
)

// Packet is a parsed, associated input packet.
type Packet struct {
	Offset      int64
	StreamID    uint32
	Seq         uint16
	Timestamp   uint32
	Marker      bool
	PayloadSize int
	Payload     []byte
	SSRC        uint32
}

// ParsePacket decodes one length-prefixed RTP packet starting at offset.
func ParsePacket(buf []byte, offset int64) (*Packet, error) {
	if len(buf) < PacketPrefix {
		return nil, media.New(media.CatMalformedPacket, media.StageIngest,
			"packet prefix needs %d bytes, got %d", PacketPrefix, len(buf)).WithOffset(offset)
	}
	rtpLen := int(binary.BigEndian.Uint16(buf[0:2]))
	payLen := int(binary.BigEndian.Uint16(buf[2:4]))
	if rtpLen < 12 || rtpLen > MaxRTPSize {
		return nil, media.New(media.CatMalformedPacket, media.StageIngest,
			"invalid RTP header length %d", rtpLen).WithOffset(offset)
	}
	if payLen > MaxPayloadCap {
		return nil, media.New(media.CatSizeLimit, media.StageIngest,
			"RTP payload %d exceeds limit %d", payLen, MaxPayloadCap).WithOffset(offset)
	}
	need := PacketPrefix + rtpLen + payLen + 4
	if len(buf) < need {
		return nil, media.New(media.CatTruncatedFrame, media.StageIngest,
			"packet truncated: need %d bytes, got %d", need, len(buf)).WithOffset(offset)
	}
	var pkt rtp.Packet
	if err := pkt.Unmarshal(buf[PacketPrefix : PacketPrefix+rtpLen+payLen]); err != nil {
		return nil, media.Wrap(media.CatMalformedPacket, media.StageIngest, err,
			"RTP unmarshal failed").WithOffset(offset)
	}
	stream := binary.BigEndian.Uint32(buf[PacketPrefix+rtpLen+payLen:])
	return &Packet{
		Offset:      offset,
		StreamID:    stream,
		Seq:         pkt.SequenceNumber,
		Timestamp:   pkt.Timestamp,
		Marker:      pkt.Marker,
		PayloadSize: len(pkt.Payload),
		Payload:     pkt.Payload,
		SSRC:        pkt.SSRC,
	}, nil
}

// EncodePacket builds the wire form for a packet. It recomputes lengths so the
// fixtures and round-trip tests share one definition.
func EncodePacket(p *Packet) ([]byte, error) {
	hdr := rtp.Header{
		Version:        2,
		Marker:         p.Marker,
		SequenceNumber: p.Seq,
		Timestamp:      p.Timestamp,
		SSRC:           p.SSRC,
	}
	raw, err := hdr.Marshal()
	if err != nil {
		return nil, media.Wrap(media.CatMalformedPacket, media.StageIngest, err, "RTP marshal failed")
	}
	if len(raw) > MaxRTPSize {
		return nil, media.New(media.CatSizeLimit, media.StageIngest, "RTP header %d too large", len(raw))
	}
	out := make([]byte, PacketPrefix+len(raw)+len(p.Payload)+4)
	binary.BigEndian.PutUint16(out[0:2], uint16(len(raw)))
	binary.BigEndian.PutUint16(out[2:4], uint16(len(p.Payload)))
	copy(out[PacketPrefix:], raw)
	copy(out[PacketPrefix+len(raw):], p.Payload)
	binary.BigEndian.PutUint32(out[PacketPrefix+len(raw)+len(p.Payload):], p.StreamID)
	return out, nil
}

// PacketReader decodes a stream of length-prefixed packets.
type PacketReader struct {
	buf    []byte
	offset int64
}

// NewPacketReader prepares a reader over the whole packet buffer.
func NewPacketReader(b []byte) *PacketReader { return &PacketReader{buf: b} }

// Next returns the next packet or io.EOF-equivalent nil,false.
func (r *PacketReader) Next() (*Packet, bool, error) {
	if r.offset >= int64(len(r.buf)) {
		return nil, false, nil
	}
	if len(r.buf)-int(r.offset) < PacketPrefix {
		return nil, false, media.New(media.CatTruncatedFrame, media.StageIngest,
			"trailing %d bytes cannot form a packet prefix", len(r.buf)-int(r.offset)).
			WithOffset(r.offset)
	}
	start := r.offset
	rtpLen := int(binary.BigEndian.Uint16(r.buf[start : start+2]))
	payLen := int(binary.BigEndian.Uint16(r.buf[start+2 : start+4]))
	total := PacketPrefix + rtpLen + payLen + 4
	if total <= PacketPrefix {
		return nil, false, media.New(media.CatMalformedPacket, media.StageIngest,
			"packet declares zero length").WithOffset(start)
	}
	if start+int64(total) > int64(len(r.buf)) {
		return nil, false, media.New(media.CatTruncatedFrame, media.StageIngest,
			"packet needs %d bytes at offset %d but only %d remain", total, start, len(r.buf)-int(start)).
			WithOffset(start)
	}
	p, err := ParsePacket(r.buf[start:start+int64(total)], start)
	if err != nil {
		return nil, false, err
	}
	r.offset += int64(total)
	return p, true, nil
}

// DescribePacket is a short deterministic label for logs.
func DescribePacket(p *Packet) string {
	return fmt.Sprintf("stream=%d seq=%d ts=%d bytes=%d", p.StreamID, p.Seq, p.Timestamp, p.PayloadSize)
}
