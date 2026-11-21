package ts

import (
	"encoding/binary"
	"errors"
)

// PES stream identifiers.
const (
	PESVideoMin byte = 0xe0
	PESVideoMax byte = 0xef
	PESAudioMin byte = 0xc0
	PESAudioMax byte = 0xdf

	// H264NALIDR marks an instantaneous decoding refresh (key) slice.
	H264NALIDR byte = 5
	// H264NALSPS is the sequence parameter set NAL unit type.
	H264NALSPS byte = 7
)

// PESHeader holds the fields sliced from one PES packet header.
type PESHeader struct {
	StreamID  byte
	Length    uint16
	HasPTS    bool
	HasDTS    bool
	PTS       uint64
	DTS       uint64
	HeaderLen int // total header bytes including start code
	Data      []byte
	Truncated bool
}

// IsVideoStream reports whether streamID selects a video elementary stream.
func IsVideoStream(id byte) bool { return id >= PESVideoMin && id <= PESVideoMax }

// IsAudioStream reports whether streamID selects an audio elementary stream.
func IsAudioStream(id byte) bool { return id >= PESAudioMin && id <= PESAudioMax }

// ParsePES parses a reassembled PES packet payload (the bytes carried across
// one or more TS packets for a PID, starting at the 0x000001 start code).
// A truncated header is reported with Truncated set instead of an error so
// callers can log the location and keep processing the stream.
func ParsePES(b []byte) (PESHeader, error) {
	if len(b) < 9 {
		return PESHeader{Data: b, Truncated: true},
			errors.New("ts: PES packet shorter than minimum header")
	}
	if b[0] != 0x00 || b[1] != 0x00 || b[2] != 0x01 {
		return PESHeader{Data: b}, errors.New("ts: missing PES start code")
	}
	h := PESHeader{
		StreamID:  b[3],
		Length:    binary.BigEndian.Uint16(b[4:6]),
		HeaderLen: 9,
	}
	// Padding (0xbe) and private/table streams do not carry the standard
	// optional PES header layout.
	if h.StreamID == 0xbe || h.StreamID == 0xbf ||
		(h.StreamID >= 0xf0 && h.StreamID <= 0xf2) ||
		(h.StreamID >= 0xf8 && h.StreamID <= 0xfe) {
		h.Data = b[6:]
		return h, nil
	}
	// Header layout after the length field: flags1(1) flags2(1)
	// PES_header_data_length(1) then the optional header bytes.
	flags1 := b[6]
	headerDataLen := int(b[8])
	h.HeaderLen = 9 + headerDataLen
	if len(b) < h.HeaderLen {
		h.Truncated = true
		h.Data = b[9:]
		return h, errors.New("ts: truncated PES optional header")
	}
	ptsDtsFlags := (flags1 >> 6) & 0x03
	switch ptsDtsFlags {
	case 0x02:
		if headerDataLen < 5 {
			return h, errors.New("ts: PTS flag set but header too short")
		}
		h.HasPTS = true
		h.PTS = decodeTimestamp(b[9:14])
	case 0x03:
		if headerDataLen < 10 {
			return h, errors.New("ts: PTS+DTS flag set but header too short")
		}
		h.HasPTS = true
		h.HasDTS = true
		h.PTS = decodeTimestamp(b[9:14])
		h.DTS = decodeTimestamp(b[14:19])
	}
	h.Data = b[h.HeaderLen:]
	return h, nil
}

// decodeTimestamp reads the 3 marker-bit-padded 33-bit PTS/DTS encoding.
func decodeTimestamp(b []byte) uint64 {
	return uint64(b[0]&0x0e)<<29 |
		uint64(b[1])<<22 |
		uint64(b[2]&0xfe)<<14 |
		uint64(b[3])<<7 |
		uint64(b[4]>>1)
}

// IsH264Keyframe scans Annex B NAL units in an H.264 PES payload and reports
// whether an IDR slice or SPS is present, i.e. the frame can start a segment.
// Emulation prevention bytes are skipped; damaged input returns false rather
// than panicking.
func IsH264Keyframe(payload []byte) bool {
	for len(payload) > 0 {
		nal, rest, ok := nextAnnexBNAL(payload)
		if !ok {
			return false
		}
		if len(nal) > 0 {
			nalType := nal[0] & 0x1f
			if nalType == H264NALIDR || nalType == H264NALSPS {
				return true
			}
		}
		payload = rest
	}
	return false
}

// nextAnnexBNAL locates the next 0x000001 / 0x00000001 start code and
// returns one NAL unit (start code stripped) plus the remaining bytes.
func nextAnnexBNAL(b []byte) (nal, rest []byte, ok bool) {
	start := indexStartCode(b)
	if start < 0 {
		return nil, nil, false
	}
	scLen := 3
	if start+3 < len(b) && b[start+3] == 0x00 {
		scLen = 4
	}
	body := b[start+scLen:]
	next := indexStartCode(body)
	if next < 0 {
		return body, nil, true
	}
	return body[:next], body[next:], true
}

// indexStartCode returns the byte index of the first Annex B start code.
func indexStartCode(b []byte) int {
	for i := 0; i+2 < len(b); i++ {
		if b[i] == 0 && b[i+1] == 0 && b[i+2] == 1 {
			return i
		}
	}
	return -1
}

// PESReassembler accumulates TS packet payloads for one PID until the next
// packet with PUSI, yielding complete PES units.
type PESReassembler struct {
	buf      []byte
	started  bool
	maxBytes int
}

// NewPESReassembler caps buffered bytes so a pathological stream cannot
// exhaust memory; zero means the default 4 MiB limit.
func NewPESReassembler(maxBytes int) *PESReassembler {
	if maxBytes <= 0 {
		maxBytes = 4 << 20
	}
	return &PESReassembler{maxBytes: maxBytes}
}

// Push feeds one TS packet. When p.PUSI is set the previous unit (if any) is
// returned first; subsequent payload bytes begin a new unit.
func (r *PESReassembler) Push(p Packet) (completed []byte, start bool) {
	if p.PUSI {
		completed = r.buf
		r.buf = r.buf[:0]
		r.started = true
		start = true
	}
	if !r.started || !p.HasPayload {
		return completed, start
	}
	pl := p.Payload()
	if len(r.buf)+len(pl) > r.maxBytes {
		// A unit that exceeds the cap is truncated rather than growing
		// without bound; callers see the partial unit at the next boundary.
		pl = pl[:r.maxBytes-len(r.buf)]
	}
	r.buf = append(r.buf, pl...)
	return completed, start
}

// Drain returns any buffered unit at end of stream and resets the buffer.
func (r *PESReassembler) Drain() []byte {
	out := r.buf
	r.buf = nil
	r.started = false
	return out
}
