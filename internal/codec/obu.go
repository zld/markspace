package codec

import (
	"mediadist/internal/merr"
)

// AV1 OBU type codes from the AV1 specification, section 5.3.1.
const (
	OBUSequenceHeader       = 1
	OBUTemporalDelimiter    = 2
	OBUFrameHeader          = 3
	OBUTileGroup            = 4
	OBUMetadata             = 5
	OBUFrame                = 6
	OBURedundantFrameHeader = 7
	OBUTileList             = 8
	OBUPadding              = 15
)

// AV1 frame_type values carried in the uncompressed frame header.
const (
	AV1KeyFrame    = 0
	AV1InterFrame  = 1
	AV1IntraOnly   = 2
	AV1SwitchFrame = 3
)

// OBU is one parsed AV1 open bitstream unit.
type OBU struct {
	Type      int
	Offset    int64
	Size      int64 // payload bytes after the header and length field
	HeaderLen int64
	Extend    bool
	HasSize   bool
	Truncated bool
	// FrameType is filled for FRAME/FRAME_HEADER OBUs when readable; -1 else.
	FrameType int
	// FrameStart marks the first OBU of a coded frame.
	FrameStart bool
	// TemporalID is the OBU extension temporal_id when Extend is set.
	TemporalID int
}

// End returns the exclusive byte end of the OBU.
func (o OBU) End() int64 { return o.Offset + o.HeaderLen + o.Size }

// ScanAV1 walks length-delimited OBUs inside buf and identifies frame
// boundaries. A truncated OBU is reported as a damaged span; the scanner then
// resynchronizes at the next plausible OBU header, so later valid OBUs in the
// same buffer still parse.
func ScanAV1(stream int, buf []byte, lim ScanLimits) ScanResult {
	lim = lim.validated()
	res := ScanResult{}
	off := 0
	for off < len(buf) {
		if len(res.Units) >= lim.MaxUnits {
			res.AddDamaged(stream, int64(off), int64(len(buf)-off), "unit_limit",
				"OBU limit reached; remainder not scanned")
			break
		}
		obu, next, err := parseOBU(stream, buf, off, lim)
		if err != nil {
			e := merr.AsError(err)
			res.Damaged = append(res.Damaged, e.Span)
			res.Errors = append(res.Errors, e)
			jump := next
			if jump <= off {
				jump = off + 1
			}
			r := resyncOBU(buf, jump)
			if r < 0 {
				if jump < len(buf) {
					res.AddDamaged(stream, int64(jump), int64(len(buf)-jump), "obu_desync",
						"no further OBU boundary found")
				}
				break
			}
			if r > jump {
				res.AddDamaged(stream, int64(jump), int64(r-jump), "obu_desync",
					"skipped bytes while resynchronizing OBU scan")
			}
			off = r
			continue
		}
		unit := Unit{
			Stream:     stream,
			Offset:     obu.Offset,
			HeaderLen:  obu.HeaderLen,
			Size:       obu.Size,
			Type:       obu.Type,
			FrameStart: obu.FrameStart,
			FrameType:  obu.FrameType,
			TemporalID: obu.TemporalID,
			Truncated:  obu.Truncated,
		}
		// Only a coded frame with frame_type KEY_FRAME is a keyframe anchor.
		// A sequence header is required for decoding but is not a frame.
		if obu.Type == OBUFrame && obu.FrameType == AV1KeyFrame {
			unit.Keyframe = true
		}
		res.Units = append(res.Units, unit)
		if unit.Keyframe {
			res.Keyframes = append(res.Keyframes, len(res.Units)-1)
		}
		off = next
	}
	return res
}

