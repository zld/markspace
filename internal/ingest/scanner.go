package ingest

import (
	"context"

	"mediad/internal/model"
)

// AccessUnit is one codec access unit located in the raw stream.
type AccessUnit struct {
	// Offset is the absolute byte offset of the unit's first byte (the first
	// start-code byte for Annex-B, the OBU header byte for AV1).
	Offset int64
	// Length is the unit size in bytes.
	Length int64
	// IsKey reports whether the unit is a random-access point (IDR/CRA/key).
	IsKey bool
	// NALType is the codec-specific unit type (NAL unit type or AV1 OBU type).
	NALType int
}

// Annex-B start codes as defined by ITU-T H.264/H.265.
var (
	startCode4 = []byte{0, 0, 0, 1}
	startCode3 = []byte{0, 1}
)

// ScanResult is the output of a boundary scan.
type ScanResult struct {
	Codec string
	Units []AccessUnit
	// FirstKeyOffset is the offset of the first random-access point, or -1.
	FirstKeyOffset int64
}

// Scanner performs codec-aware access-unit boundary recognition on a raw
// bitstream. The bounded chunk reader feeds the hashing path; scanning
// operates on a per-source buffer assembled from those same fixed-size chunks,
// so peak memory remains a function of configuration, not of stream length
// growth across sources.
type Scanner struct {
	cfg ReaderConfig
}

// NewScanner builds a Scanner using the supplied chunk configuration.
func NewScanner(cfg ReaderConfig) *Scanner { return &Scanner{cfg: cfg.normalize()} }

// Scan recognizes every access unit in data and validates headers. It returns
// the first reportable byte offset inside a structured error when a unit is
// truncated or carries an illegal codec header, rather than partially
// succeeding.
func (s *Scanner) Scan(ctx context.Context, codec string, data []byte) (*ScanResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch codec {
	case CodecH264:
		return scanAnnexB(codec, data)
	case CodecH265:
		return scanAnnexB(codec, data)
	case CodecAV1:
		return scanAV1(ctx, data)
	default:
		return nil, model.New(model.ErrUnknownCodec, codec, -1, "cannot scan codec %q", codec)
	}
}

// h264NALInfo decodes the H.264 NAL unit header (ITU-T H.264 7.3.1): the
// forbidden_zero_bit must be clear and the type must not be reserved.
func h264NALInfo(hdr byte) (typ int, isKey bool, err error) {
	if hdr&0x80 != 0 {
		return 0, false, model.New(model.ErrIllegalHeader, CodecH264, -1,
			"H.264 forbidden_zero_bit is set in NAL header 0x%02x", hdr)
	}
	typ = int(hdr & 0x1f)
	if typ == 0 || typ >= 25 {
		return 0, false, model.New(model.ErrIllegalHeader, CodecH264, -1,
			"H.264 reserved NAL unit type %d", typ)
	}
	return typ, typ == 5, nil // type 5 = IDR picture = key frame
}

// h265NALInfo decodes the HEVC NAL unit header (ITU-T H.265 7.3.1.2).
func h265NALInfo(h0 byte) (typ int, isKey bool, err error) {
	if h0&0x80 != 0 {
		return 0, false, model.New(model.ErrIllegalHeader, CodecH265, -1,
			"H.265 forbidden_zero_bit is set in NAL header 0x%02x", h0)
	}
	typ = int((h0 >> 1) & 0x3f)
	// IDR_W_RADL (19), IDR_N_LP (20), CRA_NUT (21) are key pictures.
	return typ, typ == 19 || typ == 20 || typ == 21, nil
}

// findStartCode returns the index and length (3 or 4) of the next Annex-B
// start code at or after from, or -1 if none exists.
func findStartCode(data []byte, from int) (idx, length int) {
	for i := from; i+2 < len(data); i++ {
		if data[i] == 0 && data[i+1] == 0 {
			if i+3 < len(data) && data[i+2] == 0 && data[i+3] == 1 {
				return i, 4
			}
			if data[i+2] == 1 {
				return i, 3
			}
		}
	}
	return -1, 0
}

func scanAnnexB(codec string, data []byte) (*ScanResult, error) {
	res := &ScanResult{Codec: codec, FirstKeyOffset: -1}
	pos, scLen := findStartCode(data, 0)
	if pos < 0 {
		return nil, model.New(model.ErrIllegalHeader, codec, 0,
			"no Annex-B start code found in bitstream")
	}
	for pos >= 0 {
		unitStart := pos
		nalStart := pos + scLen
		next, nextLen := findStartCode(data, nalStart)
		unitEnd := len(data)
		if next >= 0 {
			unitEnd = next
		}
		if nalStart >= unitEnd {
			return nil, model.New(model.ErrTruncatedUnit, codec, int64(unitStart),
				"empty NAL unit at offset %d", unitStart)
		}
		var typ int
		var isKey bool
		var err error
		if codec == CodecH265 {
			if nalStart+1 >= unitEnd {
				return nil, model.New(model.ErrTruncatedUnit, codec, int64(unitStart),
					"H.265 NAL unit truncated before 2-byte header at offset %d", unitStart)
			}
			typ, isKey, err = h265NALInfo(data[nalStart])
		} else {
			typ, isKey, err = h264NALInfo(data[nalStart])
		}
		if err != nil {
			// Attach the concrete stream offset to the header violation.
			if se, ok := model.AsStructured(err); ok {
				se.ByteOffset = int64(nalStart)
				return nil, se
			}
			return nil, err
		}
		res.Units = append(res.Units, AccessUnit{
			Offset:  int64(unitStart),
			Length:  int64(unitEnd - unitStart),
			IsKey:   isKey,
			NALType: typ,
		})
		if isKey && res.FirstKeyOffset < 0 {
			res.FirstKeyOffset = int64(unitStart)
		}
		pos, scLen = next, nextLen
	}
	return res, nil
}

