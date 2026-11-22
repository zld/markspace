// Package codec scans compressed bitstream boundaries without decoding them.
// Scanning never rewrites sample bytes: it only reports where valid units are
// and where truncation damaged the stream.
package codec

import (
	"encoding/binary"

	"mediadist/internal/merr"
)

// Unit is one NAL unit or OBU located inside a byte buffer.
type Unit struct {
	// Stream is the logical input stream this unit came from.
	Stream int
	// Offset is the byte offset of the unit's first byte (after any start
	// code for Annex B, or the OBU header byte for AV1).
	Offset int64
	// HeaderLen is the length of the start code (Annex B) or OBU header.
	HeaderLen int64
	// Size is the byte length of the unit payload, excluding start codes.
	Size int64
	// Type is the NAL unit type or OBU type.
	Type int
	// Truncated is true when the unit runs past the end of the buffer.
	Truncated bool
	// Keyframe marks a unit that can start an independently decodable frame.
	Keyframe bool
	// FrameStart marks the first unit of a coded picture: a VCL NAL for
	// H.264/H.265, or a FRAME/FRAME_HEADER OBU for AV1.
	FrameStart bool
	// FrameType is the AV1 frame_type (0 means KEY_FRAME); -1 when unknown.
	FrameType int
	// TemporalID and LayerID are H.265-only identifiers; zero otherwise.
	TemporalID int
	LayerID    int
}

// End returns the exclusive byte end of the unit.
func (u Unit) End() int64 { return u.Offset + u.HeaderLen + u.Size }

// Span returns the unit's byte footprint as a merr.Span.
func (u Unit) Span() merr.Span {
	return merr.OffsetSpan(u.Stream, u.Offset, u.HeaderLen+u.Size)
}

// ScanResult is the output of scanning one access unit group.
type ScanResult struct {
	Units []Unit
	// Damaged lists truncated or malformed byte intervals.
	Damaged []merr.Span
	// Errors are recoverable structured errors matching Damaged.
	Errors []*merr.Error
	// Keyframes lists unit indices flagged as keyframes, ascending.
	Keyframes []int
}

// AddDamaged records a damage interval and its matching error.
func (r *ScanResult) AddDamaged(stream int, offset, length int64, code, msg string) {
	r.Damaged = append(r.Damaged, merr.OffsetSpan(stream, offset, length))
	r.Errors = append(r.Errors, merr.New(merr.StageCodec, code, merr.OffsetSpan(stream, offset, length), true, "%s", msg))
}

// ScanLimits bounds scan work.
type ScanLimits struct {
	// MaxUnits caps the number of units returned per buffer.
	MaxUnits int
	// MaxUnitBytes caps any single unit's declared size.
	MaxUnitBytes int64
}

// DefaultScanLimits allows 8192 units of up to 16 MiB each.
func DefaultScanLimits() ScanLimits {
	return ScanLimits{MaxUnits: 8192, MaxUnitBytes: 16 << 20}
}

func (l ScanLimits) validated() ScanLimits {
	if l.MaxUnits <= 0 {
		l.MaxUnits = DefaultScanLimits().MaxUnits
	}
	if l.MaxUnitBytes <= 0 {
		l.MaxUnitBytes = DefaultScanLimits().MaxUnitBytes
	}
	return l
}

// FindStartCodes returns the byte offsets of every Annex B start code
// (00 00 01 or 00 00 00 01) in buf, in ascending order. A start code is
// reported at the first zero of the run so its length (3 or 4) is the gap to
// the payload.
func FindStartCodes(buf []byte) []int {
	var out []int
	for i := 0; i+2 < len(buf); {
		if buf[i] == 0 && buf[i+1] == 0 && buf[i+2] == 1 {
			out = append(out, i)
			i += 3
			continue
		}
		if i+3 < len(buf) && buf[i] == 0 && buf[i+1] == 0 && buf[i+2] == 0 && buf[i+3] == 1 {
			out = append(out, i)
			i += 4
			continue
		}
		i++
	}
	return out
}

// startCodeLen returns 4 for 00 00 00 01 and 3 for 00 00 01 at offset.
func startCodeLen(buf []byte, offset int) int {
	if offset+3 < len(buf) && buf[offset] == 0 && buf[offset+1] == 0 && buf[offset+2] == 0 && buf[offset+3] == 1 {
		return 4
	}
	return 3
}

