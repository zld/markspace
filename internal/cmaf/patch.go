package cmaf

import (
	"encoding/binary"

	"rtx/internal/model"
)

// PatchTFDT returns a copy of data with one track's base media decode time
// replaced. The replacement keeps the original field width, so every other
// byte offset in the file stays valid. A value that does not fit the field is
// rejected rather than truncated.
func PatchTFDT(data []byte, track *TrackFragment, value uint64) ([]byte, error) {
	if track.TFDTOffset < 0 {
		return nil, model.E(model.CodeInvalidInput, "patch tfdt", "negative tfdt offset").WithByte(track.TFDTOffset)
	}
	out := make([]byte, len(data))
	copy(out, data)
	switch track.TFDTVersion {
	case 0:
		if value > 0xffffffff {
			return nil, model.E(model.CodeInvalidInput, "patch tfdt",
				"value %d does not fit a version 0 tfdt", value).WithByte(track.TFDTOffset)
		}
		if track.TFDTOffset+4 > int64(len(out)) {
			return nil, model.E(model.CodeTruncatedBox, "patch tfdt",
				"tfdt field at %d is outside the %d byte fragment", track.TFDTOffset, len(out)).WithByte(track.TFDTOffset)
		}
		binary.BigEndian.PutUint32(out[track.TFDTOffset:], uint32(value))
	case 1:
		if track.TFDTOffset+8 > int64(len(out)) {
			return nil, model.E(model.CodeTruncatedBox, "patch tfdt",
				"tfdt field at %d is outside the %d byte fragment", track.TFDTOffset, len(out)).WithByte(track.TFDTOffset)
		}
		binary.BigEndian.PutUint64(out[track.TFDTOffset:], value)
	default:
		return nil, model.E(CodeCMAFInvalid, "patch tfdt", "unsupported tfdt version %d", track.TFDTVersion)
	}
	return out, nil
}

// SampleData returns the payload bytes of a sample that lie inside data.
func SampleData(data []byte, s Sample) ([]byte, error) {
	if s.DataOffset < 0 || s.DataOffset+int64(s.Size) > int64(len(data)) {
		return nil, model.E(model.CodeTruncatedBox, "read sample",
			"sample at %d+%d escapes the %d byte fragment", s.DataOffset, s.Size, len(data)).
			WithByte(s.DataOffset)
	}
	return data[s.DataOffset : s.DataOffset+int64(s.Size)], nil
}
