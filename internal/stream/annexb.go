package stream

import (
	perr "gamepack/internal/errors"
)

// findStartCodes returns the start offset (after the 3/4-byte prefix) of
// every Annex B start code in raw together with the prefix length.
func findStartCodes(raw []byte) []codePos {
	var starts []codePos
	i := 0
	for i+3 <= len(raw) {
		if raw[i] == 0 && raw[i+1] == 0 && raw[i+2] == 1 {
			starts = append(starts, codePos{data: int64(i + 3), prefix: 3})
			i += 3
			continue
		}
		if i+4 <= len(raw) && raw[i] == 0 && raw[i+1] == 0 &&
			raw[i+2] == 0 && raw[i+3] == 1 {
			starts = append(starts, codePos{data: int64(i + 4), prefix: 4})
			i += 4
			continue
		}
		i++
	}
	return starts
}

type codePos struct {
	data   int64 // first payload byte after the start-code prefix
	prefix int64
}

// checkTrailingPrefix rejects a stream that ends inside an incomplete start
// code (two or more trailing 0x00 bytes not consumed by a complete prefix).
// A single trailing 0x00 is legal NAL trailing data and accepted.
func checkTrailingPrefix(raw []byte) error {
	n := len(raw)
	zeros := 0
	for zeros < n && raw[n-1-zeros] == 0 {
		zeros++
	}
	if zeros >= 2 {
		return perr.At(subsystem, perr.CodeTruncatedUnit, int64(n-zeros),
			"stream ends inside a start-code prefix")
	}
	return nil
}

func scanAnnexB(codec Codec, raw []byte) ([]Unit, error) {
	if err := checkTrailingPrefix(raw); err != nil {
		return nil, err
	}
	starts := findStartCodes(raw)
	if len(starts) == 0 {
		return nil, perr.At(subsystem, perr.CodeInvalidEncoding, 0,
			"no Annex B start code found")
	}
	units := make([]Unit, 0, len(starts))
	for n, sc := range starts {
		var end int64
		if n+1 < len(starts) {
			next := starts[n+1]
			end = next.data - next.prefix
		} else {
			end = int64(len(raw))
		}
		if end <= sc.data {
			return nil, perr.At(subsystem, perr.CodeTruncatedUnit, sc.data,
				"empty NAL unit at byte %d", sc.data)
		}
		data := raw[sc.data:end]
		nalType := annexBType(codec, data[0])
		key := isAnnexBKey(codec, nalType)
		units = append(units, Unit{
			Ordinal: n, Type: nalType, Start: sc.data, End: end,
			Key: key, Data: append([]byte(nil), data...),
		})
	}
	if last := units[len(units)-1]; last.End != int64(len(raw)) {
		return nil, perr.At(subsystem, perr.CodeTruncatedUnit, last.End,
			"trailing %d bytes do not form a complete NAL unit",
			int64(len(raw))-last.End)
	}
	return units, nil
}

func annexBType(codec Codec, first byte) uint8 {
	if codec == CodecH265 {
		return (first >> 1) & 0x3f
	}
	return first & 0x1f
}

func isAnnexBKey(codec Codec, nalType uint8) bool {
	if codec == CodecH265 {
		// HEVC IDR_W_RADL=19, IDR_N_LP=20, CRA=21.
		return nalType == 19 || nalType == 20 || nalType == 21
	}
	// AVC IDR slice = 5.
	return nalType == 5
}
