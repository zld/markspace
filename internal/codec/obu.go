package codec

import (
	"errors"

	"mediaroute/internal/clock"
)

// AV1 OBU types from the spec (only the ones this pipeline distinguishes).
const (
	OBUSequenceHeader = 1
	OBUTemporalDelim  = 2
	OBUFrameHeader    = 3
	OBUTileGroup      = 4
	OBUFrame          = 6
	OBUPadding        = 15
)

// OBU is one parsed AV1 open bitstream unit.
type OBU struct {
	Type      byte
	HasSize   bool
	Header    byte
	Payload   []byte // size-field payload (without header and leb128 size)
	Complete  bool   // false when declared size runs past the buffer
}

// readLEB128 decodes an unsigned LEB128 value and returns it with the number
// of bytes consumed. A value that overflows uint32 or runs out of buffer is an
// error, which prevents any later out-of-bounds indexing.
func readLEB128(buf []byte, pos int) (value uint64, n int, err error) {
	const maxBytes = 8
	for shift := uint(0); shift < 64 && n < maxBytes; shift += 7 {
		if pos+n >= len(buf) {
			return 0, n, errors.New("truncated LEB128 size")
		}
		b := buf[pos+n]
		n++
		value |= uint64(b&0x7F) << shift
		if b&0x80 == 0 {
			if value > uint64(^uint32(0)) {
				return 0, n, errors.New("LEB128 size exceeds uint32")
			}
			return value, n, nil
		}
	}
	return 0, n, errors.New("malformed LEB128 size")
}

// SplitOBU parses a length-delimited AV1 low-overhead bitstream. Each OBU is
// [header][leb128 size][payload]. Any OBU whose declared payload extends past
// the buffer is returned with Complete=false; the scan stops there because
// following offsets cannot be trusted.
func SplitOBU(buf []byte) ([]OBU, error) {
	var out []OBU
	pos := 0
	for pos < len(buf) {
		if obuForbidden := buf[pos] >> 7; obuForbidden != 0 {
			return out, clock.E(clock.PhaseCodec, 0, -1, false,
				"AV1 forbidden OBU header bit set", nil)
		}
		header := buf[pos]
		obuType := (header >> 3) & 0x0F
		hasSize := header&0x02 != 0
		pos++
		if !hasSize {
			// The synthetic fixtures always use obu_has_size_field=1; a
			// size-less OBU in this pipeline means an unsupported layout.
			return out, clock.E(clock.PhaseCodec, 0, -1, false,
				"AV1 OBU without size field is unsupported in this pipeline", nil)
		}
		size, n, err := readLEB128(buf, pos)
		if err != nil {
			return out, clock.E(clock.PhaseCodec, 0, -1, false, "bad AV1 OBU size", err)
		}
		pos += n
		end := pos + int(size)
		if end > len(buf) {
			// Declared payload crosses the buffer boundary: truncated OBU.
			out = append(out, OBU{Type: obuType, HasSize: true, Header: header,
				Payload: buf[pos:], Complete: false})
			return out, nil
		}
		out = append(out, OBU{
			Type:     obuType,
			HasSize:  true,
			Header:   header,
			Payload:  buf[pos:end],
			Complete: true,
		})
		pos = end
	}
	return out, nil
}

// AV1Keyframe reports whether a complete AV1 temporal unit starts a keyframe.
// The synthetic frame headers encode the keyframe flag as the first payload
// bit of OBU_FRAME_HEADER / OBU_FRAME (1 = key frame). A sequence header OBU
// is also treated as a keyframe carrier. Truncated OBUs never match.
func AV1Keyframe(frame []byte) (bool, error) {
	obus, err := SplitOBU(frame)
	if err != nil {
		return false, err
	}
	if len(obus) == 0 {
		return false, nil
	}
	key := false
	for _, obu := range obus {
		if !obu.Complete {
			return false, nil
		}
		switch obu.Type {
		case OBUSequenceHeader:
			key = true
		case OBUFrameHeader, OBUFrame:
			if len(obu.Payload) > 0 && obu.Payload[0]&0x80 != 0 {
				key = true
			}
		}
	}
	return key, nil
}

// FrameKeyAV1 is the AV1 entry point matching the NAL-style FrameKey helper.
func FrameKeyAV1(frame []byte) (bool, error) {
	return AV1Keyframe(frame)
}
