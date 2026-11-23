package ingest

import (
	"encoding/binary"
	"math"

	"mediapipe/internal/fault"
	"mediapipe/internal/limits"
	"mediapipe/internal/model"
)

// fullboxHdr returns the 4-byte version/flags prefix and skips it.
func fullboxHdr(b rawBox) (ver byte, flags uint32, body []byte, ok bool) {
	if len(b.payload) < 4 {
		return 0, 0, nil, false
	}
	ver = b.payload[0]
	flags = uint32(b.payload[1])<<16 | uint32(b.payload[2])<<8 | uint32(b.payload[3])
	return ver, flags, b.payload[4:], true
}

// parseMdhd reads timescale and duration from an mdhd box.
func parseMdhd(b rawBox) (timescale uint32, durationUnits uint64) {
	ver, _, body, ok := fullboxHdr(b)
	if !ok {
		return 0, 0
	}
	if ver == 1 {
		// creation(8) modification(8) timescale(4) duration(8)
		if len(body) < 28 {
			return 0, 0
		}
		timescale = binary.BigEndian.Uint32(body[16:])
		durationUnits = binary.BigEndian.Uint64(body[20:])
		return timescale, durationUnits
	}
	// creation(4) modification(4) timescale(4) duration(4)
	if len(body) < 16 {
		return 0, 0
	}
	timescale = binary.BigEndian.Uint32(body[8:])
	durationUnits = uint64(binary.BigEndian.Uint32(body[12:]))
	return timescale, durationUnits
}

// parseTkhd reads track id, dimensions and rotation matrix from a tkhd box.
func parseTkhd(b rawBox) (trackID uint32, w, h int64, rot model.Rotation) {
	ver, _, body, ok := fullboxHdr(b)
	if !ok {
		return 0, 0, 0, model.Rotation{}
	}
	var pos int
	if ver == 1 {
		// 8+8 creation/mod, 4+4 id/reserved, 8 duration, 8 reserved,
		// 2+2+2+2 layer/alt/volume/reserved => matrix at 48.
		pos = 48
	} else {
		// 4+4 creation/mod, 4+4 id/reserved, 4 duration, 8 reserved,
		// 2+2+2+2 layer/alt/volume/reserved => matrix at 40.
		pos = 40
	}
	if len(body) < pos+36+8 {
		return 0, 0, 0, model.Rotation{}
	}
	// Track ID sits at a fixed offset for both versions (after the two
	// 4-byte time fields): v0 = 8, v1 = 16.
	idOff := 8
	if ver == 1 {
		idOff = 16
	}
	trackID = binary.BigEndian.Uint32(body[idOff:])
	var matrix [9]int32
	for i := 0; i < 9; i++ {
		matrix[i] = int32(binary.BigEndian.Uint32(body[pos+i*4:]))
	}
	one := int32(0x00010000)
	deg := 0
	// ISO/Apple display-matrix convention for counter-clockwise rotation.
	switch matrix {
	case [9]int32{0, one, 0, -one, 0, 0, 0, 0, 0x40000000}:
		deg = 90
	case [9]int32{-one, 0, 0, 0, -one, 0, 0, 0, 0x40000000}:
		deg = 180
	case [9]int32{0, -one, 0, one, 0, 0, 0, 0, 0x40000000}:
		deg = 270
	}
	w = int64(binary.BigEndian.Uint32(body[pos+36:])) / 65536
	h = int64(binary.BigEndian.Uint32(body[pos+40:])) / 65536
	return trackID, w, h, model.Rotation{Degrees: deg, SourceOffset: b.offset, SourceBox: "tkhd"}
}

// parseHdlr returns the 4-byte handler type.
func parseHdlr(b rawBox) string {
	_, _, body, ok := fullboxHdr(b)
	if !ok || len(body) < 8 {
		return ""
	}
	return string(body[4:8])
}

// parseColr decodes an nclx color box; other color types are reported by tag.
func parseColr(b rawBox, file string, errs *fault.Batch) model.ColorDecl {
	decl := model.ColorDecl{
		ColorType: string(safeSlice(b.payload, 0, 4)),
		SourceBox: "colr",
		SourceOff: b.offset,
	}
	if decl.ColorType != "nclx" || len(b.payload) < 11 {
		decl.ProfileLen = len(b.payload) - 4
		return decl
	}
	decl.Primaries = binary.BigEndian.Uint16(b.payload[4:])
	decl.Transfer = binary.BigEndian.Uint16(b.payload[6:])
	decl.Matrix = binary.BigEndian.Uint16(b.payload[8:])
	decl.FullRange = b.payload[10]&0x80 != 0
	if len(b.payload) > limits.MaxPayloadBytes {
		errs.Add(limits.Exceeded(file, "colr profile", b.offset, len(b.payload),
			limits.MaxPayloadBytes))
	}
	return decl
}

// parseClap decodes clean-aperture geometry into pixel crops. The track's
// stored width/height are needed because clap expresses an aperture plus an
// offset rather than explicit margins.
func parseClap(b rawBox, width, height int64) *model.Crop {
	if len(b.payload) < 32 {
		return nil
	}
	get := func(i int) int64 {
		n := binary.BigEndian.Uint32(b.payload[i*4:])
		d := binary.BigEndian.Uint32(b.payload[i*4+4:])
		if d == 0 {
			return 0
		}
		return int64(math.Round(float64(n) / float64(d)))
	}
	cleanW := get(0)
	cleanH := get(2)
	offH := get(4)
	offV := get(6)
	// ISO 14496-12: centre of clean aperture relative to the pixel centre.
	left := (width - cleanW + 2*offH) / 2
	right := width - cleanW - left
	top := (height - cleanH + 2*offV) / 2
	bottom := height - cleanH - top
	if left < 0 {
		left = 0
	}
	if top < 0 {
		top = 0
	}
	if right < 0 {
		right = 0
	}
	if bottom < 0 {
		bottom = 0
	}
	return &model.Crop{Left: left, Top: top, Right: right, Bottom: bottom}
}

// parseElst returns the first entry's media start in track timescale units.
func parseElst(b rawBox) (mediaStartUnits int64) {
	ver, _, body, ok := fullboxHdr(b)
	if !ok || len(body) < 4 {
		return 0
	}
	count := binary.BigEndian.Uint32(body[0:])
	if count == 0 || (ver == 1 && len(body) < 24) || (ver == 0 && len(body) < 16) {
		return 0
	}
	if ver == 1 {
		return int64(binary.BigEndian.Uint64(body[12:])) // media_time v1
	}
	return int64(int32(binary.BigEndian.Uint32(body[8:12]))) // media_time v0
}

func safeSlice(b []byte, start, end int) []byte {
	if start < 0 || end > len(b) || start > end {
		return nil
	}
	return b[start:end]
}
