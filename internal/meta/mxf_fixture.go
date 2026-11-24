package meta

import "encoding/binary"

// MXFFixture builds byte streams in the kernel's MXF Local Set dialect. The
// generator is deliberately co-located with the parser so the dialect and its
// test inputs can never drift apart.

// MXFInput is one track's authored metadata as encoded into a fixture.
type MXFInput struct {
	TrackNo  uint32
	Kind     string
	ClockNum uint32
	ClockDen uint32
	Width    uint32
	Height   uint32
	Rotation uint32
	Crop     Crop
	Color    Color
}

// BuildMXF encodes tracks as concatenated KLV Local Sets.
func BuildMXF(tracks []MXFInput) []byte {
	var out []byte
	for _, tr := range tracks {
		var body []byte
		body = appendItem(body, tagTrackNumber, u32b(tr.TrackNo))
		body = appendItem(body, tagTrackKind, []byte(tr.Kind))
		body = appendItem(body, tagClockNum, u32b(tr.ClockNum))
		body = appendItem(body, tagClockDen, u32b(tr.ClockDen))
		body = appendItem(body, tagWidth, u32b(tr.Width))
		body = appendItem(body, tagHeight, u32b(tr.Height))
		body = appendItem(body, tagRotation, u32b(tr.Rotation))
		body = appendItem(body, tagCropX, u32b(tr.Crop.X))
		body = appendItem(body, tagCropY, u32b(tr.Crop.Y))
		body = appendItem(body, tagCropW, u32b(tr.Crop.Width))
		body = appendItem(body, tagCropH, u32b(tr.Crop.Height))
		body = appendItem(body, tagColorPrim, []byte(tr.Color.Primaries))
		body = appendItem(body, tagColorTrans, []byte(tr.Color.Transfer))
		body = appendItem(body, tagColorMatrix, []byte(tr.Color.Matrix))

		key := make([]byte, 16)
		binary.BigEndian.PutUint64(key[:8], trackSetPrefix)
		binary.BigEndian.PutUint64(key[8:], uint64(tr.TrackNo))
		out = append(out, key...)
		out = append(out, encodeBER(int64(len(body)))...)
		out = append(out, body...)
	}
	return out
}

// CorruptMXFTruncatedKey returns bytes that end inside a KLV key.
func CorruptMXFTruncatedKey() []byte {
	good := BuildMXF([]MXFInput{validMXFTrack(1)})
	return append(good, 0x06, 0x0e, 0x2b)
}

// CorruptMXFBadBER returns a well-formed key followed by an illegal BER.
func CorruptMXFBadBER() []byte {
	key := make([]byte, 16)
	binary.BigEndian.PutUint64(key[:8], trackSetPrefix)
	return append(key, 0x85) // long form claiming 5 bytes with none following
}

// CorruptMXFBodyOverrun declares a body longer than the bytes present.
func CorruptMXFBodyOverrun() []byte {
	key := make([]byte, 16)
	binary.BigEndian.PutUint64(key[:8], trackSetPrefix)
	return append(key, 0x20, 0xaa, 0xbb) // length 32, only 2 body bytes
}

// CorruptMXFInvalidCrop encodes a track whose crop extends past the frame.
func CorruptMXFInvalidCrop() []byte {
	bad := validMXFTrack(2)
	bad.Crop = Crop{X: 100, Y: 0, Width: 2000, Height: 1080}
	return BuildMXF([]MXFInput{bad})
}

// CorruptMXFShortItem gives a local item a length larger than its set.
func CorruptMXFShortItem() []byte {
	key := make([]byte, 16)
	binary.BigEndian.PutUint64(key[:8], trackSetPrefix)
	body := []byte{
		0x3c, 0x0a, 0x00, 0x10, // tag track number, length 16, no payload
	}
	out := append(key, encodeBER(int64(len(body)))...)
	return append(out, body...)
}

// ValidMXFTrack exposes a known-good track for cross-package fixtures.
func ValidMXFTrack(no uint32) MXFInput { return validMXFTrack(no) }

func validMXFTrack(no uint32) MXFInput {
	return MXFInput{
		TrackNo: no, Kind: "video", ClockNum: 90000, ClockDen: 1,
		Width: 1920, Height: 1080, Rotation: 0,
		Crop:  Crop{X: 0, Y: 0, Width: 1920, Height: 1080, Source: SourceMXF},
		Color: Color{Primaries: "bt709", Transfer: "bt709", Matrix: "bt709", Source: SourceMXF},
	}
}

func appendItem(dst []byte, tag uint16, value []byte) []byte {
	hdr := make([]byte, 4)
	binary.BigEndian.PutUint16(hdr[0:2], tag)
	binary.BigEndian.PutUint16(hdr[2:4], uint16(len(value)))
	return append(append(dst, hdr...), value...)
}

func u32b(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}

func encodeBER(v int64) []byte {
	if v < berLongFlag {
		return []byte{byte(v)}
	}
	var raw []byte
	for x := v; x > 0; x >>= 8 {
		raw = append([]byte{byte(x)}, raw...)
	}
	return append([]byte{berLongFlag | byte(len(raw))}, raw...)
}
