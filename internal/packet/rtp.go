// Package packet parses the framed RTP/RTCP capture files used by
// mediacheck and validates every RTP header field called out by RFC 3550.
package packet

import (
	"encoding/binary"
	"hash/crc32"

	"github.com/pion/rtp"

	"mediaforge/internal/mediaerr"
)

// Limits enforced for every capture and packet.
const (
	// MaxPacketSize bounds a single RTP or RTCP record (64 KiB).
	MaxPacketSize = 64 << 10
	// MaxInputSize bounds a single capture file (2 GiB).
	MaxInputSize = 2 << 30
	// Magic identifies a mediacheck RTP/RTCP capture file.
	Magic = "MFRTP\x00\x01"
	// ExtensionCRC carries the IEEE CRC-32 of the RTP payload.
	ExtensionCRC = 1
)

// RecordKind distinguishes the two record payloads in a capture.
type RecordKind uint8

const (
	// KindRTP marks an RTP packet.
	KindRTP RecordKind = 1
	// KindRTCP marks an RTCP compound datagram.
	KindRTCP RecordKind = 2
	// KindRTPTruncated marks an RTP record whose sender-declared length is
	// longer than the bytes actually captured. The framing stays valid so
	// parsing can resynchronise on the following record.
	KindRTPTruncated RecordKind = 0x81
)

// Packet is a validated RTP packet. Payload aliases the capture buffer.
type Packet struct {
	Version          uint8
	Padding          bool
	ExtensionProfile uint16
	Marker           bool
	PayloadType      uint8
	Seq              uint16
	Timestamp        uint32
	SSRC             uint32
	CSRC             []uint32
	Payload          []byte
	PaddingSize      int
	// HeaderLen is the number of bytes before the payload.
	HeaderLen int
	// Offset is the absolute byte offset of the packet in the capture.
	Offset int64
}

// Record is one framed element of a capture. Exactly one of Packet or RTCP is
// set when Err is nil. Err reports a recoverable per-record failure.
type Record struct {
	Kind   RecordKind
	Offset int64
	Packet *Packet
	RTCP   []byte
	// DeclaredLen is the record length from the framing header. It lets the
	// pipeline estimate what a truncated final record would have carried.
	DeclaredLen int
	Err         error
}

// Stream is a fully read capture.
type Stream struct {
	Records []Record
	// Bytes is the number of bytes consumed from the source.
	Bytes int64
}

// ParsePacket validates an RTP packet at the given capture offset.
func ParsePacket(buf []byte, offset int64) (*Packet, error) {
	if len(buf) < 12 {
		return nil, mediaerr.At(mediaerr.CodeTruncated, offset, 0, false,
			"RTP header needs 12 bytes, got %d", len(buf))
	}
	version := buf[0] >> 6
	seq := binary.BigEndian.Uint16(buf[2:4])
	if version != 2 {
		return nil, mediaerr.At(mediaerr.CodeBadPacket, offset, seq, true,
			"unsupported RTP version %d", version)
	}
	hasPadding := buf[0]&0x20 != 0
	hasExtension := buf[0]&0x10 != 0
	cc := int(buf[0] & 0x0f)
	headerLen := 12 + 4*cc
	if len(buf) < headerLen {
		return nil, mediaerr.At(mediaerr.CodeBadPacket, offset, seq, true,
			"CSRC count %d needs %d header bytes, got %d", cc, headerLen, len(buf))
	}
	var profile uint16
	if hasExtension {
		if len(buf) < headerLen+4 {
			return nil, mediaerr.At(mediaerr.CodeBadPacket, offset, seq, true,
				"RTP extension header truncated")
		}
		profile = binary.BigEndian.Uint16(buf[headerLen:])
		words := int(binary.BigEndian.Uint16(buf[headerLen+2:]))
		// Each extension word is four bytes; compute with overflow protection.
		if words > (len(buf)-headerLen-4)/4 {
			return nil, mediaerr.At(mediaerr.CodeBadPacket, offset, seq, true,
				"RTP extension length %d words exceeds %d available bytes", words, len(buf)-headerLen-4)
		}
		headerLen += 4 + 4*words
	}
	pad := 0
	if hasPadding {
		pad = int(buf[len(buf)-1])
		if pad == 0 || pad > len(buf)-headerLen {
			return nil, mediaerr.At(mediaerr.CodeBadPacket, offset, seq, true,
				"invalid RTP padding size %d (payload %d bytes)", pad, len(buf)-headerLen)
		}
	}
	if len(buf)-headerLen-pad <= 0 {
		return nil, mediaerr.At(mediaerr.CodeTruncated, offset, seq, true,
			"RTP packet has no payload")
	}
	var raw rtp.Packet
	if err := raw.Unmarshal(buf); err != nil {
		return nil, mediaerr.At(mediaerr.CodeBadPacket, offset, seq, true,
			"RTP header rejected: %v", err)
	}
	p := &Packet{
		Version:          version,
		Padding:          hasPadding,
		ExtensionProfile: profile,
		Marker:           raw.Marker,
		PayloadType:      raw.PayloadType,
		Seq:              raw.SequenceNumber,
		Timestamp:        raw.Timestamp,
		SSRC:             raw.SSRC,
		CSRC:             append([]uint32(nil), raw.CSRC...),
		Payload:          raw.Payload,
		PaddingSize:      pad,
		HeaderLen:        headerLen,
		Offset:           offset,
	}
	if profile == 0xBEDE {
		if tag := raw.GetExtension(ExtensionCRC); len(tag) == 4 {
			want := binary.BigEndian.Uint32(tag)
			if got := crc32.ChecksumIEEE(p.Payload); got != want {
				return p, mediaerr.At(mediaerr.CodeCorrupt, offset, seq, true,
					"payload CRC mismatch: got %08x want %08x", got, want)
			}
		}
	}
	return p, nil
}

// FrameCRC builds the payload-integrity extension used by captures.
func FrameCRC(payload []byte) uint32 { return crc32.ChecksumIEEE(payload) }

// ParseRTCP validates a compound RTCP datagram and returns it unchanged.
func ParseRTCP(buf []byte, offset int64) ([]byte, error) {
	if len(buf) < 8 {
		return nil, mediaerr.At(mediaerr.CodeTruncated, offset, 0, false,
			"RTCP needs at least 8 bytes, got %d", len(buf))
	}
	if buf[0]>>6 != 2 {
		return nil, mediaerr.At(mediaerr.CodeBadPacket, offset, 0, false,
			"unsupported RTCP version %d", buf[0]>>6)
	}
	out := append([]byte(nil), buf...)
	return out, nil
}

// ClampSize caps a byte count at the single-asset limit.
func ClampSize(n int64) int64 {
	if n < 0 {
		return 0
	}
	if n > MaxInputSize {
		return MaxInputSize
	}
	return n
}