// trailingZeroLength counts zeros after the last start code payload; Annex B
// allows trailing_zero_8bits which belong to nobody.
func trailingZeroLength(buf []byte, from int) int {
	n := 0
	for i := len(buf) - 1; i >= from && buf[i] == 0; i-- {
		n++
	}
	return n
}

// ScanH264 splits an Annex B byte stream into NAL units. A NAL unit that the
// declared boundaries leave truncated is reported as a damaged span, and
// scanning continues with the next start code.
func ScanH264(stream int, buf []byte, lim ScanLimits) ScanResult {
	lim = lim.validated()
	res := ScanResult{}
	starts := FindStartCodes(buf)
	if len(starts) == 0 {
		if len(buf) > 0 {
			res.AddDamaged(stream, 0, int64(len(buf)), "no_start_code",
				"buffer has no Annex B start code")
		}
		return res
	}
	// Bytes before the first start code are junk or an incomplete prefix.
	if starts[0] > 0 {
		res.AddDamaged(stream, 0, int64(starts[0]), "leading_garbage",
			"bytes before first start code discarded")
	}
	for i, s := range starts {
		if len(res.Units) >= lim.MaxUnits {
			res.AddDamaged(stream, int64(s), int64(len(buf)-s), "unit_limit",
				"unit limit reached; remainder not scanned")
			break
		}
		scLen := startCodeLen(buf, s)
		payloadStart := s + scLen
		if payloadStart >= len(buf) {
			res.AddDamaged(stream, int64(s), int64(len(buf)-s), "empty_unit",
				"start code with no following byte")
			continue
		}
		var end int
		if i+1 < len(starts) {
			end = starts[i+1]
		} else {
			end = len(buf) - trailingZeroLength(buf, payloadStart)
		}
		if end <= payloadStart {
			res.AddDamaged(stream, int64(s), int64(end-s), "empty_unit",
				"start code immediately followed by another")
			continue
		}
		size := int64(end - payloadStart)
		hdr := buf[payloadStart]
		nalType := int(hdr & 0x1F)
		unit := Unit{
			Stream:     stream,
			Offset:     int64(s),
			HeaderLen:  int64(scLen),
			Size:       size,
			Type:       nalType,
			Keyframe:   H264Keyframe(nalType, buf[payloadStart:end]),
			FrameStart: nalType >= 1 && nalType <= 5,
			FrameType:  -1,
		}
		if size > lim.MaxUnitBytes {
			unit.Truncated = true
			res.AddDamaged(stream, int64(s), size, "unit_too_large",
				"NAL unit size exceeds limit")
		}
		res.Units = append(res.Units, unit)
		if unit.Keyframe {
			res.Keyframes = append(res.Keyframes, len(res.Units)-1)
		}
	}
	return res
}

// H264Keyframe reports whether a NAL begins an IDR access unit. Only type 5 is
// an IDR slice; SPS/PPS describe the stream but do not decode to a picture, so
// they are not keyframe anchors on their own.
func H264Keyframe(nalType int, payload []byte) bool {
	return nalType == 5
}

// H265Keyframe reports whether a NAL is an IRAP picture. IRAP types are 16..23
// (BLA/IDR/CRA). VPS/SPS/PPS (32..34) are parameter sets, not pictures.
func H265Keyframe(nalType int) bool {
	return nalType >= 16 && nalType <= 23
}

