package fixtures

import "encoding/binary"

// MXF 子集 UL 键（与 container 包注册表一致）。
var (
	ulHeaderPartition = []byte{0x06, 0x0e, 0x2b, 0x34, 0x02, 0x05, 0x01, 0x01, 0x0d, 0x01, 0x02, 0x01, 0x01, 0x02, 0x04, 0x00}
	ulTrackMeta       = []byte{0x06, 0x0e, 0x2b, 0x34, 0x02, 0x53, 0x01, 0x01, 0x0d, 0x01, 0x01, 0x01, 0x01, 0x01, 0x48, 0x00}
	ulColorDescriptor = []byte{0x06, 0x0e, 0x2b, 0x34, 0x02, 0x53, 0x01, 0x01, 0x0d, 0x01, 0x01, 0x01, 0x01, 0x01, 0x29, 0x00}
	ulEssence         = []byte{0x06, 0x0e, 0x2b, 0x34, 0x01, 0x02, 0x01, 0x01, 0x0d, 0x01, 0x03, 0x01, 0x15, 0x01, 0x05, 0x00}
)

// berLen 以 BER 编码长度（短形式或 4 字节长形式）。
func berLen(n int) []byte {
	if n < 0x80 {
		return []byte{byte(n)}
	}
	return []byte{0x84, byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)}
}

// klv 组装一个 KLV 三元组。
func klv(key, value []byte) []byte {
	out := append([]byte{}, key...)
	out = append(out, berLen(len(value))...)
	return append(out, value...)
}

// MXFTrack 描述一条待生成的 MXF 轨道。
type MXFTrack struct {
	ID          int
	Kind        string // "video" | "audio" | "meta"
	Name        string
	RateNum     uint32
	RateDen     uint32
	Origin      uint64
	Width       uint32
	Height      uint32
	Rotation    int
	Crop        *[4]uint32 // x, y, w, h
	Color       *[4]uint16 // 轨道级色彩
	AltColor    *[4]uint16 // 描述符级色彩（可冲突）
	SampleDur   uint32
	SampleSizes []uint32
	Seed        string
}

// BuildMXF 生成确定性 MXF 子集文件：头部分区 + 轨道元数据 + 色彩描述符 + essence。
func BuildMXF(tracks []MXFTrack) []byte {
	var out []byte
	header := make([]byte, 16)
	binary.BigEndian.PutUint32(header, 1) // 子集版本
	out = append(out, klv(ulHeaderPartition, header)...)
	for _, t := range tracks {
		out = append(out, klv(ulTrackMeta, mxfTrackValue(t))...)
	}
	for _, t := range tracks {
		if t.AltColor != nil {
			v := make([]byte, 0, 11)
			v = binary.BigEndian.AppendUint32(v, uint32(t.ID))
			v = binary.BigEndian.AppendUint16(v, t.AltColor[0])
			v = binary.BigEndian.AppendUint16(v, t.AltColor[1])
			v = binary.BigEndian.AppendUint16(v, t.AltColor[2])
			v = append(v, uint8(t.AltColor[3]))
			out = append(out, klv(ulColorDescriptor, v)...)
		}
	}
	for _, t := range tracks {
		for i, sz := range t.SampleSizes {
			v := make([]byte, 0, 8+sz)
			v = binary.BigEndian.AppendUint32(v, uint32(t.ID))
			v = binary.BigEndian.AppendUint32(v, uint32(i))
			v = append(v, pseudoBytes(t.Seed, int(sz))...)
			out = append(out, klv(ulEssence, v)...)
		}
	}
	return out
}

// field 组装一个本地集字段（tag/len/value）。
func field(tag uint16, val []byte) []byte {
	out := binary.BigEndian.AppendUint16(nil, tag)
	out = binary.BigEndian.AppendUint16(out, uint16(len(val)))
	return append(out, val...)
}

func fieldU32(tag uint16, v uint32) []byte { return field(tag, binary.BigEndian.AppendUint32(nil, v)) }
func fieldU64(tag uint16, v uint64) []byte { return field(tag, binary.BigEndian.AppendUint64(nil, v)) }

func mxfTrackValue(t MXFTrack) []byte {
	kind := map[string]uint8{"video": 1, "audio": 2, "meta": 3}[t.Kind]
	var v []byte
	v = append(v, fieldU32(0x01, uint32(t.ID))...)
	v = append(v, field(0x02, []byte{kind})...)
	v = append(v, fieldU32(0x03, t.RateNum)...)
	v = append(v, fieldU32(0x04, t.RateDen)...)
	if t.Origin != 0 {
		v = append(v, fieldU64(0x05, t.Origin)...)
	}
	dur := uint64(t.SampleDur) * uint64(len(t.SampleSizes))
	v = append(v, fieldU64(0x06, dur)...)
	if t.Kind == "video" {
		v = append(v, fieldU32(0x07, t.Width)...)
		v = append(v, fieldU32(0x08, t.Height)...)
	}
	if t.Name != "" {
		v = append(v, field(0x09, []byte(t.Name))...)
	}
	if t.Rotation != 0 {
		v = append(v, field(0x0a, binary.BigEndian.AppendUint16(nil, uint16(int16(t.Rotation))))...)
	}
	if t.Crop != nil {
		v = append(v, fieldU32(0x0b, t.Crop[0])...)
		v = append(v, fieldU32(0x0c, t.Crop[1])...)
		v = append(v, fieldU32(0x0d, t.Crop[2])...)
		v = append(v, fieldU32(0x0e, t.Crop[3])...)
	}
	if t.Color != nil {
		v = append(v, fieldU32(0x0f, uint32(t.Color[0]))...)
		v = append(v, fieldU32(0x10, uint32(t.Color[1]))...)
		v = append(v, fieldU32(0x11, uint32(t.Color[2]))...)
		v = append(v, field(0x12, []byte{uint8(t.Color[3])})...)
	}
	if len(t.SampleSizes) > 0 {
		v = append(v, fieldU32(0x13, t.SampleDur)...)
	}
	return v
}
