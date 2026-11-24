// Package ts: PES header parsing.
//
// Only what the repair and index stages need is decoded: the stream id, the
// PTS/DTS fields and the header length. Payload data is never reinterpreted
// as media, which keeps the remuxer byte-faithful.
package ts

import (
	"errors"
	"fmt"
)

// PESHeader is the decoded PES header of a PUSI packet.
type PESHeader struct {
	StreamID    byte
	HasPTS      bool
	HasDTS      bool
	PTS         int64
	DTS         int64
	HasLength   bool
	PacketLen   int
	HeaderBytes int
}

const pesStartCodeLen = 6 // start code prefix + stream id + packet length

// ErrNotPES means the bytes do not start a PES packet.
var ErrNotPES = errors.New("ts: not a PES start")

// ParsePES decodes a PES header from packet payload bytes.
func ParsePES(b []byte) (*PESHeader, error) {
	if len(b) < pesStartCodeLen || b[0] != 0x00 || b[1] != 0x00 || b[2] != 0x01 {
		return nil, ErrNotPES
	}
	h := &PESHeader{StreamID: b[3]}
	h.PacketLen = int(b[4])<<8 | int(b[5])
	h.HasLength = h.PacketLen != 0
	switch {
	case b[3] == 0xBE: // padding
		return h, nil
	case b[3] == 0xBF: // private stream 2
		h.HeaderBytes = pesStartCodeLen
		return h, nil
	case b[3] == 0xB9: // program stream map
		h.HeaderBytes = pesStartCodeLen
		return h, nil
	}
	if len(b) < 9 {
		return nil, fmt.Errorf("ts: PES header truncated at %d bytes", len(b))
	}
	if b[6]&0xC0 != 0x80 {
		return nil, fmt.Errorf("ts: PES marker bits 0x%02x invalid", b[6]&0xC0)
	}
	flags := b[7] >> 6
	hdrLen := int(b[8])
	start := 9
	if start+hdrLen > len(b) {
		return nil, fmt.Errorf("ts: PES optional header %d exceeds payload %d", hdrLen, len(b))
	}
	h.HeaderBytes = start + hdrLen
	// PTS_DTS_flags: '10' (2) is PTS only, '11' (3) is PTS+DTS, '01' (1) is
	// forbidden by the standard.
	switch flags {
	case 2, 3:
		if hdrLen < 5 {
			return nil, fmt.Errorf("ts: PES PTS flag with %d header bytes", hdrLen)
		}
		// In a PTS+DTS pair the PTS carries the '0011' prefix; a lone PTS
		// carries '0010'.
		ptsPrefix := byte(0x2)
		if flags == 3 {
			ptsPrefix = 0x3
		}
		pts, ok := decodeTimestamp(b[start:start+5], ptsPrefix)
		if !ok {
			return nil, fmt.Errorf("ts: malformed PTS field")
		}
		h.PTS = pts
		h.HasPTS = true
		if flags == 3 {
			if hdrLen < 10 {
				return nil, fmt.Errorf("ts: PES PTS/DTS flags with %d header bytes", hdrLen)
			}
			dts, ok := decodeTimestamp(b[start+5:start+10], 0x1)
			if !ok {
				return nil, fmt.Errorf("ts: malformed DTS field")
			}
			h.DTS = dts
			h.HasDTS = true
		}
	case 1:
		return nil, fmt.Errorf("ts: forbidden PTS_DTS_flags value 1")
	}
	if !h.HasPTS {
		return h, nil
	}
	if !h.HasDTS {
		h.DTS = h.PTS
	}
	return h, nil
}

// decodeTimestamp decodes the 33-bit PTS/DTS encoding. prefix must match the
// expected nibble (0x2 for PTS-only, 0x3 for PTS in a PTS+DTS pair, 0x1 for
// DTS).
func decodeTimestamp(b []byte, prefix byte) (int64, bool) {
	if len(b) < 5 {
		return 0, false
	}
	if b[0]>>4 != prefix || b[0]&1 != 1 || b[2]&1 != 1 || b[4]&1 != 1 {
		return 0, false
	}
	v := uint64(b[0]&0x0E)<<28 |
		uint64(b[1])<<22 |
		uint64(b[2]&0xFE)<<14 |
		uint64(b[3])<<7 |
		uint64(b[4]&0xFE)>>1
	return int64(v), true
}

// EncodeTimestamp writes the 5-byte encoding used in PES headers and tests.
func EncodeTimestamp(prefix byte, ts int64) []byte {
	v := uint64(ts) & (1<<33 - 1)
	return []byte{
		prefix<<4 | byte((v>>30)&0x07)<<1 | 1,
		byte(v >> 22),
		byte((v>>15)&0x7F)<<1 | 1,
		byte(v >> 7),
		byte(v&0x7F)<<1 | 1,
	}
}
