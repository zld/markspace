package scan

import "mediarc/internal/model"

// AV1 OBU types relevant to access-unit structure.
const (
	obuTemporalDelimiter = 2
	obuFrameHeader       = 3
	obuFrame             = 6
)

// leb128 decodes an AV1 unsigned leb128 value. It returns the value and the
// number of bytes consumed, or 0 bytes if the encoding is invalid/truncated.
func leb128(data []byte) (int, int) {
	v := 0
	for i := 0; i < 8 && i < len(data); i++ {
		v |= int(data[i]&0x7f) << (7 * i)
		if data[i]&0x80 == 0 {
			return v, i + 1
		}
	}
	return 0, 0
}

// ScanAV1 scans a low-overhead AV1 OBU stream into access units. A temporal
// delimiter starts a new AU. Truncation is reported at the offset of the OBU
// whose declared size exceeds the remaining data.
func ScanAV1(data []byte) ([]AU, *model.Error) {
	var aus []AU
	cur := -1
	for pos := 0; pos < len(data); {
		hdr := data[pos]
		if hdr&0x80 != 0 {
			return aus, &model.Error{Code: model.ErrMisaligned, Offset: int64(pos), Msg: "obu forbidden bit set"}
		}
		typ := int(hdr>>3) & 0x0f
		p := pos + 1
		if hdr&0x04 != 0 { // extension flag
			p++
		}
		if p > len(data) {
			return aus, &model.Error{Code: model.ErrTruncated, Offset: int64(pos), Msg: "obu header cut off"}
		}
		size := len(data) - p
		if hdr&0x02 != 0 { // has_size_field
			v, n := leb128(data[p:])
			if n == 0 {
				return aus, &model.Error{Code: model.ErrTruncated, Offset: int64(pos), Msg: "invalid leb128 size"}
			}
			size, p = v, p+n
		}
		if p+size > len(data) {
			return aus, &model.Error{Code: model.ErrTruncated, Offset: int64(pos), Msg: "obu size exceeds remaining data"}
		}
		switch typ {
		case obuTemporalDelimiter:
			cur = len(aus)
			aus = append(aus, AU{Offset: int64(pos)})
		case obuFrameHeader, obuFrame:
			if cur < 0 {
				cur = len(aus)
				aus = append(aus, AU{Offset: int64(pos)})
			}
			// Uncompressed header: show_existing_frame(1)=0, frame_type(2)=KEY.
			if p < len(data) && data[p]&0xe0 == 0 {
				aus[cur].Keyframe = true
			}
		}
		if cur >= 0 {
			aus[cur].Length = int64(p+size) - aus[cur].Offset
		}
		pos = p + size
	}
	return aus, nil
}
