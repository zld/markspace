// Package packet parses RTP/RTCP records and resequences RTP packets
// into a unified, ordered event stream.
package packet

import (
	"encoding/binary"
	"fmt"

	"github.com/pion/rtp"
)

// Error is a structured parse/validation error carrying the byte offset
// of the offending record within the input stream.
type Error struct {
	Offset int64  // byte offset in the input stream
	Seq    int    // RTP sequence number if decodable, else -1
	Reason string // human readable cause
}

func (e *Error) Error() string {
	if e.Seq >= 0 {
		return fmt.Sprintf("packet error at byte %d (seq %d): %s", e.Offset, e.Seq, e.Reason)
	}
	return fmt.Sprintf("packet error at byte %d: %s", e.Offset, e.Reason)
}

// RTPInfo is the parsed, validated view of one RTP packet.
type RTPInfo struct {
	Seq        uint16
	Timestamp  uint32
	SSRC       uint32
	Marker     bool
	Payload    []byte // excludes header, extension and padding
	HeaderLen  int
	PaddingLen int
	RawLen     int
	// Offset is the byte offset of the packet's record in the input
	// stream. It is set by the caller, not by ParseRTP.
	Offset int64
}

// ValidateRTP checks the RTP v2 header structure: version, CSRC count,
// extension header length and padding length. data must be exactly one
// RTP packet. It returns the header length and padding length.
func ValidateRTP(data []byte) (headerLen, paddingLen int, err error) {
	if len(data) < 12 {
		return 0, 0, fmt.Errorf("header truncated: %d bytes < 12", len(data))
	}
	if v := data[0] >> 6; v != 2 {
		return 0, 0, fmt.Errorf("unsupported RTP version %d", v)
	}
	cc := int(data[0] & 0x0f)
	headerLen = 12 + 4*cc
	if len(data) < headerLen {
		return 0, 0, fmt.Errorf("csrc list truncated: need %d bytes, have %d", headerLen, len(data))
	}
	if data[0]&0x10 != 0 { // extension bit
		if len(data) < headerLen+4 {
			return 0, 0, fmt.Errorf("extension header truncated")
		}
		extWords := int(binary.BigEndian.Uint16(data[headerLen+2:]))
		extLen := 4 * (1 + extWords)
		if len(data) < headerLen+extLen {
			return 0, 0, fmt.Errorf("extension body truncated: need %d bytes, have %d", headerLen+extLen, len(data))
		}
		headerLen += extLen
	}
	if data[0]&0x20 != 0 { // padding bit
		if len(data) == headerLen {
			return 0, 0, fmt.Errorf("padding bit set with no payload")
		}
		pad := int(data[len(data)-1])
		if pad == 0 {
			return 0, 0, fmt.Errorf("invalid padding length 0")
		}
		if pad > len(data)-headerLen {
			return 0, 0, fmt.Errorf("padding %d exceeds payload %d", pad, len(data)-headerLen)
		}
		paddingLen = pad
	}
	return headerLen, paddingLen, nil
}

// ParseRTP validates and parses one RTP packet. off is the byte offset
// of data within the input stream, reported in errors.
func ParseRTP(data []byte, off int64) (*RTPInfo, error) {
	seq := -1
	if len(data) >= 4 {
		seq = int(binary.BigEndian.Uint16(data[2:]))
	}
	headerLen, paddingLen, err := ValidateRTP(data)
	if err != nil {
		return nil, &Error{Offset: off, Seq: seq, Reason: err.Error()}
	}
	var p rtp.Packet
	if uerr := p.Unmarshal(data); uerr != nil {
		return nil, &Error{Offset: off, Seq: seq, Reason: uerr.Error()}
	}
	return &RTPInfo{
		Seq:        p.SequenceNumber,
		Timestamp:  p.Timestamp,
		SSRC:       p.SSRC,
		Marker:     p.Marker,
		Payload:    p.Payload,
		HeaderLen:  headerLen,
		PaddingLen: paddingLen,
		RawLen:     len(data),
	}, nil
}
