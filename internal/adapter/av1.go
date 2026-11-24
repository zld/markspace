package adapter

import (
	"bytes"

	"archive-recovery/internal/media"
)

// AV1Version is the single supported AV1 parser generation.
const AV1Version = "av1-1"

// AV1 OBU types (AV1 spec §6.2.1).
const (
	obuSeqHeader       = 1
	obuTemporalDelim   = 2
	obuFrameHeader     = 3
	obuTileGroup       = 4
	obuMetadata        = 5
	obuFrame           = 6
	obuRedundantHeader = 7
	obuTileList        = 8
	obuPadding         = 15
)

const (
	av1MaxOBUSize = 16 << 20 // 16 MiB hard cap
	lebMaxBytes   = 8
)

// AV1Adapter parses AV1 low-overhead bitstreams (raw OBU sequence).
type AV1Adapter struct{}

func init() { Register(AV1Adapter{}) }

func (AV1Adapter) Codec() string      { return "av1" }
func (AV1Adapter) Version() string    { return AV1Version }
func (AV1Adapter) MaxUnitSize() int64 { return av1MaxOBUSize }

// NewSplitter returns a fresh streaming OBU splitter.
func (AV1Adapter) NewSplitter() Splitter { return &av1Scanner{} }

type av1Scanner struct {
	buf    bytes.Buffer
	absPos int64 // absolute offset of buf[0]
}

func (s *av1Scanner) Write(p []byte) ([]Unit, []media.UnitError, error) {
	if len(p) == 0 {
		return nil, nil, nil
	}
	s.buf.Write(p)
	var units []Unit
	var uerrs []media.UnitError

	for s.buf.Len() > 0 {
		window := s.buf.Bytes()
		headerLen, obuType, hasSize, hdrErr := parseOBUHeader(window)
		if hdrErr != nil {
			// Malformed header: record at the OBU offset and resync by
			// discarding the single bad byte so parsing may continue.
			uerrs = append(uerrs, media.UnitError{
				Offset: s.absPos, Class: hdrErr.Class, Reason: hdrErr.Message,
			})
			s.buf.Next(1)
			s.absPos++
			continue
		}
		if !hasSize {
			return units, uerrs, media.NewOffsetError(media.ClassRecoverable, "av1",
				"OBU without size field is illegal in low-overhead bitstream", s.absPos)
		}
		size, lebLen, ok := decodeLEB128(window[headerLen:])
		if !ok {
			break // incomplete length field, wait for more bytes
		}
		total := int64(headerLen+lebLen) + size
		if total > av1MaxOBUSize {
			return units, uerrs, media.NewOffsetError(media.ClassRecoverable, "av1",
				"OBU exceeds maximum size", s.absPos)
		}
		if int64(s.buf.Len()) < total {
			break // payload (or part of it) still in transit
		}
		raw := window[:total]
		isFrame, isKey := classifyAV1(obuType, raw[headerLen+lebLen:])
		units = append(units, Unit{
			Offset: s.absPos, Length: total, TypeCode: obuType,
			IsFrame: isFrame, IsKeyframe: isKey,
		})
		s.buf.Next(int(total))
		s.absPos += total
	}
	return units, uerrs, nil
}

func (s *av1Scanner) Close() ([]Unit, *media.UnitError) {
	if s.buf.Len() == 0 {
		return nil, nil
	}
	off := s.absPos
	n := s.buf.Len()
	s.buf.Reset()
	return nil, &media.UnitError{
		Offset: off, Length: int64(n),
		Class: media.ClassRecoverable, Reason: "truncated OBU at end of stream",
	}
}

// parseOBUHeader validates the OBU header and returns its total length
// (including the optional extension byte), the type and size-field flag.
func parseOBUHeader(p []byte) (headerLen int, obuType int, hasSize bool, me *media.Error) {
	if len(p) < 1 {
		return 0, 0, false, media.NewError(media.ClassRecoverable, "av1", "incomplete OBU header")
	}
	h := p[0]
	if h&0x80 != 0 {
		return 0, 0, false, media.NewError(media.ClassRecoverable, "av1", "OBU forbidden bit set")
	}
	obuType = int((h >> 3) & 0x0f)
	extFlag := h&0x04 != 0
	hasSize = h&0x02 != 0
	headerLen = 1
	if extFlag {
		if len(p) < 2 {
			return 0, 0, false, media.NewError(media.ClassRecoverable, "av1", "incomplete OBU extension byte")
		}
		headerLen = 2
	}
	return headerLen, obuType, hasSize, nil
}

// decodeLEB128 parses an unsigned LEB128 integer, rejecting overlong
// encodings, continuation past 8 bytes and 64-bit overflow.
func decodeLEB128(p []byte) (value int64, n int, ok bool) {
	var v uint64
	for i := 0; i < lebMaxBytes; i++ {
		if i >= len(p) {
			return 0, 0, false
		}
		b := p[i]
		if i == lebMaxBytes-1 && b&0x7f > 0x0f {
			return 0, 0, false // exceeds uint64
		}
		v |= uint64(b&0x7f) << (7 * i)
		if b&0x80 == 0 {
			if b == 0 && i > 0 {
				return 0, 0, false // non-canonical overlong encoding
			}
			return int64(v), i + 1, true
		}
	}
	return 0, 0, false // continuation on the 9th byte
}

// classifyAV1 identifies frame-bearing OBUs and reads frame_type for the
// keyframe decision. The frame header's uncompressed part begins with
// show_existing_frame(1 bit); when zero, frame_type occupies the next two
// bits. KEY_FRAME (0) is the only random-access point.
func classifyAV1(obuType int, payload []byte) (isFrame, isKey bool) {
	switch obuType {
	case obuFrame, obuFrameHeader, obuRedundantHeader:
	default:
		return false, false
	}
	if len(payload) == 0 {
		return obuType == obuFrame, false
	}
	top := payload[0] >> 5
	showExisting := top&0x04 != 0
	if showExisting {
		return true, false
	}
	// top bit layout: [show_existing_frame | frame_type(2 bits)].
	frameType := int(top & 0x03)
	return true, frameType == 0
}
