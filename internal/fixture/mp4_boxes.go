package fixture

import (
	"mediapack/internal/binio"
)

func buildMoov(tracks []MP4TrackSpec) []byte {
	mvhd := binio.FullBox(0, 0, mvhdBody())
	moov := boxBytes("mvhd", mvhd)
	for _, tr := range tracks {
		moov = append(moov, buildTrak(tr)...)
	}
	return boxBytes("moov", moov)
}

func mvhdBody() []byte {
	b := make([]byte, 0, 96)
	b = append(b, binio.BE32(0)...)          // creation
	b = append(b, binio.BE32(0)...)          // modification
	b = append(b, binio.BE32(90000)...)      // movie timescale
	b = append(b, binio.BE32(0)...)          // duration
	b = append(b, binio.BE32(0x00010000)...) // rate 1.0
	b = append(b, 0x01, 0x00)                // volume
	b = append(b, make([]byte, 10)...)       // reserved
	b = append(b, identityMatrix()...)
	b = append(b, make([]byte, 24)...) // pre-defined
	b = append(b, binio.BE32(2)...)    // next track ID
	return b
}

func identityMatrix() []byte {
	m := make([]byte, 0, 36)
	for i := 0; i < 9; i++ {
		v := int32(0)
		if i == 0 || i == 4 || i == 8 {
			v = 0x00010000
		}
		m = append(m, binio.BE32(uint32(v))...)
	}
	return m
}

// rotationMatrix returns the 9x fixed-point matrix for 0/90/180/270 degrees.
func rotationMatrix(deg int32) []byte {
	var vals [9]int32
	switch deg {
	case 90:
		vals[0], vals[1], vals[3], vals[4], vals[8] = 0, 0x00010000, -0x00010000, 0, 0x40000000
	case 180:
		vals[0], vals[4], vals[8] = -0x00010000, -0x00010000, 0x40000000
	case 270:
		vals[0], vals[1], vals[3], vals[4], vals[8] = 0, -0x00010000, 0x00010000, 0, 0x40000000
	default:
		vals[0], vals[4], vals[8] = 0x00010000, 0x00010000, 0x40000000
	}
	out := make([]byte, 0, 36)
	for _, v := range vals {
		out = append(out, binio.BE32(uint32(v))...)
	}
	return out
}

func buildTrak(tr MP4TrackSpec) []byte {
	tkhd := buildTkhd(tr)
	mdhd := boxBytes("mdhd", binio.FullBox(0, 0, mdhdBody(tr)))
	hdlr := buildHdlr(tr)
	colr := buildColr(tr)
	clap := buildClap(tr)
	stbl := buildStbl(tr, colr, clap)
	minf := boxBytes("minf", stbl)
	mdia := boxBytes("mdia", concat(mdhd, hdlr, minf))
	return boxBytes("trak", concat(tkhd, mdia))
}

