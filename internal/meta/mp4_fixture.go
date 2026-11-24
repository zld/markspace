package meta

import (
	"encoding/binary"
	"strconv"
)

// MP4Input is one track's authored metadata as encoded into an MP4 fixture.
type MP4Input struct {
	TrackNo  uint32
	Kind     string
	ClockNum uint32
	Width    uint16
	Height   uint16
	Rotation int
	Crop     Crop
	Color    Color
	// OmitCrop / OmitColor leave the corresponding boxes out of the entry so
	// tests can exercise the parser's explicit defaults.
	OmitCrop  bool
	OmitColor bool
}

// BuildMP4 hand-encodes a minimal but spec-shaped MP4: ftyp + moov/trak
// hierarchy with tkhd, mdhd, hdlr and a stsd visual sample entry carrying clap
// and colr. Bytes are built explicitly so fixtures stay independent of any
// writer implementation in the third-party library.
func BuildMP4(tracks []MP4Input) []byte {
	var out []byte
	out = append(out, box("ftyp", append(append([]byte("isom"), u32b(0)...), []byte("isom")...))...)
	var moov []byte
	for _, tr := range tracks {
		moov = append(moov, buildTrak(tr)...)
	}
	out = append(out, box("moov", moov)...)
	return out
}

func buildTrak(tr MP4Input) []byte {
	tkhd := make([]byte, 88) // version 0 tkhd payload (FullBox + 84 bytes)
	binary.BigEndian.PutUint32(tkhd[12:], tr.TrackNo)   // track ID
	binary.BigEndian.PutUint32(tkhd[20:], 1000)         // duration
	putMatrix(tkhd[40:76], matrixFor(tr.Rotation))
	binary.BigEndian.PutUint32(tkhd[76:], uint32(tr.Width)<<16)
	binary.BigEndian.PutUint32(tkhd[80:], uint32(tr.Height)<<16)

	mdhd := make([]byte, 24) // version 0 mdhd payload
	binary.BigEndian.PutUint32(mdhd[12:], tr.ClockNum) // timescale
	binary.BigEndian.PutUint32(mdhd[16:], 1000)        // duration
	handler := "vide"
	if tr.Kind == "audio" {
		handler = "soun"
	}
	hdlr := make([]byte, 25)
	copy(hdlr[8:12], handler)

	var entry []byte
	if handler == "vide" {
		entry = buildVisualEntry(tr)
	} else {
		entry = buildAudioEntry()
	}
	// stsd is a FullBox: 4 zero bytes (version+flags), then entry count 1.
	stsdPayload := append(append(make([]byte, 4), u32b(1)...), entry...)
	stbl := box("stbl", box("stsd", stsdPayload))
	minf := box("minf", stbl)
	mdia := box("mdia", append(append(box("mdhd", mdhd), box("hdlr", hdlr)...), minf...))
	return box("trak", append(box("tkhd", tkhd), mdia...))
}

func buildVisualEntry(tr MP4Input) []byte {
	vse := make([]byte, 78)
	binary.BigEndian.PutUint16(vse[6:], 1) // data reference index
	binary.BigEndian.PutUint16(vse[24:], tr.Width)
	binary.BigEndian.PutUint16(vse[26:], tr.Height)
	binary.BigEndian.PutUint32(vse[28:], 0x00480000) // 72 dpi horiz
	binary.BigEndian.PutUint32(vse[32:], 0x00480000)
	binary.BigEndian.PutUint16(vse[38:], 1)          // frame count
	binary.BigEndian.PutUint16(vse[74:], 0x0018)     // depth
	binary.BigEndian.PutUint16(vse[76:], 0xffff)     // predefined

	clapPayload := make([]byte, 32)
	binary.BigEndian.PutUint32(clapPayload[0:], tr.Crop.Width<<16)
	binary.BigEndian.PutUint32(clapPayload[4:], tr.Crop.Height<<16)
	binary.BigEndian.PutUint32(clapPayload[8:], tr.Crop.X<<16)
	binary.BigEndian.PutUint32(clapPayload[12:], tr.Crop.Y<<16)

	colr := make([]byte, 18)
	copy(colr[0:4], "nclx")
	binary.BigEndian.PutUint16(colr[4:], nclxIndex(tr.Color.Primaries))
	binary.BigEndian.PutUint16(colr[6:], nclxIndex(tr.Color.Transfer))
	binary.BigEndian.PutUint16(colr[8:], nclxIndex(tr.Color.Matrix))
	colr[10] = 0 // full range flag + 7 reserved bytes stay zero

	var children []byte
	if !tr.OmitCrop {
		children = append(children, box("clap", clapPayload)...)
	}
	if !tr.OmitColor {
		children = append(children, box("colr", colr)...)
	}
	return box("avc1", append(vse, children...))
}