// parseOBU reads one OBU header at off. On a structural error it returns the
// offset where resynchronization should start.
func parseOBU(stream int, buf []byte, off int, lim ScanLimits) (OBU, int, error) {
	if off >= len(buf) {
		return OBU{}, off, merr.New(merr.StageCodec, "obu_eof",
			merr.OffsetSpan(stream, int64(off), 0), true, "offset at end of buffer")
	}
	header := buf[off]
	obuType := int((header >> 3) & 0x0F)
	extend := header&0x04 != 0
	hasSize := header&0x02 != 0
	if header&0x80 != 0 {
		return OBU{}, off + 1, merr.New(merr.StageCodec, "obu_forbidden_bit",
			merr.OffsetSpan(stream, int64(off), 1), true,
			"OBU forbidden bit set at offset %d", off)
	}
	headerLen := int64(1)
	if extend {
		headerLen++
	}
	if off+int(headerLen) > len(buf) {
		return OBU{}, off + 1, merr.New(merr.StageCodec, "obu_short_header",
			merr.OffsetSpan(stream, int64(off), int64(len(buf)-off)), true,
			"truncated OBU header at offset %d", off)
	}
	tid := 0
	if extend {
		tid = int(buf[off+1] & 0x07)
	}
	sizeStart := off + int(headerLen)
	payloadSize := -1
	if hasSize {
		n, val, err := readLEB128(stream, buf, sizeStart)
		if err != nil {
			return OBU{}, off + 1, err
		}
		headerLen += int64(n)
		if val > uint64(lim.MaxUnitBytes) {
			return OBU{}, off + int(headerLen), merr.New(merr.StageCodec, "obu_too_large",
				merr.OffsetSpan(stream, int64(off), int64(len(buf)-off)), true,
				"OBU declares %d bytes, limit %d", val, lim.MaxUnitBytes)
		}
		payloadSize = int(val)
	} else {
		payloadSize = len(buf) - sizeStart
	}
	payloadStart := off + int(headerLen)
	if payloadSize < 0 {
		payloadSize = 0
	}
	obu := OBU{
		Type:       obuType,
		Offset:     int64(off),
		Size:       int64(payloadSize),
		HeaderLen:  headerLen,
		Extend:     extend,
		HasSize:    hasSize,
		FrameType:  -1,
		TemporalID: tid,
	}
	if payloadStart > len(buf) || payloadSize > len(buf)-payloadStart {
		obu.Truncated = true
		obu.Size = int64(len(buf) - payloadStart)
		if obu.Size < 0 {
			obu.Size = 0
		}
		return obu, len(buf), merr.New(merr.StageCodec, "obu_truncated",
			merr.OffsetSpan(stream, int64(off), int64(len(buf)-off)), true,
			"OBU type %d declares %d payload bytes but %d remain",
			obuType, payloadSize, len(buf)-payloadStart)
	}
	if payloadSize > 0 {
		switch obuType {
		case OBUFrameHeader, OBUFrame:
			if ft, ok := frameTypeFromHeader(buf[payloadStart:]); ok {
				obu.FrameType = ft
			}
			obu.FrameStart = true
		}
	}
	return obu, payloadStart + payloadSize, nil
}

// frameTypeFromHeader extracts frame_type from the uncompressed header. The
// first bit is show_existing_frame; when clear, the next two bits are
// frame_type. All other AV1 frame types (INTER, INTRA_ONLY, SWITCH) are not
// independently decodable starts.
func frameTypeFromHeader(payload []byte) (int, bool) {
	if len(payload) == 0 {
		return -1, false
	}
	if payload[0]&0x80 != 0 {
		return -1, false // show_existing_frame: references a decoded frame
	}
	return int((payload[0] >> 5) & 0x03), true
}

// readLEB128 reads an unsigned LEB128 value, rejecting truncated and
// non-canonical encodings so a corrupt length cannot swallow the buffer.
func readLEB128(stream int, buf []byte, off int) (int, uint64, error) {
	var val uint64
	var shift uint
	for i := 0; i < 8; i++ {
		if off+i >= len(buf) {
			return i, 0, merr.New(merr.StageCodec, "leb128_eof",
				merr.OffsetSpan(stream, int64(off+i), 1), true, "truncated LEB128")
		}
		b := buf[off+i]
		val |= uint64(b&0x7F) << shift
		if b&0x80 == 0 {
			if i > 0 && b == 0 {
				return i + 1, 0, merr.New(merr.StageCodec, "leb128_overlong",
					merr.OffsetSpan(stream, int64(off), int64(i+1)), true,
					"non-canonical LEB128")
			}
			return i + 1, val, nil
		}
		shift += 7
	}
	return 8, 0, merr.New(merr.StageCodec, "leb128_too_long",
		merr.OffsetSpan(stream, int64(off), 8), true, "LEB128 longer than 8 bytes")
}

// resyncOBU scans forward for a byte that could begin an OBU.
func resyncOBU(buf []byte, from int) int {
	for i := from; i < len(buf); i++ {
		h := buf[i]
		if h&0x80 != 0 {
			continue
		}
		if int((h>>3)&0x0F) == 0 {
			continue // type 0 is reserved
		}
		return i
	}
	return -1
}

// OBUCanonical returns the complete OBU byte ranges (header, length and
// payload) in scan order. AV1 samples are stored verbatim in MP4, so the
// canonical content includes the OBU framing.
func OBUCanonical(units []Unit, data []byte) []byte {
	var out []byte
	for _, u := range units {
		start := u.Offset
		end := start + u.HeaderLen + u.Size
		if start < 0 || end > int64(len(data)) || start >= end {
			continue
		}
		out = append(out, data[start:end]...)
	}
	return out
}
