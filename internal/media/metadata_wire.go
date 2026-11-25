package media

import (
	"encoding/binary"
	"fmt"

	"media-audit/internal/timebase"
	"media-audit/internal/wire"
)

// EncodeTrackMeta is the deterministic payload used by MXF metadata KLVs and
// MP4 amet boxes.
func EncodeTrackMeta(id uint32, tb timebase.Timebase, duration int64, m Metadata) []byte {
	var b wire.Buffer
	b.Byte(1)
	b.U32(id)
	b.U32(tb.Numerator)
	b.U32(tb.Denominator)
	b.U64(uint64(duration))
	var rot uint32
	if m.HasRotation {
		rot = uint32(uint32(m.RotationDegrees))
	}
	b.U32(rot)
	if m.Crop == nil {
		b.Byte(0)
	} else {
		b.Byte(1)
		b.U32(uint32(m.Crop.Top))
		b.U32(uint32(m.Crop.Right))
		b.U32(uint32(m.Crop.Bottom))
		b.U32(uint32(m.Crop.Left))
	}
	if m.Color == nil {
		b.Byte(0)
	} else {
		b.Byte(1)
		b.U16(m.Color.Primaries)
		b.U16(m.Color.Transfer)
		b.U16(m.Color.Matrix)
		if m.Color.FullRange {
			b.Byte(1)
		} else {
			b.Byte(0)
		}
	}
	b.U32(m.MetadataVersion)
	return b.Bytes()
}

type reader struct {
	b []byte
	p int
}

func (r *reader) take(n int) ([]byte, error) {
	if r.p+n > len(r.b) {
		return nil, fmt.Errorf("need %d bytes at %d", n, r.p)
	}
	v := r.b[r.p : r.p+n]
	r.p += n
	return v, nil
}
func (r *reader) u8() (byte, error) {
	v, err := r.take(1)
	if err != nil {
		return 0, err
	}
	return v[0], nil
}
func (r *reader) u16() (uint16, error) {
	v, err := r.take(2)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint16(v), nil
}
func (r *reader) u32() (uint32, error) {
	v, err := r.take(4)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(v), nil
}
func (r *reader) u64() (uint64, error) {
	v, err := r.take(8)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint64(v), nil
}

func DecodeTrackMeta(payload []byte) (uint32, timebase.Timebase, int64, Metadata, error) {
	r := reader{b: payload}
	version, err := r.u8()
	if err != nil || version != 1 {
		return 0, timebase.Timebase{}, 0, Metadata{}, fmt.Errorf("unsupported metadata version %d", version)
	}
	id, _ := r.u32()
	num, _ := r.u32()
	den, _ := r.u32()
	dur, _ := r.u64()
	rot, _ := r.u32()
	m := Metadata{HasRotation: rot != 0, RotationDegrees: int32(rot)}
	if ok, _ := r.u8(); ok == 1 {
		top, _ := r.u32()
		right, _ := r.u32()
		bottom, _ := r.u32()
		left, _ := r.u32()
		m.Crop = &Crop{Top: int64(top), Right: int64(right), Bottom: int64(bottom), Left: int64(left)}
	}
	if ok, _ := r.u8(); ok == 1 {
		pri, _ := r.u16()
		tr, _ := r.u16()
		mat, _ := r.u16()
		full, _ := r.u8()
		m.Color = &Color{Primaries: pri, Transfer: tr, Matrix: mat, FullRange: full == 1}
	}
	mv, err := r.u32()
	if err != nil {
		return 0, timebase.Timebase{}, 0, Metadata{}, err
	}
	m.MetadataVersion = mv
	return id, timebase.MXF(num, den), int64(dur), m, nil
}
