// Package scan parses H.264/H.265 Annex B and AV1 low-overhead OBU
// elementary streams byte by byte and validates externally supplied keyframe
// indexes against the parsed access units.
//
// The scanners are streaming: data is read through a fixed-size buffer and
// only the pending NAL/OBU unit, the accumulated access units and the caller
// supplied index are held in memory.
package scan

import (
	"bufio"
	"errors"
	"fmt"
	"io"

	"regress/internal/fault"
)

// Codec identifiers used across the pipeline.
const (
	CodecH264 = "h264"
	CodecH265 = "h265"
	CodecAV1  = "av1"
)

// NAL kinds (H.264/H.265) and OBU kinds (AV1) share one enumeration so that
// access unit assembly code below stays codec agnostic. The AV1-specific
// values deliberately match the OBU type numbers (sequence header=1,
// temporal delimiter=2, frame header=6); Annex B classifiers map their own
// NAL types onto these semantic categories.
const (
	kindUnknown = iota
	kindSequenceHeader // 1: AV1 OBU sequence header
	kindTemporalDelimiter // 2: AV1 OBU temporal delimiter
	kindAUD             // 3
	kindSPS             // 4
	kindPPS             // 5
	kindFrameHeader     // 6: AV1 OBU frame header
	kindVCL             // 7
	kindVPS             // 8
	kindOther           // 9
)

// NALUnit is one Annex B NAL unit or one AV1 OBU.
type NALUnit struct {
	Offset int64  // byte offset of the start code (Annex B) or OBU header (AV1)
	Kind   int
	IsRAP  bool   // random access point marker on a VCL/frame-header unit
	Data   []byte // full unit including header bytes, excluding the start code
}

// AccessUnit is one complete coded picture plus its parameter sets.
type AccessUnit struct {
	Index      int // ordinal picture index, zero based
	Offset     int64
	Size       int64
	IsRAP      bool
	NALs       []NALUnit
	ConfigData []byte // SPS/PPS (and VPS) or AV1 sequence header payload
}

// ScanResult is the parsed view of an elementary stream.
type ScanResult struct {
	Codec       string
	AccessUnits []AccessUnit
	Keyframes   []int // access-unit indices flagged as random access points
	StreamBytes int64
}

// RAPOffsets returns the byte offsets of every random access point in order.
func (s *ScanResult) RAPOffsets() []int64 {
	out := make([]int64, 0, len(s.Keyframes))
	for _, i := range s.Keyframes {
		out = append(out, s.AccessUnits[i].Offset)
	}
	return out
}

const maxUnitBytes = 8 * 1024 * 1024 // bounded memory for a single NAL/OBU unit

// ScanElementary parses a complete elementary stream. A truncated tail
// (dangling bytes without a terminating unit) is reported as a structured
// fault.CodeTruncatedTail error rather than silently dropped.
func ScanElementary(codec string, r io.Reader) (*ScanResult, error) {
	br := bufio.NewReaderSize(r, 64*1024)
	switch codec {
	case CodecH264, CodecH265:
		units, n, err := splitAnnexB(codec, br)
		if err != nil {
			return nil, err
		}
		return assemble(codec, units, n)
	case CodecAV1:
		units, n, err := splitOBU(br)
		if err != nil {
			return nil, err
		}
		return assemble(codec, units, n)
	default:
		return nil, fault.New(fault.StageScan, fault.CodeParse, codec, "unsupported codec: "+codec)
	}
}

// splitAnnexB emits NAL units via an explicit byte state machine. A start
// code is 00 00 01 or 00 00 00 01; the recorded offset points at its first
// byte. Emulation prevention bytes are preserved because only header bits are
// inspected.
func splitAnnexB(codec string, r io.ByteReader) ([]NALUnit, int64, error) {
	var (
		units     []NALUnit
		pos       int64
		zeroRun   int
		inUnit    bool
		startOff  int64
		buf       []byte
		flushUnit = func(end int64) error {
			if !inUnit || len(buf) == 0 {
				inUnit = false
				return nil
			}
			data := make([]byte, len(buf))
			copy(data, buf)
			k, rap := classifyAnnexB(codec, data)
			units = append(units, NALUnit{Offset: startOff, Kind: k, IsRAP: rap, Data: data})
			buf = buf[:0]
			inUnit = false
			_ = end
			return nil
		}
	)
	for {
		b, err := r.ReadByte()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, pos, fault.New(fault.StageScan, fault.CodeIO, codec, err.Error()).Wrap(err)
		}
		pos++

		if !inUnit {
			// Hunting state: look for 00 00 01.
			if b == 0 {
				zeroRun++
				continue
			}
			if b == 1 && zeroRun >= 2 {
				inUnit = true
				startOff = pos - 3 // first byte of the 00 00 01 start code
				zeroRun = 0
				buf = buf[:0]
			} else {
				zeroRun = 0
			}
			continue
		}

		// In-unit state. A zero may belong to this unit or begin the next
		// start code, so zeros are buffered tentatively and reclassified.
		if b != 0 && zeroRun > 0 {
			if b == 1 && zeroRun >= 2 {
				// Strip the tentative start-code zeros from the current unit.
				buf = buf[:len(buf)-(zeroRun-1)]
				if err := flushUnit(pos); err != nil {
					return nil, pos, err
				}
				inUnit = true
				startOff = pos - 3
				zeroRun = 0
				continue
			}
			zeroRun = 0
		}
		if len(buf) >= maxUnitBytes {
			return nil, pos, fault.At(fault.StageScan, fault.CodeTruncatedTail, codec,
				startOff, "NAL unit exceeds bounded size")
		}
		buf = append(buf, b)
		if b == 0 {
			zeroRun++
		}
	}
	if inUnit {
		if len(buf) == 0 {
			return nil, pos, fault.At(fault.StageScan, fault.CodeTruncatedTail, codec,
				startOff, "start code with no NAL unit before EOF")
		}
		if err := flushUnit(pos); err != nil {
			return nil, pos, err
		}
	} else if zeroRun > 0 {
		return nil, pos, fault.At(fault.StageScan, fault.CodeTruncatedTail, codec,
			pos-int64(zeroRun), fmt.Sprintf("%d dangling zero byte(s) after last NAL", zeroRun))
	}
	return units, pos, nil
}

func classifyAnnexB(codec string, data []byte) (kind int, rap bool) {
	if len(data) == 0 {
		return kindUnknown, false
	}
	switch codec {
	case CodecH264:
		t := data[0] & 0x1f
		switch {
		case t == 1 || t == 2 || t == 5:
			return kindVCL, t == 5
		case t == 7:
			return kindSPS, false
		case t == 8:
			return kindPPS, false
		case t == 9:
			return kindAUD, false
		default:
			return kindOther, false
		}
	case CodecH265:
		t := (data[0] >> 1) & 0x3f
		switch {
		case t <= 31: // VCL NAL types 0..31; IRAP types are 16..23
			return kindVCL, t >= 16 && t <= 23
		case t == 32:
			return kindVPS, false
		case t == 33:
			return kindSPS, false
		case t == 34:
			return kindPPS, false
		case t == 35:
			return kindAUD, false
		default:
			return kindOther, false
		}
	}
	return kindUnknown, false
}