// ScanH265 splits an Annex B H.265 byte stream into NAL units using the
// two-byte NAL header. Truncation is isolated per unit.
func ScanH265(stream int, buf []byte, lim ScanLimits) ScanResult {
	lim = lim.validated()
	res := ScanResult{}
	starts := FindStartCodes(buf)
	if len(starts) == 0 {
		if len(buf) > 0 {
			res.AddDamaged(stream, 0, int64(len(buf)), "no_start_code",
				"buffer has no Annex B start code")
		}
		return res
	}
	if starts[0] > 0 {
		res.AddDamaged(stream, 0, int64(starts[0]), "leading_garbage",
			"bytes before first start code discarded")
	}
	for i, s := range starts {
		if len(res.Units) >= lim.MaxUnits {
			res.AddDamaged(stream, int64(s), int64(len(buf)-s), "unit_limit",
				"unit limit reached; remainder not scanned")
			break
		}
		scLen := startCodeLen(buf, s)
		payloadStart := s + scLen
		if payloadStart+2 > len(buf) {
			res.AddDamaged(stream, int64(s), int64(len(buf)-s), "short_nal_header",
				"H.265 NAL header needs two bytes")
			continue
		}
		var end int
		if i+1 < len(starts) {
			end = starts[i+1]
		} else {
			end = len(buf) - trailingZeroLength(buf, payloadStart)
		}
		if end <= payloadStart {
			res.AddDamaged(stream, int64(s), int64(end-s), "empty_unit",
				"start code immediately followed by another")
			continue
		}
		size := int64(end - payloadStart)
		hdr := binary.BigEndian.Uint16(buf[payloadStart:])
		nalType := int((hdr >> 9) & 0x3F)
		layerID := int((hdr >> 3) & 0x3F)
		tid := int(hdr & 0x07)
		if tid > 6 {
			res.AddDamaged(stream, int64(s), size, "bad_temporal_id",
				"H.265 temporal_id_plus1 exceeds 7")
			continue
		}
		unit := Unit{
			Stream:     stream,
			Offset:     int64(s),
			HeaderLen:  int64(scLen),
			Size:       size,
			Type:       nalType,
			Keyframe:   H265Keyframe(nalType),
			FrameStart: nalType <= 31,
			FrameType:  -1,
			TemporalID: tid,
			LayerID:    layerID,
		}
		if size > lim.MaxUnitBytes {
			unit.Truncated = true
			res.AddDamaged(stream, int64(s), size, "unit_too_large",
				"H.265 NAL unit size exceeds limit")
		}
		res.Units = append(res.Units, unit)
		if unit.Keyframe {
			res.Keyframes = append(res.Keyframes, len(res.Units)-1)
		}
	}
	return res
}

// WrapLengthPrefixed converts a 4-byte big-endian length-prefixed sample
// (as stored in MP4) into an Annex B byte stream without altering NAL bytes.
// Truncated units are reported but valid earlier units are kept.
func WrapLengthPrefixed(stream int, sample []byte, lim ScanLimits) ([]byte, []merr.Span) {
	lim = lim.validated()
	var out []byte
	var damaged []merr.Span
	off := 0
	for off < len(sample) {
		if off+4 > len(sample) {
			damaged = append(damaged, merr.OffsetSpan(stream, int64(off), int64(len(sample)-off)))
			break
		}
		n := int(binary.BigEndian.Uint32(sample[off:]))
		off += 4
		if int64(n) > lim.MaxUnitBytes {
			damaged = append(damaged, merr.OffsetSpan(stream, int64(off-4), int64(len(sample)-(off-4))))
			break
		}
		if off+n > len(sample) {
			damaged = append(damaged, merr.OffsetSpan(stream, int64(off), int64(len(sample)-off)))
			break
		}
		// Annex B start code; the NAL bytes themselves are copied verbatim.
		out = append(out, 0, 0, 0, 1)
		out = append(out, sample[off:off+n]...)
		off += n
	}
	return out, damaged
}

// Canonical returns the unit bytes that define media content, independent of
// transport framing. For Annex B, start codes are excluded because remuxing
// replaces them with length prefixes; the NAL payloads themselves are what
// must survive. Units are concatenated in scan order, so the digest is
// deterministic.
func Canonical(units []Unit, data []byte) []byte {
	var out []byte
	for _, u := range units {
		start := u.Offset + u.HeaderLen
		end := start + u.Size
		if start < 0 || end > int64(len(data)) || start >= end {
			continue
		}
		out = append(out, data[start:end]...)
	}
	return out
}

// LengthPrefixedCanonical returns the canonical bytes of an MP4-style
// length-prefixed sample. It is the read-back counterpart of Canonical: the
// 4-byte prefixes are stripped and the NAL payloads are concatenated.
func LengthPrefixedCanonical(data []byte, lim ScanLimits) ([]byte, []merr.Span) {
	lim = lim.validated()
	var out []byte
	var damaged []merr.Span
	off := 0
	for off < len(data) {
		if off+4 > len(data) {
			damaged = append(damaged, merr.OffsetSpan(0, int64(off), int64(len(data)-off)))
			break
		}
		n := int(binary.BigEndian.Uint32(data[off:]))
		off += 4
		if int64(n) > lim.MaxUnitBytes || n < 0 || off+n > len(data) {
			damaged = append(damaged, merr.OffsetSpan(0, int64(off), int64(len(data)-off)))
			break
		}
		out = append(out, data[off:off+n]...)
		off += n
	}
	return out, damaged
}
