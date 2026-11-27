// Package codec parses elementary stream structure without decoding any
// picture content. It recognises H.264/H.265 Annex B NAL units and AV1
// length-prefixed OBU sequences, classifies keyframe-bearing units, and
// reports truncated or corrupt byte ranges that never swallow the next
// valid unit. The scanners are streaming: chunks from the stream package
// are pushed in order, cross-chunk units are reassembled, and emitted units
// carry absolute byte offsets.
package codec

import (
	mediaerr "mediacheck/internal/errors"
)

// Codec identifiers supported by the kernel.
type Codec string

const (
	H264 Codec = "h264"
	H265 Codec = "h265"
	AV1  Codec = "av1"
)

// Valid reports whether c is a supported codec.
func (c Codec) Valid() bool {
	switch c {
	case H264, H265, AV1:
		return true
	default:
		return false
	}
}

// Framing selects how unit boundaries are encoded in the byte stream.
type Framing string

const (
	// AnnexB: units are delimited by 00 00 01 / 00 00 00 01 start codes
	// (H.264/H.265 elementary streams).
	AnnexB Framing = "annexb"
	// LengthPrefixed: each unit is preceded by a 4-byte big-endian length
	// (AVCC-style NALs and AV1 low-overhead OBU streams).
	LengthPrefixed Framing = "length-prefixed"
)

// MaxUnitBytes bounds reassembly so a corrupt stream cannot exhaust memory.
const MaxUnitBytes = 32 << 20 // 32 MiB

// Unit is one parsed NAL unit or OBU.
type Unit struct {
	Codec     Codec
	Type      int    // codec-specific type number
	TypeName  string // human-readable type label
	Keyframe  bool   // unit starts an IDR/keyframe picture
	Start     int64  // absolute source offset of the first payload byte
	End       int64  // absolute source offset one past the payload
	HeaderEnd int64  // absolute offset one past the framing/header bytes
	Raw       []byte // payload bytes without start code or length prefix
}

// Gap is a byte range that could not be parsed into a valid unit. The
// pipeline converts gaps into structured damage ranges; a gap is always
// clamped between two valid units (or a unit and EOF), never extended over
// later valid data.
type Gap struct {
	Start int64
	End   int64
	Code  string
	Msg   string
}

// H.264 NAL unit type labels (ITU-T H.264 Table 7-1).
var h264TypeNames = []string{
	"unspecified", "non-idr-slice", "dpa", "dpb", "dpc", "idr-slice",
	"sei", "sps", "pps", "aud", "end-sequence", "end-stream", "filler",
	"sps-extension", "prefix-nal", "subset-sps",
}

// H.265 NAL unit type labels (ITU-T H.265 Table 7-1, high level).
var h265TypeNames = map[int]string{
	0: "trail-n", 1: "trail-r", 16: "bla-w-lp", 17: "bla-w-radl",
	18: "bla-n-lp", 19: "idl-w-radl", 20: "idl-n-lp", 21: "cra-nut",
	32: "vps", 33: "sps", 34: "pps", 35: "aud", 39: "prefix-sei-nut",
	40: "suffix-sei-nut",
}

// H264NALType extracts the 5-bit nal_ref_idc/type header field.
func H264NALType(header byte) int { return int(header & 0x1f) }

// H265NALType extracts the 6-bit type from the two-byte HEVC NAL header.
func H265NALType(first, _ byte) int { return int(first >> 1) }

// H264Keyframe reports whether an H.264 NAL type starts a keyframe picture.
func H264Keyframe(nalType int) bool { return nalType == 5 } // IDR slice

// H265Keyframe reports whether an HEVC NAL type is an IRAP keyframe unit.
func H265Keyframe(nalType int) bool {
	switch nalType {
	case 16, 17, 18, 19, 20, 21: // BLA/IDR/CRA
		return true
	default:
		return false
	}
}

func nalLabel(c Codec, nalType int) string {
	switch c {
	case H264:
		if nalType >= 0 && nalType < len(h264TypeNames) {
			return h264TypeNames[nalType]
		}
	case H265:
		if name, ok := h265TypeNames[nalType]; ok {
			return name
		}
	}
	return "unknown"
}

func headerLen(c Codec) int64 {
	if c == H265 {
		return 2
	}
	return 1
}