func buildAudioEntry() []byte {
	// AudioSampleEntry v0: SampleEntry(8) + entry version(2) + reserved(6)
	// + channel count(2) + sample size(2) + predefined(2) + reserved2(2)
	// + sample rate 16.16(4) = 28 bytes.
	payload := make([]byte, 28)
	binary.BigEndian.PutUint16(payload[6:], 1)              // data reference index
	binary.BigEndian.PutUint16(payload[16:], 2)             // stereo
	binary.BigEndian.PutUint16(payload[18:], 16)            // sample size bits
	binary.BigEndian.PutUint32(payload[24:], 48000<<16)     // 48 kHz 16.16
	return box("mp4a", payload)
}

// CorruptMP4TruncatedHeader gives moov a valid-looking size then cuts the
// stream inside its first child box, producing an unexpected EOF at a known
// byte offset.
func CorruptMP4TruncatedHeader() []byte {
	data := BuildMP4([]MP4Input{validMP4Track(1)})
	// Locate moov (starts right after the 20-byte ftyp) and cut 12 bytes into tkhd.
	moovOff := 20
	tkhdOff := moovOff + 8 // skip moov header
	return data[:tkhdOff+12]
}

// CorruptMP4OversizeBox declares a box far larger than the bytes present.
func CorruptMP4OversizeBox() []byte {
	data := BuildMP4([]MP4Input{validMP4Track(1)})
	// Inflate the moov size so its declared payload runs past EOF.
	binary.BigEndian.PutUint32(data[20:24], binary.BigEndian.Uint32(data[20:24])+1_000_000)
	return data
}

// CorruptMP4BadMatrix gives a track an unsupported (skew) display matrix.
func CorruptMP4BadMatrix() []byte {
	bad := validMP4Track(1)
	bad.Rotation = -1 // sentinel consumed by matrixFor below
	return BuildMP4([]MP4Input{bad})
}

// CorruptMP4InvalidCrop places clap outside the visual frame.
func CorruptMP4InvalidCrop() []byte {
	bad := validMP4Track(2)
	bad.Crop = Crop{X: 0, Y: 0, Width: 4000, Height: 1080, Source: SourceMP4}
	return BuildMP4([]MP4Input{bad})
}
// ValidMP4Track exposes a known-good track for cross-package fixtures.
func ValidMP4Track(no uint32) MP4Input { return validMP4Track(no) }

func validMP4Track(no uint32) MP4Input {
	return MP4Input{
		TrackNo: no, Kind: "video", ClockNum: 90000,
		Width: 1920, Height: 1080, Rotation: 0,
		Crop:  Crop{Width: 1920, Height: 1080, Source: SourceMP4},
		Color: Color{Primaries: "bt709", Transfer: "bt709", Matrix: "bt709", Source: SourceMP4},
	}
}

func box(typ string, payload []byte) []byte {
	b := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(b[0:4], uint32(8+len(payload)))
	copy(b[4:8], typ)
	copy(b[8:], payload)
	return b
}

func putMatrix(dst []byte, m [9]int32) {
	for i, v := range m {
		binary.BigEndian.PutUint32(dst[i*4:], uint32(v))
	}
}

func matrixFor(rotation int) [9]int32 {
	const one = 1 << 16
	switch rotation {
	case 0:
		return [9]int32{one, 0, 0, 0, one, 0, 0, 0, one}
	case 90:
		return [9]int32{0, one, 0, -one, 0, 0, 0, 0, one}
	case 180:
		return [9]int32{-one, 0, 0, 0, -one, 0, 0, 0, one}
	case 270:
		return [9]int32{0, -one, 0, one, 0, 0, 0, 0, one}
	default:
		// Deterministic skew matrix used only by the bad-matrix fixture.
		return [9]int32{one, 1234, 0, 0, one, 0, 0, 0, one}
	}
}

func nclxIndex(name string) uint16 {
	switch name {
	case "bt709":
		return 1
	case "bt2020", "bt2020nc":
		return 9
	case "smpte2084":
		return 11
	}
	if n, err := strconv.ParseUint(trimPrefix(name, "nclx:"), 10, 16); err == nil {
		return uint16(n)
	}
	return 0
}

func trimPrefix(s, p string) string {
	if len(s) >= len(p) && s[:len(p)] == p {
		return s[len(p):]
	}
	return s
}
