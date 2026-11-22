// Package bitstream parses raw H.264, H.265 and AV1 elementary
// streams into units and locates random access points. Synthetic
// sample streams carry a small meta header per unit (magic, sequence,
// PTS on the unified timeline, declared length and CRC32) so that
// truncation and corruption are provable without a decoder.
package bitstream

import (
	"encoding/binary"
	"hash/crc32"

	"github.com/pion/rtp/codecs"

	"mediq/internal/timeline"
)

// Codec identifies the elementary stream coding.
type Codec int

const (
	CodecUnknown Codec = iota
	CodecH264
	CodecH265
	CodecAV1
)

func (c Codec) String() string {
	switch c {
	case CodecH264:
		return "h264"
	case CodecH265:
		return "h265"
	case CodecAV1:
		return "av1"
	}
	return "unknown"
}

// ParseCodec maps a name to a Codec.
func ParseCodec(s string) Codec {
	switch s {
	case "h264":
		return CodecH264
	case "h265":
		return CodecH265
	case "av1":
		return CodecAV1
	}
	return CodecUnknown
}

// Unit is one parsed element (NAL unit or OBU) of a stream.
type Unit struct {
	Codec    Codec
	Type     uint8 // NAL unit type or OBU type
	Seq      uint32
	Offset   int64 // absolute byte offset of the unit (incl. start code / OBU header)
	Length   int64 // total bytes occupied on disk
	PTS      timeline.Ticks
	Keyframe bool
	Corrupt  bool
}

// MetaLen is the per-unit meta header size in bytes.
const MetaLen = 24

var magic = [4]byte{'M', 'D', 'I', 'Q'}

// EncodeMeta builds the 24-byte meta header for a unit payload.
// declaredLen covers data (and, for AV1, the synthetic frame header).
func EncodeMeta(seq uint32, pts timeline.Ticks, declaredLen uint32, crc uint32) [MetaLen]byte {
	var m [MetaLen]byte
	copy(m[0:4], magic[:])
	binary.BigEndian.PutUint32(m[4:8], seq)
	binary.BigEndian.PutUint64(m[8:16], uint64(pts))
	binary.BigEndian.PutUint32(m[16:20], declaredLen)
	binary.BigEndian.PutUint32(m[20:24], crc)
	return m
}

// CRC32 computes the checksum stored in the meta header.
func CRC32(data []byte) uint32 { return crc32.ChecksumIEEE(data) }

// meta is a decoded unit meta header.
type meta struct {
	seq         uint32
	pts         timeline.Ticks
	declaredLen uint32
	crc         uint32
}

// decodeMeta parses a meta header; ok=false when the magic mismatches.
func decodeMeta(b []byte) (m meta, ok bool) {
	if len(b) < MetaLen || string(b[0:4]) != string(magic[:]) {
		return m, false
	}
	m.seq = binary.BigEndian.Uint32(b[4:8])
	m.pts = timeline.Ticks(int64(binary.BigEndian.Uint64(b[8:16])))
	m.declaredLen = binary.BigEndian.Uint32(b[16:20])
	m.crc = binary.BigEndian.Uint32(b[20:24])
	return m, true
}

// h264Type extracts the NAL unit type from the first header byte.
func h264Type(b byte) uint8 { return b & 0x1f }

// h265Type extracts the NAL unit type from the first of two header bytes.
func h265Type(b byte) uint8 { return (b >> 1) & 0x3f }

// isRAP reports whether a NAL/OBU type is a random access point.
func isRAP(codec Codec, typ uint8) bool {
	switch codec {
	case CodecH264:
		return typ == 5 // IDR
	case CodecH265:
		return typ == 19 || typ == 20 // IDR_W_RADL / IDR_N_LP
	}
	return false
}

// rtpPacketCount fragments one unit payload into RTP packets using the
// pion/rtp payloaders, both as a structural check and to account for
// transport resource usage against the scan quota.
func rtpPacketCount(codec Codec, payload []byte, mtu uint16) int {
	if len(payload) == 0 {
		return 0
	}
	switch codec {
	case CodecH264:
		return len((&codecs.H264Payloader{}).Payload(mtu, payload))
	case CodecH265:
		return len((&codecs.H265Payloader{}).Payload(mtu, payload))
	case CodecAV1:
		return len((&codecs.AV1Payloader{}).Payload(mtu, payload))
	}
	return 0
}

// escapeEPB applies H.264/H.265 emulation prevention: a 0x03 byte is
// inserted after 00 00 and after 00 00 01/02/03 patterns so that no
// Annex B start code can occur inside a NAL unit. It is the inverse of
// unescapeEPB and makes synthetic streams codec-faithful.
func EscapeEPB(b []byte) []byte {
	out := make([]byte, 0, len(b)+len(b)/32)
	zeros := 0
	for _, c := range b {
		if zeros >= 2 && c <= 0x03 {
			out = append(out, 0x03)
			zeros = 0
		}
		out = append(out, c)
		if c == 0 {
			zeros++
		} else {
			zeros = 0
		}
	}
	return out
}

// unescapeEPB removes emulation prevention bytes. A standalone 0x03
// not part of an escape sequence is left untouched (malformed input
// surfaces later as a CRC or length failure).
func UnescapeEPB(b []byte) []byte {
	out := make([]byte, 0, len(b))
	zeros := 0
	for i := 0; i < len(b); i++ {
		c := b[i]
		if zeros >= 2 && c == 0x03 && i+1 < len(b) && b[i+1] <= 0x03 {
			zeros = 0 // drop the emulation prevention byte
			continue
		}
		out = append(out, c)
		if c == 0 {
			zeros++
		} else {
			zeros = 0
		}
	}
	return out
}
