package medpipe

import "errors"

// Codec identifies one of the three supported elementary stream codecs.
type Codec string

const (
	CodecH264 Codec = "h264"
	CodecH265 Codec = "h265"
	CodecAV1  Codec = "av1"
)

// Kind distinguishes video from audio elementary streams.
type Kind string

const (
	KindVideo Kind = "video"
	KindAudio Kind = "audio"
)

// H.264 NAL unit types (ISO/IEC 14496-10 table 7-1).
const (
	h264NALNonIDRSlice = 1
	h264NALIDRSlice    = 5
	h264NALSEI         = 6
	h264NALPrefixNALU  = 14
	h264NALDRPartedCNA = 20
)

// H.265 NAL unit types (ISO/IEC 23008-2 table 7-1).
const (
	h265NALTRAIL_N    = 0
	h265NALIDR_W_RADL = 19
	h265NALIDR_N_LP   = 20
	h265NALCRA        = 21
	h265NALPREFIX_SEI = 39
	h265NALSUFFIX_SEI = 40
)

// AV1 OBU types (AV1 spec table 6-1).
const (
	av1OBUSequenceHeader    = 1
	av1OBUTemporalDelimiter = 2
	av1OBUFrameHeader       = 3
	av1OBUMetadata          = 5
	av1OBUFrame             = 6
)

// medpipeMetaMagic prefixes an in-band metadata NAL/OBU payload so the
// scanner can distinguish pipeline metadata units from coded samples.
var medpipeMetaMagic = []byte{'M', 'E', 'T', 'A', 0x01}

// medpipeMetaTags enumerate the TLV fields carried inside a metadata unit.
const (
	metaTagColorPrimaries = 1
	metaTagTransfer       = 2
	metaTagMatrix         = 3
	metaTagQuality        = 4 // deterministic per-frame quality marker
)

// Frame is one decoded access unit produced by the scanner. Only complete
// units are ever emitted; truncated tails are reported as errors instead.
type Frame struct {
	Codec    Codec `json:"codec"`
	Keyframe bool  `json:"keyframe"`
	// Ordinal is the zero-based position of the frame within its track;
	// assigned by the index/frame reader and used for resume bookkeeping.
	Ordinal int `json:"ordinal"`
	// ByteOffset is the absolute offset of the unit start inside the
	// transport (.rtp) file.
	ByteOffset int64 `json:"byte_offset"`
	// RawPayload is the coded NAL/OBU payload excluding transport headers.
	RawPayload []byte `json:"-"`
	// Meta carries parsed in-band metadata (color, quality), if present.
	Meta    FrameMeta `json:"meta"`
	HasMeta bool      `json:"has_meta"`
}

// FrameMeta is the parsed content of an in-band metadata unit.
type FrameMeta struct {
	ColorPrimaries uint8 `json:"color_primaries,omitempty"`
	Transfer       uint8 `json:"transfer_characteristics,omitempty"`
	Matrix         uint8 `json:"matrix_coefficients,omitempty"`
	Quality        uint8 `json:"quality,omitempty"`
}

// ErrNotMetadata indicates a payload is not a medpipe metadata unit.
var ErrNotMetadata = errors.New("not a medpipe metadata unit")

// parseMetaPayload decodes the magic-prefixed TLV metadata payload. The
// layout is: magic(5) tag(1) len(1) value(len) ...
func parseMetaPayload(payload []byte) (FrameMeta, error) {
	if len(payload) < len(medpipeMetaMagic) {
		return FrameMeta{}, ErrNotMetadata
	}
	for i, m := range medpipeMetaMagic {
		if payload[i] != m {
			return FrameMeta{}, ErrNotMetadata
		}
	}
	var fm FrameMeta
	p := payload[len(medpipeMetaMagic):]
	for len(p) >= 2 {
		tag, ln := p[0], p[1]
		p = p[2:]
		if int(ln) > len(p) {
			return FrameMeta{}, errors.New("metadata TLV overruns unit payload")
		}
		if ln < 1 {
			return FrameMeta{}, errors.New("metadata TLV zero length")
		}
		v := p[0]
		p = p[ln:]
		switch tag {
		case metaTagColorPrimaries:
			fm.ColorPrimaries = v
		case metaTagTransfer:
			fm.Transfer = v
		case metaTagMatrix:
			fm.Matrix = v
		case metaTagQuality:
			fm.Quality = v
		default:
			// Unknown tags are skipped but their bytes are still validated.
		}
	}
	if len(p) != 0 {
		return FrameMeta{}, errors.New("dangling metadata TLV bytes")
	}
	return fm, nil
}