// AV1 OBU type codes (AV1 spec 6.2.2).
const (
	obuSequenceHeader = 1
	obuTemporalDelim  = 2
	obuFrameHeader    = 3
	obuTileGroup      = 4
	obuMetadata       = 5
	obuFrame          = 6
)

// readLeb128 decodes an unsigned LEB128 integer at data[from:], returning the
// value and the number of bytes consumed.
func readLeb128(data []byte, from int) (val int, n int, err error) {
	var result uint64
	for i := 0; ; i++ {
		if from+i >= len(data) {
			return 0, i, model.New(model.ErrTruncatedUnit, CodecAV1, int64(from),
				"truncated LEB128 OBU size")
		}
		b := data[from+i]
		result |= uint64(b&0x7f) << (7 * i)
		if b&0x80 == 0 {
			return int(result), i + 1, nil
		}
		if i == 7 {
			return 0, i + 1, model.New(model.ErrIllegalHeader, CodecAV1, int64(from),
				"LEB128 OBU size exceeds 8 bytes")
		}
	}
}

// av1FrameType extracts frame_type from an uncompressed frame header payload
// (AV1 spec 6.8.2): show_existing_frame(1 bit); when zero, frame_type occupies
// the next two bits, with KEY_FRAME == 0.
func av1FrameIsKey(payload []byte) (isKey bool, err error) {
	br := newBitReader(payload)
	showExisting, err := br.readBits(1)
	if err != nil {
		return false, err
	}
	if showExisting == 1 {
		return false, nil // inter-ref-frame case, never a key frame here
	}
	frameType, err := br.readBits(2)
	if err != nil {
		return false, err
	}
	return frameType == 0, nil // KEY_FRAME
}

func scanAV1(ctx context.Context, data []byte) (*ScanResult, error) {
	res := &ScanResult{Codec: CodecAV1, FirstKeyOffset: -1}
	pos := 0
	for pos < len(data) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		hdrOff := pos
		hdr := data[pos]
		if hdr&0x80 != 0 { // obu_forbidden_bit
			return nil, model.New(model.ErrIllegalHeader, CodecAV1, int64(hdrOff),
				"AV1 forbidden bit set in OBU header 0x%02x", hdr)
		}
		obuType := int((hdr >> 3) & 0x0f)
		hasExt := hdr&0x04 != 0
		hasSize := hdr&0x02 != 0
		if hdr&0x01 != 0 { // reserved bit must be zero
			return nil, model.New(model.ErrIllegalHeader, CodecAV1, int64(hdrOff),
				"AV1 reserved header bit is set")
		}
		payloadStart := pos + 1
		if hasExt {
			if payloadStart >= len(data) {
				return nil, model.New(model.ErrTruncatedUnit, CodecAV1, int64(hdrOff),
					"AV1 OBU truncated before extension byte")
			}
			payloadStart++
		}
		if !hasSize {
			// The low-overhead bitstream format requires obu_has_size_flag=1.
			return nil, model.New(model.ErrIllegalHeader, CodecAV1, int64(hdrOff),
				"AV1 OBU without size field is not supported (low-overhead format only)")
		}
		size, sizeLen, err := readLeb128(data, payloadStart)
		if err != nil {
			if se, ok := model.AsStructured(err); ok {
				se.ByteOffset = int64(hdrOff)
			}
			return nil, err
		}
		bodyStart := payloadStart + sizeLen
		bodyEnd := bodyStart + size
		if bodyEnd > len(data) {
			return nil, model.New(model.ErrTruncatedUnit, CodecAV1, int64(hdrOff),
				"AV1 OBU declares %d payload bytes but only %d remain", size, len(data)-bodyStart)
		}
		isKey := false
		if obuType == obuFrameHeader || obuType == obuFrame {
			isKey, err = av1FrameIsKey(data[bodyStart:bodyEnd])
			if err != nil {
				if se, ok := model.AsStructured(err); ok {
					se.ByteOffset = int64(hdrOff)
					return nil, se
				}
				return nil, err
			}
		}
		res.Units = append(res.Units, AccessUnit{
			Offset:  int64(hdrOff),
			Length:  int64(bodyEnd - hdrOff),
			IsKey:   isKey,
			NALType: obuType,
		})
		if isKey && res.FirstKeyOffset < 0 {
			res.FirstKeyOffset = int64(hdrOff)
		}
		pos = bodyEnd
	}
	if len(res.Units) == 0 {
		return nil, model.New(model.ErrTruncatedUnit, CodecAV1, 0, "empty AV1 bitstream")
	}
	return res, nil
}

// bitReader is a minimal big-endian bit reader for the handful of uncompressed
// header fields the pipeline must inspect.
type bitReader struct {
	data    []byte
	bytePos int
	bitPos  int
}

func newBitReader(data []byte) *bitReader { return &bitReader{data: data} }

func (b *bitReader) readBits(n int) (int, error) {
	v := 0
	for i := 0; i < n; i++ {
		if b.bytePos >= len(b.data) {
			return 0, model.New(model.ErrTruncatedUnit, CodecAV1, int64(b.bytePos),
				"frame header ends before required fields were read")
		}
		bit := int((b.data[b.bytePos] >> (7 - b.bitPos)) & 1)
		v = (v << 1) | bit
		b.bitPos++
		if b.bitPos == 8 {
			b.bitPos = 0
			b.bytePos++
		}
	}
	return v, nil
}
