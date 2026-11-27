package codec

import (
	"encoding/binary"

	mediaerr "mediacheck/internal/errors"
)

// AV1 OBU type identifiers (AV1 spec Table 6).
const (
	OBUSequenceHeader = 1
	OBUTemporalDelim  = 2
	OBUFrameHeader    = 3
	OBUTileGroup      = 4
	OBUMetadata       = 5
	OBUFrame          = 6
	OBUPadding        = 7
)

var av1TypeNames = map[int]string{
	0: "reserved", 1: "sequence-header", 2: "temporal-delimiter",
	3: "frame-header", 4: "tile-group", 5: "metadata", 6: "frame",
	7: "padding", 8: "redundant-frame-header", 15: "reserved",
}

// AV1 OBU header flags.
const (
	obuTypeMask  = 0x78 // bits 7..4 hold obu_type
	obuExtension = 0x04 // bit 2: extension byte present
	obuHasSize   = 0x02 // bit 1: obu_size field present
)

// obuHeader is the decoded one-byte OBU header plus its extension flag.
type obuHeader struct {
	Type       int
	HasExt     bool
	HasSize    bool
	HeaderSize int // bytes consumed by header + optional extension
}

// parseOBUHeader decodes the leading OBU header byte.
func parseOBUHeader(b byte) obuHeader {
	h := obuHeader{
		Type:    int((b & obuTypeMask) >> 3),
		HasExt:  b&obuExtension != 0,
		HasSize: b&obuHasSize != 0,
	}
	h.HeaderSize = 1
	if h.HasExt {
		h.HeaderSize = 2
	}
	return h
}

// obuIsKeyframe inspects a frame/frame-header OBU payload (header byte
// included). frame_type is the top two bits of the frame header syntax;
// frame_type 0 is KEY_FRAME. show_existing_frame precedes it, hence the
// conditional offset.
func obuIsKeyframe(obuType int, payload []byte) bool {
	if obuType != OBUFrame && obuType != OBUFrameHeader {
		return false
	}
	if len(payload) < 2 {
		return false
	}
	r := newBitReader(payload[1:]) // skip the OBU header byte
	if r.bits(1) == 1 {            // show_existing_frame
		return false
	}
	frameType := r.bits(2)
	return frameType == 0 // KEY_FRAME
}

// bitReader is a minimal MSB-first reader for the handful of frame header
// fields the kernel needs; it never decodes picture content.
type bitReader struct {
	data []byte
	pos  int
}

func newBitReader(data []byte) *bitReader { return &bitReader{data: data} }

func (r *bitReader) bits(n int) uint32 {
	var v uint32
	for i := 0; i < n; i++ {
		byteIdx := r.pos / 8
		bitIdx := 7 - r.pos%8
		if byteIdx >= len(r.data) {
			return v
		}
		v = v<<1 | uint32((r.data[byteIdx]>>bitIdx)&1)
		r.pos++
	}
	return v
}

// lengthPrefixScanner parses 4-byte big-endian length-prefixed units
// (AVCC-style H.264/H.265 NALs and AV1 low-overhead OBU streams).
//
// Resync guarantee: when a declared length overruns the remaining stream or
// the unit cap, the scanner does not blindly consume everything. It walks
// forward to the next offset that both (a) decodes as a plausible length and
// (b) is followed by a valid unit header, turns the skipped bytes into a
// gap, and resumes there, so corrupt framing cannot swallow a later valid
// unit. The state carried across chunks is only the partially read prefix or
// payload, keeping memory bounded by one unit.
type lengthPrefixScanner struct {
	codec Codec

	pending     []byte
	pendStart   int64
	declare     int   // declared payload length for the open unit; -1 if none
	haveLen     bool
	resyncing   bool   // a bad length forced a forward search
	badStart    int64  // anchor of the corrupt span during resync
	prefixStart int64  // absolute offset of the current 4-byte length prefix
	gaps        []Gap
}

func newLengthPrefixScanner(c Codec) *lengthPrefixScanner {
	return &lengthPrefixScanner{codec: c, pendStart: -1, declare: -1}
}