// buildMetaPayload is the inverse of parseMetaPayload, used by the fixture
// generator and kept here so the wire format lives in one place.
func buildMetaPayload(fm FrameMeta) []byte {
	out := append([]byte(nil), medpipeMetaMagic...)
	put := func(tag, v uint8) {
		out = append(out, tag, 1, v)
	}
	if fm.ColorPrimaries != 0 {
		put(metaTagColorPrimaries, fm.ColorPrimaries)
	}
	if fm.Transfer != 0 {
		put(metaTagTransfer, fm.Transfer)
	}
	if fm.Matrix != 0 {
		put(metaTagMatrix, fm.Matrix)
	}
	if fm.Quality != 0 {
		put(metaTagQuality, fm.Quality)
	}
	return out
}

// classifyH264 returns (keyframe, isMetadata) for an H.264 NAL unit.
func classifyH264(nal []byte) (keyframe, isMeta bool) {
	if len(nal) == 0 {
		return false, false
	}
	typ := nal[0] & 0x1F
	if typ == h264NALIDRSlice {
		return true, false
	}
	if typ == h264NALSEI {
		if _, err := parseMetaPayload(nal[1:]); err == nil {
			return false, true
		}
	}
	return false, false
}

// classifyH265 returns (keyframe, isMetadata) for an H.265 NAL unit.
func classifyH265(nal []byte) (keyframe, isMeta bool) {
	if len(nal) < 2 {
		return false, false
	}
	typ := (nal[0] >> 1) & 0x3F
	switch typ {
	case h265NALIDR_W_RADL, h265NALIDR_N_LP, h265NALCRA:
		return true, false
	case h265NALPREFIX_SEI, h265NALSUFFIX_SEI:
		if _, err := parseMetaPayload(nal[2:]); err == nil {
			return false, true
		}
	}
	return false, false
}

// av1OBUHeader splits an OBU header into type and extension flag.
func av1OBUHeader(obu []byte) (typ uint8, hasExt bool, ok bool) {
	// obu_forbidden_bit is the high bit and must be zero (AV1 spec 6.2).
	if len(obu) < 1 || obu[0]&0x80 != 0 {
		return 0, false, false
	}
	typ = (obu[0] >> 3) & 0x0F
	hasExt = obu[0]&0x04 != 0
	return typ, hasExt, true
}

// classifyAV1 returns (keyframe, isMetadata) for one AV1 OBU. AV1 keyframes
// are signalled by show_existing_frame=0 and frame_type=0 in the frame header;
// the synthetic fixtures encode those two bits as the first payload byte of
// OBU_FRAME/OBU_FRAME_HEADER, which matches the real bitstream placement.
func classifyAV1(obu []byte) (keyframe, isMeta bool) {
	typ, hasExt, ok := av1OBUHeader(obu)
	if !ok {
		return false, false
	}
	off := 1
	if hasExt {
		off++
	}
	if typ == av1OBUMetadata {
		if len(obu) > off {
			if _, err := parseMetaPayload(obu[off:]); err == nil {
				return false, true
			}
		}
	}
	if typ != av1OBUFrame && typ != av1OBUFrameHeader || len(obu) <= off {
		return false, false
	}
	// First payload byte: bit0 = show_existing_frame (0), bit1..2 = frame_type (0 = KEY).
	hdr := obu[off]
	showExisting := hdr & 0x01
	frameType := (hdr >> 1) & 0x03
	return showExisting == 0 && frameType == 0, false
}

// classifyFrame dispatches codec-specific keyframe/metadata detection.
func classifyFrame(codec Codec, payload []byte) (keyframe, isMeta bool) {
	switch codec {
	case CodecH264:
		return classifyH264(payload)
	case CodecH265:
		return classifyH265(payload)
	case CodecAV1:
		return classifyAV1(payload)
	default:
		return false, false
	}
}

// metadataNALHeader builds the codec-specific NAL/OBU header for a metadata
// unit so the generator can interleave it like a real in-band unit.
func metadataNALHeader(codec Codec) byte {
	switch codec {
	case CodecH264:
		return h264NALSEI // nal_ref_idc=0, type=6
	case CodecH265:
		return h265NALPREFIX_SEI << 1 // type 39 in the high 6 bits
	default: // AV1 OBU metadata, no extension, no obu_size field
		// OBU header layout: forbidden(1)=0 | type(4)=5 | ext(1)=0 | has_size(1)=0
		return 5 << 3 // 0x28
	}
}
