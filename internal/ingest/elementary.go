// Package ingest scans elementary video streams and parses key-frame index
// containers.  Scanning never aborts on the first bad unit: every illegal or
// truncated unit is reported with its byte offset, then parsing resumes at the
// next recoverable boundary.
package ingest

import (
	"encoding/binary"
	"errors"

	"mediapipeline/internal/clock"
	"mediapipeline/internal/fault"
)

// Codec identifies the elementary stream coding.
type Codec string

const (
	CodecH264 Codec = "h264"
	CodecH265 Codec = "h265"
	CodecAV1  Codec = "av1"
)

// Limits bounds untrusted input.  Out-of-range input is rejected with a
// LIMIT_EXCEEDED fault carrying the offending byte offset.
type Limits struct {
	MaxUnitBytes    int
	MaxStreamBytes  int
	MaxIndexEntries int
}

// DefaultLimits are deliberately generous but bounded.
var DefaultLimits = Limits{
	MaxUnitBytes:    4 << 20,
	MaxStreamBytes:  256 << 20,
	MaxIndexEntries: 100_000,
}

// Unit is one parsed NAL unit (H.264/H.265) or OBU (AV1).
type Unit struct {
	Codec      Codec
	Type       byte
	IsKey      bool
	Offset     int64 // byte offset of the unit framing byte
	PayloadOff int64 // byte offset of the first payload byte
	Payload    []byte
	TimeFrom   clock.Rational
	TimeTo     clock.Rational
}

// avccPrefix is the 4-byte big-endian length prefix used for H.264/H.265
// elementary streams (AVCC/HVCC style).
const avccPrefix = 4

// Scan partitions an elementary stream into units.
//
// H.264 and H.265 use 4-byte length-prefixed NAL units; AV1 uses the low
// overhead bitstream format where every OBU carries a leb128 size field.
// Faults are appended to report and scanning continues whenever the framing
// allows a safe restart.
func Scan(codec Codec, data []byte, lim Limits, report *fault.Report) ([]Unit, error) {
	if report == nil {
		return nil, errors.New("ingest: nil fault report")
	}
	if len(data) > lim.MaxStreamBytes {
		report.Addf(fault.CodeLimitExceeded, int64(lim.MaxStreamBytes),
			clock.MustRational(0, 1), clock.MustRational(0, 1),
			"stream %d bytes exceeds limit %d", len(data), lim.MaxStreamBytes)
		return nil, errors.New("ingest: stream exceeds size limit")
	}
	switch codec {
	case CodecH264, CodecH265:
		return scanLengthPrefixed(codec, data, lim, report), nil
	case CodecAV1:
		return scanAV1(data, lim, report), nil
	default:
		return nil, errors.New("ingest: unknown codec " + string(codec))
	}
}

func scanLengthPrefixed(codec Codec, data []byte, lim Limits, report *fault.Report) []Unit {
	var units []Unit
	zero := clock.MustRational(0, 1)
	off := 0
	for off < len(data) {
		unitOff := int64(off)
		if len(data)-off < avccPrefix {
			report.Addf(fault.CodeNALTruncated, unitOff, zero, zero,
				"%d trailing bytes cannot hold a length prefix", len(data)-off)
			return units
		}
		n := int(binary.BigEndian.Uint32(data[off:]))
		off += avccPrefix
		if n == 0 {
			report.Addf(fault.CodeNALIllegalLength, unitOff, zero, zero,
				"zero-length NAL unit")
			continue
		}
		if n > lim.MaxUnitBytes {
			report.Addf(fault.CodeNALIllegalLength, unitOff, zero, zero,
				"NAL length %d exceeds limit %d", n, lim.MaxUnitBytes)
			if n > len(data)-off { // cannot trust framing past the cut
				report.Addf(fault.CodeNALTruncated, unitOff, zero, zero,
					"declared %d bytes but only %d remain", n, len(data)-off)
				return units
			}
			off += n
			continue
		}
		if n > len(data)-off {
			report.Addf(fault.CodeNALTruncated, unitOff, zero, zero,
				"declared %d bytes but only %d remain", n, len(data)-off)
			return units
		}
		payload := data[off : off+n]
		typ, key := classifyNAL(codec, payload)
		if typ == nalTypeInvalid {
			report.Addf(fault.CodeNALIllegalLength, unitOff, zero, zero,
				"NAL header shorter than codec minimum")
			off += n
			continue
		}
		units = append(units, Unit{
			Codec: codec, Type: typ, IsKey: key,
			Offset: unitOff, PayloadOff: int64(off), Payload: payload,
			TimeFrom: zero, TimeTo: zero,
		})
		off += n
	}
	return units
}

