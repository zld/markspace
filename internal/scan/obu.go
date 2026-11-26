package scan

import (
	"errors"
	"io"

	"regress/internal/fault"
)

// splitOBU parses AV1 low-overhead bitstream format: each OBU has a header
// byte, an optional extension byte, an unsigned LEB128 size and a payload.
// Header, extension and LEB bytes are retained in NALUnit.Data so downstream
// decoder-configuration writers can reproduce the exact on-disk unit.
func splitOBU(r io.ByteReader) ([]NALUnit, int64, error) {
	var (
		units []NALUnit
		pos   int64
	)
	readByte := func(what string, hdrPos int64) (byte, error) {
		b, err := r.ReadByte()
		if errors.Is(err, io.EOF) {
			return 0, fault.At(fault.StageScan, fault.CodeTruncatedTail, CodecAV1,
				hdrPos, "truncated OBU "+what)
		}
		if err != nil {
			return 0, fault.New(fault.StageScan, fault.CodeIO, CodecAV1, err.Error()).Wrap(err)
		}
		pos++
		return b, nil
	}
	for {
		hdr, err := r.ReadByte()
		if errors.Is(err, io.EOF) {
			return units, pos, nil
		}
		if err != nil {
			return nil, pos, fault.New(fault.StageScan, fault.CodeIO, CodecAV1, err.Error()).Wrap(err)
		}
		hdrPos := pos
		pos++
		obuType := (hdr >> 3) & 0x0f
		// OBU header (AV1 spec 5.3.2), MSB to LSB:
		//   obu_forbidden_bit(1) obu_type(4) obu_extension_flag(1)
		//   obu_has_size_field(1) obu_reserved_1bit(1)
		// The low-overhead bitstream format requires every OBU to be followed
		// by an unsigned LEB128 size regardless of the has-size bit, so the
		// size is always read here. The extension flag is bit 2 (0x04).
		hasExtension := hdr&0x04 != 0
		data := []byte{hdr}
		if hasExtension {
			ext, err := readByte("extension byte", hdrPos)
			if err != nil {
				return nil, pos, err
			}
			data = append(data, ext)
		}
		size, leb, err := readLEB128(r)
		if err != nil {
			return nil, pos, fault.At(fault.StageScan, fault.CodeTruncatedTail, CodecAV1,
				hdrPos, "truncated OBU size").Wrap(err)
		}
		pos += int64(len(leb))
		data = append(data, leb...)
		if size > maxUnitBytes {
			return nil, pos, fault.At(fault.StageScan, fault.CodeTruncatedTail, CodecAV1,
				hdrPos, "OBU exceeds bounded size")
		}
		payload := make([]byte, size)
		for i := range payload {
			b, err := readByte("payload", hdrPos)
			if err != nil {
				return nil, pos, err
			}
			payload[i] = b
		}
		data = append(data, payload...)
		k, rap := classifyOBU(obuType, payload)
		units = append(units, NALUnit{Offset: hdrPos, Kind: k, IsRAP: rap, Data: data})
	}
}

func classifyOBU(obuType byte, payload []byte) (kind int, rap bool) {
	switch obuType {
	case 1:
		return kindSequenceHeader, false
	case 2:
		return kindTemporalDelimiter, false
	case 3:
		return kindOther, false // tile group / tile list
	case 6:
		// Frame header OBU: top 3 bits of the uncompressed header are
		// frame_type; 0 means KEY_FRAME.
		if len(payload) > 0 && payload[0]>>5 == 0 {
			return kindFrameHeader, true
		}
		return kindFrameHeader, false
	default:
		return kindOther, false
	}
}

// readLEB128 consumes an unsigned LEB128 integer, enforcing the 8-byte cap,
// and returns both the value and the raw encoded bytes.
func readLEB128(r io.ByteReader) (uint64, []byte, error) {
	var v uint64
	var raw []byte
	for i := 0; i < 8; i++ {
		b, err := r.ReadByte()
		if err != nil {
			return 0, raw, err
		}
		raw = append(raw, b)
		v |= uint64(b&0x7f) << (7 * uint(i))
		if b&0x80 == 0 {
			return v, raw, nil
		}
	}
	return 0, raw, errors.New("LEB128 exceeds 8 bytes")
}
