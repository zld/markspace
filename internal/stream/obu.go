package stream

import (
	perr "gamepack/internal/errors"
)

// obuHeader describes the first byte(s) of an AV1 OBU.
type obuHeader struct {
	Type      uint8
	HasSize   bool
	HeaderLen int
}

func parseOBUHeader(raw []byte, at int64) (obuHeader, error) {
	if at >= int64(len(raw)) {
		return obuHeader{}, perr.At(subsystem, perr.CodeTruncatedUnit, at,
			"OBU header runs past end of stream")
	}
	h := raw[at]
	if h&0x80 == 0 {
		return obuHeader{}, perr.At(subsystem, perr.CodeInvalidEncoding, at,
			"OBU forbidden bit set")
	}
	oh := obuHeader{
		Type:      (h >> 3) & 0x0f,
		HasSize:   h&0x02 != 0,
		HeaderLen: 1,
	}
	if h&0x01 != 0 { // extension flag
		oh.HeaderLen = 2
		if at+1 >= int64(len(raw)) {
			return obuHeader{}, perr.At(subsystem, perr.CodeTruncatedUnit, at,
				"OBU extension byte truncated")
		}
	}
	if !oh.HasSize {
		return obuHeader{}, perr.At(subsystem, perr.CodeInvalidEncoding, at,
			"only length-delimited OBUs are supported")
	}
	return oh, nil
}

func readLeb128(raw []byte, at int64) (value int64, consumed int, err error) {
	var v int64
	for i := int64(0); i < 8; i++ {
		pos := at + i
		if pos >= int64(len(raw)) {
			return 0, 0, perr.At(subsystem, perr.CodeTruncatedUnit, at,
				"OBU leb128 size truncated at byte %d", pos)
		}
		b := raw[pos]
		v |= int64(b&0x7f) << (7 * i)
		if b&0x80 == 0 {
			return v, int(i + 1), nil
		}
	}
	return 0, 0, perr.At(subsystem, perr.CodeInvalidEncoding, at,
		"OBU leb128 size too long")
}

func scanOBUs(raw []byte) ([]Unit, error) {
	var units []Unit
	at := int64(0)
	ordinal := 0
	for at < int64(len(raw)) {
		unitStart := at
		oh, err := parseOBUHeader(raw, at)
		if err != nil {
			return nil, err
		}
		sizePos := at + int64(oh.HeaderLen)
		size, sizeLen, err := readLeb128(raw, sizePos)
		if err != nil {
			return nil, err
		}
		payloadStart := sizePos + int64(sizeLen)
		end := payloadStart + size
		if end > int64(len(raw)) || end < payloadStart {
			return nil, perr.At(subsystem, perr.CodeTruncatedUnit, unitStart,
				"OBU type %d declares %d bytes but stream ends at %d",
				oh.Type, size, len(raw))
		}
		// A keyframe is anchored by a sequence header (type 1) OBU; padding
		// OBUs (type 3) do not represent samples and are skipped.
		key := oh.Type == 1
		if oh.Type != 3 {
			units = append(units, Unit{
				Ordinal: ordinal, Type: oh.Type, Start: unitStart, End: end,
				Key: key, Data: append([]byte(nil), raw[unitStart:end]...),
			})
			ordinal++
		}
		at = end
	}
	return units, nil
}