// Push feeds one chunk located at absolute source offset absStart.
func (s *lengthPrefixScanner) Push(chunk []byte, absStart int64, out *[]Unit) error {
	i := 0
	for i < len(chunk) {
		if s.resyncing {
			i = s.searchResync(chunk, i, absStart)
			if s.resyncing {
				return nil // boundary may start in the next chunk
			}
			continue
		}
		if !s.haveLen {
			if len(s.pending) == 0 {
				// Anchor the prefix at the first byte read for it.
				s.prefixStart = absStart + int64(i)
				if s.pendStart < 0 {
					s.pendStart = s.prefixStart
				}
			}
			need := 4 - len(s.pending)
			take := need
			if left := len(chunk) - i; take > left {
				take = left
			}
			s.pending = append(s.pending, chunk[i:i+take]...)
			i += take
			if len(s.pending) == 4 {
				decl := int(binary.BigEndian.Uint32(s.pending))
				prefixStart := s.prefixStart
				s.pending = nil
				s.haveLen = true
				s.declare = decl
				s.pendStart = absStart + int64(i)
				if decl <= 0 || decl > MaxUnitBytes {
					s.beginResync(prefixStart)
				}
			}
			continue
		}
		need := s.declare - len(s.pending)
		take := need
		if left := len(chunk) - i; take > left {
			take = left
		}
		s.pending = append(s.pending, chunk[i:i+take]...)
		i += take
		if len(s.pending) == s.declare {
			s.emitUnit(out)
		}
	}
	return nil
}

// beginResync marks the corrupt prefix as the start of the damage span and
// switches the scanner into forward-search mode.
func (s *lengthPrefixScanner) beginResync(prefixStart int64) {
	s.resyncing = true
	s.badStart = prefixStart
	s.pending = nil
	s.haveLen = false
	s.pendStart = -1
	s.declare = -1
}

// searchResync scans chunk[from:] for a credible length prefix plus valid
// header. On a hit it records the gap, consumes the 4-byte prefix, and leaves
// the scanner ready to collect that unit's payload (returning the index just
// past the prefix). On a miss it returns len(chunk) and keeps resyncing so
// the search continues seamlessly across the next push.
func (s *lengthPrefixScanner) searchResync(chunk []byte, from int, absStart int64) int {
	for k := from; k+5 <= len(chunk); k++ {
		decl := int(binary.BigEndian.Uint32(chunk[k : k+4]))
		if decl <= 0 || decl > MaxUnitBytes || k+4+decl > len(chunk) {
			continue
		}
		if !s.headerLooksValid(chunk[k+4]) {
			continue
		}
		boundary := absStart + int64(k)
		s.recordGap(s.badStart, boundary, "parse.invalid-length",
			"declared length overruns; resynced to next valid unit")
		s.resyncing = false
		s.haveLen = true
		s.declare = decl
		s.pendStart = absStart + int64(k+4)
		s.pending = nil
		return k + 4
	}
	return len(chunk)
}

// headerLooksValid sanity-checks the first header byte of the next unit.
func (s *lengthPrefixScanner) headerLooksValid(first byte) bool {
	switch s.codec {
	case H264:
		return H264NALType(first) <= 15
	case H265:
		return H265NALType(first, 0) <= 40
	case AV1:
		h := parseOBUHeader(first)
		_, known := av1TypeNames[h.Type]
		return known
	default:
		return false
	}
}

func (s *lengthPrefixScanner) emitUnit(out *[]Unit) {
	if s.declare <= 0 || s.declare > MaxUnitBytes {
		s.recordGap(s.pendStart, s.pendStart+int64(len(s.pending)),
			"parse.invalid-length", "declared length out of range")
		s.reset()
		return
	}
	payload := make([]byte, len(s.pending))
	copy(payload, s.pending)
	u := Unit{
		Codec: s.codec,
		Start: s.pendStart,
		End:   s.pendStart + int64(len(payload)),
		Raw:   payload,
	}
	if s.codec == AV1 {
		classifyAV1(&u)
	} else {
		u.HeaderEnd = u.Start + headerLen(s.codec)
		classify(s.codec, &u)
	}
	*out = append(*out, u)
	s.reset()
}

func (s *lengthPrefixScanner) reset() {
	s.pending = nil
	s.pendStart = -1
	s.declare = -1
	s.haveLen = false
	s.resyncing = false
	s.badStart = 0
	s.prefixStart = 0
}

func (s *lengthPrefixScanner) recordGap(start, end int64, code, msg string) {
	if end <= start {
		return
	}
	s.gaps = append(s.gaps, Gap{Start: start, End: end, Code: code, Msg: msg})
}