const nalTypeInvalid = 0xFF

func classifyNAL(codec Codec, p []byte) (byte, bool) {
	switch codec {
	case CodecH264:
		if len(p) < 1 {
			return nalTypeInvalid, false
		}
		t := p[0] & 0x1F
		return t, t == 5 // IDR slice
	case CodecH265:
		if len(p) < 2 {
			return nalTypeInvalid, false
		}
		t := (p[0] >> 1) & 0x3F
		return t, t == 19 || t == 20 // IDR_W_RADL / IDR_N_LP
	default:
		return nalTypeInvalid, false
	}
}

// AV1 OBU types, see AV1 spec section 6.2.2.
const (
	obuSequenceHeader = 1
	obuTemporalDelim  = 2
	obuFrame          = 6
	obuKeyFrame       = 7
)

func scanAV1(data []byte, lim Limits, report *fault.Report) []Unit {
	var units []Unit
	zero := clock.MustRational(0, 1)
	off := 0
	for off < len(data) {
		unitOff := int64(off)
		header := data[off]
		if header&0x80 != 0 { // forbidden_bit
			report.Addf(fault.CodeOBUIllegalLength, unitOff, zero, zero,
				"OBU forbidden header bit set: 0x%02x", header)
			return units // framing is unrecoverable
		}
		typ := (header >> 3) & 0x0F
		hasExt := header&0x04 != 0
		hasSize := header&0x02 != 0
		off++
		if hasExt {
			if off >= len(data) {
				report.Addf(fault.CodeOBUTruncated, unitOff, zero, zero,
					"extension byte missing")
				return units
			}
			off++
		}
		if !hasSize {
			report.Addf(fault.CodeOBUIllegalLength, unitOff, zero, zero,
				"OBU without size field (only length-delivered OBUs supported)")
			return units
		}
		size, n := readLeb128(data[off:])
		if n == 0 {
			report.Addf(fault.CodeOBUTruncated, unitOff, zero, zero,
				"truncated OBU size leb128")
			return units
		}
		off += n
		if size == 0 && typ != obuTemporalDelim {
			report.Addf(fault.CodeOBUIllegalLength, unitOff, zero, zero,
				"zero-size OBU of type %d", typ)
			continue
		}
		if size > uint64(lim.MaxUnitBytes) {
			report.Addf(fault.CodeOBUIllegalLength, unitOff, zero, zero,
				"OBU size %d exceeds limit %d", size, lim.MaxUnitBytes)
			return units
		}
		if size > uint64(len(data)-off) {
			report.Addf(fault.CodeOBUTruncated, unitOff, zero, zero,
				"declared %d OBU bytes but only %d remain", size, len(data)-off)
			return units
		}
		payload := data[off : off+int(size)]
		units = append(units, Unit{
			Codec: CodecAV1, Type: typ,
			IsKey:      typ == obuSequenceHeader || typ == obuKeyFrame,
			Offset:     unitOff,
			PayloadOff: int64(off),
			Payload:    payload,
			TimeFrom:   zero, TimeTo: zero,
		})
		off += int(size)
	}
	return units
}

// readLeb128 decodes an unsigned LEB128 value, returning the value and the
// number of bytes consumed (0 means truncated input).
func readLeb128(b []byte) (uint64, int) {
	var v uint64
	for i := 0; i < len(b) && i < 8; i++ {
		v |= uint64(b[i]&0x7F) << (7 * i)
		if b[i]&0x80 == 0 {
			return v, i + 1
		}
	}
	return 0, 0
}
