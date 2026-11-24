package meta

import (
	"encoding/binary"
	"fmt"

	"mediad/internal/clock"
	"mediad/internal/model"
)

// MXF parsing uses only the standard library. The kernel's MXF dialect stores
// each track as a Local Set (SMPTE ST 379-4 style): a 16-byte UL key, BER
// length, then tag(2 bytes)+length(2 bytes, BER-compatible uint16 here)+value
// triples. The tag layout below is fixed and shared by the fixture builder.

const (
	berLongFlag = 0x80
	uint16Bytes = 2
	uint32Bytes = 4
	// trackSetPrefix tags the Track Local Set.
	trackSetPrefix = 0x060e2b3402530101
)

// Local tag numbers inside a Track Local Set.
const (
	tagTrackNumber uint16 = 0x3c0a
	tagTrackKind   uint16 = 0x3c0b
	tagClockNum    uint16 = 0x3c01
	tagClockDen    uint16 = 0x3c02
	tagWidth       uint16 = 0x3c03
	tagHeight      uint16 = 0x3c04
	tagRotation    uint16 = 0x3c05
	tagCropX       uint16 = 0x3c06
	tagCropY       uint16 = 0x3c07
	tagCropW       uint16 = 0x3c08
	tagCropH       uint16 = 0x3c09
	tagColorPrim   uint16 = 0x3c0c
	tagColorTrans  uint16 = 0x3c0d
	tagColorMatrix uint16 = 0x3c0e
)

// ParseMXF walks the KLV stream and decodes every Track Local Set.
//
// Structural failures (truncated KLV, bad BER, malformed Local Set) become
// META_UNPARSEABLE errors carrying the absolute byte offset. A track whose
// payload is unparsable is skipped while later tracks are still attempted.
func ParseMXF(data []byte) *Result {
	res := &Result{}
	off := int64(0)
	for off < int64(len(data)) {
		if len(data)-int(off) < 16 {
			res.Errors = append(res.Errors, model.New(model.ErrMetaUnparseable,
				"truncated KLV key, %d bytes remain", len(data)-int(off)).AtOffset(off))
			return res
		}
		prefix := binary.BigEndian.Uint64(data[off : off+8])
		keyEnd := off + 16
		length, n, err := decodeBER(data[keyEnd:])
		if err != nil {
			res.Errors = append(res.Errors, model.New(model.ErrMetaUnparseable,
				"bad BER length: %v", err).AtOffset(keyEnd))
			return res
		}
		bodyStart := keyEnd + int64(n)
		bodyEnd := bodyStart + length
		if bodyEnd > int64(len(data)) {
			res.Errors = append(res.Errors, model.New(model.ErrMetaUnparseable,
				"KLV body length %d exceeds container (%d bytes available)", length, len(data)-int(bodyStart)).
				AtOffset(off))
			return res
		}
		if prefix == trackSetPrefix {
			t, perr := parseMXFTrack(data[bodyStart:bodyEnd], bodyStart)
			if perr != nil {
				res.Errors = append(res.Errors, perr)
			} else {
				res.Tracks = append(res.Tracks, t)
			}
		}
		off = bodyEnd
	}
	return res
}

// decodeBER reads an SMPTE BER-encoded length (short form, or long form with
// up to 8 continuation bytes).
func decodeBER(b []byte) (int64, int, error) {
	if len(b) == 0 {
		return 0, 0, fmt.Errorf("empty length field")
	}
	first := b[0]
	if first&berLongFlag == 0 {
		return int64(first), 1, nil
	}
	count := int(first &^ berLongFlag)
	if count == 0 || count > 8 || len(b) < 1+count {
		return 0, 0, fmt.Errorf("invalid long-form BER header 0x%02x", first)
	}
	var v int64
	for i := 1; i <= count; i++ {
		v = v<<8 | int64(b[i])
	}
	if v < 0 {
		return 0, 0, fmt.Errorf("BER length overflows int64")
	}
	return v, 1 + count, nil
}

func parseMXFTrack(body []byte, base int64) (*TrackMeta, *model.MediaError) {
	t := &TrackMeta{RotSrc: SourceMXF}
	got := map[uint16][]byte{}
	pos := 0
	for pos < len(body) {
		if len(body)-pos < 4 {
			return nil, model.New(model.ErrMetaUnparseable,
				"truncated local item header, %d bytes remain", len(body)-pos).
				AtOffset(base + int64(pos))
		}
		tag := binary.BigEndian.Uint16(body[pos : pos+2])
		itemLen := int(binary.BigEndian.Uint16(body[pos+2 : pos+4]))
		pos += 4
		if itemLen > len(body)-pos {
			return nil, model.New(model.ErrMetaUnparseable,
				"local item 0x%04x length %d exceeds set", tag, itemLen).
				AtOffset(base + int64(pos))
		}
		got[tag] = body[pos : pos+itemLen]
		pos += itemLen
	}

	num := u32(got, tagTrackNumber)
	t.TrackNo = int(num)
	t.Kind = string(got[tagTrackKind])
	t.Clock = clock.Rate{Num: int64(u32(got, tagClockNum)), Den: int64(u32(got, tagClockDen))}
	if !t.Clock.Valid() {
		return nil, model.New(model.ErrMetaUnparseable, "invalid clock rate %+v", t.Clock).
			AtTrack(t.TrackNo).AtOffset(base)
	}
	t.Width, t.Height = u32(got, tagWidth), u32(got, tagHeight)
	t.Rotation = int(int32(u32(got, tagRotation)))
	t.Crop = Crop{
		X: u32(got, tagCropX), Y: u32(got, tagCropY),
		Width: u32(got, tagCropW), Height: u32(got, tagCropH), Source: SourceMXF,
	}
	// Missing crop fields are an explicit default: full frame. Audio tracks
	// carry no frame, so they have no crop to validate.
	if _, ok := got[tagCropW]; !ok {
		t.Crop = FullFrame(t.Width, t.Height)
	}
	if t.Width > 0 && t.Height > 0 {
		if err := validateCrop(t.Crop, t.Width, t.Height, t.TrackNo, base); err != nil {
			return nil, err
		}
	}
	t.Color = Color{
		Primaries: string(got[tagColorPrim]), Transfer: string(got[tagColorTrans]),
		Matrix: string(got[tagColorMatrix]), Source: SourceMXF,
	}
	return t, nil
}

func u32(m map[uint16][]byte, tag uint16) uint32 {
	v, ok := m[tag]
	if !ok || len(v) != uint32Bytes {
		return 0
	}
	return binary.BigEndian.Uint32(v)
}

func validateCrop(c Crop, width, height uint32, trackNo int, off int64) *model.MediaError {
	if c.Width == 0 || c.Height == 0 {
		return model.New(model.ErrInvalidCrop, "zero-size crop %dx%d", c.Width, c.Height).
			AtTrack(trackNo).AtOffset(off)
	}
	if uint64(c.X)+uint64(c.Width) > uint64(width) ||
		uint64(c.Y)+uint64(c.Height) > uint64(height) {
		return model.New(model.ErrInvalidCrop,
			"crop (%d,%d %dx%d) lies outside frame %dx%d", c.X, c.Y, c.Width, c.Height, width, height).
			AtTrack(trackNo).AtOffset(off)
	}
	return nil
}