func classify(c Codec, u *Unit) {
	if len(u.Raw) == 0 {
		return
	}
	switch c {
	case H264:
		t := H264NALType(u.Raw[0])
		u.Type = t
		u.TypeName = nalLabel(H264, t)
		u.Keyframe = H264Keyframe(t)
	case H265:
		if len(u.Raw) < 2 {
			u.TypeName = "truncated-header"
			return
		}
		t := H265NALType(u.Raw[0], u.Raw[1])
		u.Type = t
		u.TypeName = nalLabel(H265, t)
		u.Keyframe = H265Keyframe(t)
	}
}

// annexBScanner locates start-code delimited NAL units across chunk pushes.
//
// It uses the canonical Annex B state machine: zeros counts consecutive zero
// bytes (carried across chunk boundaries, capped at 3) and a 01 byte after
// at least two zeros closes a start code. All bytes — including the zero
// run itself — are queued as tentative payload; when a start code is found,
// exactly its framing bytes are trimmed from pending before the unit flushes.
// Only the small zero counter crosses a chunk boundary, so no byte is ever
// revisited and later valid units cannot be swallowed.
type annexBScanner struct {
	codec Codec

	pending   []byte
	pendStart int64 // absolute offset of pending[0]; -1 when no unit open
	zeros     int   // consecutive zeros at the scan position
	gaps      []Gap
}

func newAnnexBScanner(c Codec) *annexBScanner {
	return &annexBScanner{codec: c, pendStart: -1}
}

// Push feeds one chunk located at absolute source offset absStart.
func (s *annexBScanner) Push(chunk []byte, absStart int64, out *[]Unit) error {
	for j, b := range chunk {
		absOff := absStart + int64(j)
		switch {
		case b == 0:
			if s.zeros < 3 {
				s.zeros++
			}
			if err := s.queueByte(0, absOff); err != nil {
				return err
			}
		case b == 1 && s.zeros >= 2:
			// 00 00 01 (3) or 00 00 00 01 (4) start code.
			framing := 3
			if s.zeros == 3 {
				framing = 4
			}
			// Queue the 01 too, then remove every framing byte. The
			// preceding zeros were already queued as tentative payload.
			if err := s.queueByte(1, absOff); err != nil {
				return err
			}
			if len(s.pending) >= framing {
				s.pending = s.pending[:len(s.pending)-framing]
			}
			s.flushUnit(out)
			s.zeros = 0
		default:
			s.zeros = 0
			if err := s.queueByte(b, absOff); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *annexBScanner) queueByte(b byte, absOff int64) error {
	if s.pendStart < 0 {
		s.pendStart = absOff
	}
	if len(s.pending)+1 > MaxUnitBytes {
		s.gaps = append(s.gaps, Gap{
			Start: s.pendStart, End: absOff + 1,
			Code: "parse.unit-too-large", Msg: "NAL unit exceeds size cap",
		})
		s.pending = nil
		s.pendStart = -1
		return mediaerr.NewCoded(mediaerr.StageParse, "", "parse.unit-too-large",
			"NAL unit exceeds %d bytes", MaxUnitBytes)
	}
	s.pending = append(s.pending, b)
	return nil
}

func (s *annexBScanner) flushUnit(out *[]Unit) {
	if s.pendStart < 0 || len(s.pending) == 0 {
		s.pending = nil
		s.pendStart = -1
		return
	}
	payload := make([]byte, len(s.pending))
	copy(payload, s.pending)
	u := Unit{
		Codec:     s.codec,
		Start:     s.pendStart,
		End:       s.pendStart + int64(len(payload)),
		HeaderEnd: s.pendStart + headerLen(s.codec),
		Raw:       payload,
	}
	classify(s.codec, &u)
	*out = append(*out, u)
	s.pending = nil
	s.pendStart = -1
}

// Flush ends the stream. A final unit with a complete header is valid even
// without a trailing start code. If the stream ends inside a potential start
// code (zeros pending) the open unit is a truncated tail: it is reported as
// a gap rather than emitted as a usable NAL.
func (s *annexBScanner) Flush(_ int64, out *[]Unit) []Gap {
	if s.pendStart < 0 || len(s.pending) == 0 {
		return s.gaps
	}
	if s.zeros > 0 {
		s.gaps = append(s.gaps, Gap{
			Start: s.pendStart,
			End:   s.pendStart + int64(len(s.pending)),
			Code:  "parse.truncated-tail",
			Msg:   "stream ended inside a partial start code",
		})
		s.pending = nil
		s.pendStart = -1
		s.zeros = 0
		return s.gaps
	}
	s.flushUnit(out)
	return s.gaps
}