// Flush ends the stream. Before declaring truncation it makes one final
// resync attempt: if the short payload itself contains a credible embedded
// unit, the corrupt prefix becomes a clamped gap and that unit is recovered
// instead of being swallowed. Already validated units stay intact.
func (s *lengthPrefixScanner) Flush(totalLen int64, out *[]Unit) []Gap {
	switch {
	case s.resyncing:
		// No credible boundary before EOF: corruption runs to the end.
		s.recordGap(s.badStart, totalLen, "parse.invalid-length",
			"no resync boundary found before end of stream")
		s.reset()
	case s.pendStart >= 0 && len(s.pending) > 0 && !s.haveLen:
		s.recordGap(s.prefixStart, s.pendStart+int64(len(s.pending)),
			"parse.truncated-tail", "length prefix truncated by end of stream")
		s.reset()
	case s.haveLen && len(s.pending) < s.declare:
		// The bad prefix sits 4 bytes before the short payload.
		if !s.finalResync(s.prefixStart, out) {
			s.recordGap(s.prefixStart, s.pendStart+int64(len(s.pending)),
				"parse.truncated-tail",
				"declared payload longer than remaining stream")
			s.reset()
		}
	}
	return s.gaps
}

// finalResync searches the accumulated short payload for an embedded credible
// unit. prefixStart is the offset of the offending length prefix. It returns
// true when a unit was found and emitted.
func (s *lengthPrefixScanner) finalResync(prefixStart int64, out *[]Unit) bool {
	badStart := prefixStart
	body := s.pending
	for k := 0; k+5 <= len(body); k++ {
		decl := int(binary.BigEndian.Uint32(body[k : k+4]))
		if decl <= 0 || decl > MaxUnitBytes || k+4+decl > len(body) {
			continue
		}
		if !s.headerLooksValid(body[k+4]) {
			continue
		}
		// Recover the embedded unit and clamp the gap to its prefix.
		boundary := badStart + 4 + int64(k)
		s.recordGap(badStart, boundary, "parse.invalid-length",
			"over-declared length; resynced to embedded unit")
		payload := make([]byte, decl)
		copy(payload, body[k+4:k+4+decl])
		u := Unit{Codec: s.codec,
			Start: boundary + 4, End: boundary + 4 + int64(decl), Raw: payload}
		if s.codec == AV1 {
			classifyAV1(&u)
		} else {
			u.HeaderEnd = u.Start + headerLen(s.codec)
			classify(s.codec, &u)
		}
		*out = append(*out, u)
		s.reset()
		return true
	}
	return false
}

func classifyAV1(u *Unit) {
	if len(u.Raw) == 0 {
		return
	}
	h := parseOBUHeader(u.Raw[0])
	u.Type = h.Type
	if name, ok := av1TypeNames[h.Type]; ok {
		u.TypeName = name
	} else {
		u.TypeName = "reserved"
	}
	u.HeaderEnd = u.Start + int64(h.HeaderSize)
	u.Keyframe = obuIsKeyframe(h.Type, u.Raw)
	// Low-overhead length-prefixed framing expects the obu_size flag set.
	if !h.HasSize && h.Type != OBUTemporalDelim && h.Type != OBUPadding {
		u.TypeName += " (missing obu_size)"
	}
}

// NewScanner constructs the streaming scanner matching the framing.
func NewScanner(c Codec, f Framing) (Scanner, error) {
	if !c.Valid() {
		return nil, mediaerr.NewCoded(mediaerr.StageParse, "", "parse.codec",
			"unsupported codec %q", c)
	}
	switch f {
	case AnnexB:
		if c == AV1 {
			return nil, mediaerr.NewCoded(mediaerr.StageParse, "", "parse.framing",
				"AV1 uses length-prefixed OBU framing, not Annex B")
		}
		return newAnnexBScanner(c), nil
	case LengthPrefixed:
		return newLengthPrefixScanner(c), nil
	default:
		return nil, mediaerr.NewCoded(mediaerr.StageParse, "", "parse.framing",
			"unknown framing %q", f)
	}
}

// Scanner is the streaming interface implemented by both framing modes.
type Scanner interface {
	// Push feeds one chunk and its absolute source offset.
	Push(chunk []byte, absStart int64, out *[]Unit) error
	// Flush finalises the stream and returns any clamped damage gaps.
	Flush(totalLen int64, out *[]Unit) []Gap
}