func concat(parts ...[]byte) []byte {
	n := 0
	for _, p := range parts {
		n += len(p)
	}
	out := make([]byte, 0, n)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func buildTkhd(tr MP4TrackSpec) []byte {
	// ISO 14496 tkhd version 0 body is exactly 80 bytes:
	// 5 u32 (creation, modification, trackID, reserved, duration),
	// 8 bytes reserved, layer u16, altGroup u16, volume u16, reserved u16,
	// 36-byte matrix, width u32, height u32.
	b := make([]byte, 0, 80)
	b = append(b, binio.BE32(0)...) // creation
	b = append(b, binio.BE32(0)...) // modification
	b = append(b, binio.BE32(tr.TrackID)...)
	b = append(b, binio.BE32(0)...) // reserved
	b = append(b, binio.BE32(0)...) // duration
	b = append(b, make([]byte, 8)...)
	b = append(b, binio.BE16(0)...) // layer
	b = append(b, binio.BE16(0)...) // alternate group
	vol := uint16(0)
	if tr.Handler == "soun" {
		vol = 0x0100
	}
	b = append(b, binio.BE16(vol)...)
	b = append(b, binio.BE16(0)...) // reserved
	b = append(b, rotationMatrix(tr.Rotation)...)
	b = append(b, binio.BE32(1920<<16)...) // width  (16.16 fixed point)
	b = append(b, binio.BE32(1080<<16)...) // height
	if len(b) != 80 {
		panic("tkhd body must be 80 bytes")
	}
	return boxBytes("tkhd", binio.FullBox(0, 0x07, b))
}

func mdhdBody(tr MP4TrackSpec) []byte {
	b := make([]byte, 0, 20)
	b = append(b, binio.BE32(0)...)
	b = append(b, binio.BE32(0)...)
	b = append(b, binio.BE32(tr.Timescale)...)
	b = append(b, binio.BE32(0)...) // duration patched by reader from stts
	b = append(b, 0x55, 0xc4)       // language 'und'
	b = append(b, 0, 0)             // pre-defined
	return b
}

func buildHdlr(tr MP4TrackSpec) []byte {
	b := make([]byte, 0, 32)
	b = append(b, binio.BE32(0)...) // pre-defined
	b = append(b, []byte(tr.Handler)...)
	b = append(b, make([]byte, 12)...) // reserved
	b = append(b, []byte(tr.Handler+"-handler")...)
	b = append(b, 0) // null terminator
	return boxBytes("hdlr", binio.FullBox(0, 0, b))
}

// buildColr emits an nclx colr box (colour_type nclx + CP/TC/matrix + range).
func buildColr(tr MP4TrackSpec) []byte {
	b := []byte("nclx")
	b = append(b, binio.BE16(tr.ColorPrim)...)
	b = append(b, binio.BE16(tr.ColorTRC)...)
	b = append(b, binio.BE16(tr.ColorMx)...)
	flag := byte(0)
	if tr.FullRange {
		flag = 0x80
	}
	b = append(b, flag, 0) // full-range flag + 7 reserved bits
	return boxBytes("colr", b)
}

// buildClap emits the clean-aperture box (four 16.16 fixed point ratios).
func buildClap(tr MP4TrackSpec) []byte {
	fp := func(v uint32) []byte { return binio.BE32(v << 16) }
	b := make([]byte, 0, 32)
	b = append(b, fp(1920-tr.CropL-tr.CropR)...) // cleanApertureWidthN
	b = append(b, fp(1)...)
	b = append(b, fp(1080-tr.CropT-tr.CropB)...) // cleanApertureHeightN
	b = append(b, fp(1)...)
	b = append(b, binio.BE32(0)...) // horiz/vert offset centered
	b = append(b, fp(1)...)
	b = append(b, binio.BE32(0)...)
	b = append(b, fp(1)...)
	return boxBytes("clap", b)
}

func buildStbl(tr MP4TrackSpec, colr, clap []byte) []byte {
	stsdEntry := buildStsdEntry(tr, colr, clap)
	stsd := binio.FullBox(0, 0, append(binio.BE32(1), stsdEntry...))
	stts := buildStts(tr)
	stsc := buildStsc(tr)
	stsz := buildStsz(tr)
	stco := buildStcoPlaceholder(tr)
	out := []byte{}
	out = append(out, boxBytes("stsd", stsd)...)
	out = append(out, boxBytes("stts", stts)...)
	out = append(out, boxBytes("stsc", stsc)...)
	out = append(out, boxBytes("stsz", stsz)...)
	out = append(out, boxBytes("stco", stco)...)
	return boxBytes("stbl", out)
}

func buildStsdEntry(tr MP4TrackSpec, colr, clap []byte) []byte {
	// Visual/sound sample entry: 6 reserved + 2 data_ref_index, then fields.
	b := make([]byte, 0, 78)
	b = append(b, make([]byte, 6)...)
	b = append(b, binio.BE16(1)...) // data reference index
	if tr.Handler == "vide" {
		b = append(b, make([]byte, 16)...)       // pre-defined + reserved
		b = append(b, binio.BE16(1920)...)       // width
		b = append(b, binio.BE16(1080)...)       // height
		b = append(b, binio.BE32(0x00480000)...) // horiz resolution 72dpi
		b = append(b, binio.BE32(0x00480000)...) // vert resolution
		b = append(b, binio.BE32(0)...)          // reserved
		b = append(b, binio.BE16(1)...)          // frame count
		b = append(b, make([]byte, 32)...)       // compressor name
		b = append(b, 0x00, 0x18)                // depth
		b = append(b, 0xff, 0xff)                // pre-defined
	} else {
		b = append(b, make([]byte, 8)...)       // audio reserved
		b = append(b, binio.BE16(2)...)         // channel count
		b = append(b, binio.BE16(16)...)        // sample size
		b = append(b, binio.BE16(0)...)         // pre-defined
		b = append(b, binio.BE16(0)...)         // reserved
		b = append(b, binio.BE32(48000<<16)...) // samplerate fixed point
	}
	b = append(b, clap...)
	b = append(b, colr...)
	return boxBytes(tr.Codec, b)
}

func buildStts(tr MP4TrackSpec) []byte {
	b := binio.BE32(1)
	b = append(b, binio.BE32(uint32(tr.Count))...)
	b = append(b, binio.BE32(tr.Timescale/25)...)
	return binio.FullBox(0, 0, b)
}

func buildStsc(tr MP4TrackSpec) []byte {
	b := binio.BE32(1)
	b = append(b, binio.BE32(1)...) // first chunk
	b = append(b, binio.BE32(1)...) // one sample per chunk
	b = append(b, binio.BE32(1)...) // description index
	return binio.FullBox(0, 0, b)
}

func buildStsz(tr MP4TrackSpec) []byte {
	b := binio.BE32(0) // uniform size expressed per entry
	b = append(b, binio.BE32(uint32(tr.Count))...)
	for i := 0; i < tr.Count; i++ {
		b = append(b, binio.BE32(uint32(tr.Size))...)
	}
	return binio.FullBox(0, 0, b)
}

// buildStcoPlaceholder emits zero offsets; patchStco fills them after mdat is
// positioned. Count equals sample count (one sample per chunk).
func buildStcoPlaceholder(tr MP4TrackSpec) []byte {
	b := binio.BE32(uint32(tr.Count))
	b = append(b, make([]byte, 4*tr.Count)...)
	return binio.FullBox(0, 0, b)
}
