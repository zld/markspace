package mp4

import (
	"encoding/binary"

	"mediatool/internal/timeline"
)

// BuildBox wraps payload in an MP4 box header with a 32-bit size. Callers
// whose payload would exceed uint32 max should reject the input instead
// of using a 64-bit header, because the pipeline limit is 4 MiB.
func BuildBox(typ string, payload []byte) []byte {
	checkBoxSize(len(payload))
	out := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(out[0:4], uint32(8+len(payload)))
	copy(out[4:8], typ)
	copy(out[8:], payload)
	return out
}

// checkBoxSize panics with a classified error when a box length cannot be
// represented in the 32-bit size field.
func checkBoxSize(payloadLen int) {
	if uint64(payloadLen)+8 > 0xFFFFFFFF {
		panic(timeline.NewError(timeline.ClassOverflow, "box payload exceeds uint32 size"))
	}
}

// BuildFullBox prepends the version/flags header before wrapping.
func BuildFullBox(typ string, ver uint8, flags uint32, payload []byte) []byte {
	body := make([]byte, 4+len(payload))
	body[0] = ver
	body[1] = byte(flags >> 16)
	body[2] = byte(flags >> 8)
	body[3] = byte(flags)
	copy(body[4:], payload)
	return BuildBox(typ, body)
}

func u32be(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}

func u64be(v uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	return b
}

// BuildFtyp returns the CMAF-compatible file-type box used on every
// output stream.
func BuildFtyp() []byte {
	payload := make([]byte, 0, 16)
	payload = append(payload, []byte("iso5")...) // major brand
	payload = append(payload, u32be(1)...)       // minor version
	payload = append(payload, []byte("iso5")...)
	payload = append(payload, []byte("dash")...)
	return BuildBox("ftyp", payload)
}

// BuildMoov synthesizes a minimal but standards-shaped init moov for the
// given tracks: mvhd plus trak(tkhd/mdhd/hdlr/minf/stbl/stsd) and trex.
// Sample payloads never appear here, so no codec bytes are invented.
func BuildMoov(tracks []TrackInfo) []byte {
	var children []byte
	// mvhd: version/flags + creation + modification + timescale + duration.
	mvhdBody := make([]byte, 0, 16)
	mvhdBody = append(mvhdBody, u32be(0)...)
	mvhdBody = append(mvhdBody, u32be(0)...)
	mvhdBody = append(mvhdBody, u32be(1000)...)
	mvhdBody = append(mvhdBody, u32be(0)...)
	children = append(children, BuildFullBox("mvhd", 0, 0, mvhdBody)...)
	for _, tr := range tracks {
		children = append(children, BuildTrak(tr)...)
		children = append(children, BuildTrex(tr)...)
	}
	return BuildBox("moov", children)
}

// BuildTrak writes one track declaration.
func BuildTrak(t TrackInfo) []byte {
	tkhdBody := append(append(u32be(0), u32be(0)...), u32be(t.TrackID)...)
	tkhd := BuildFullBox("tkhd", 0, 3, tkhdBody)
	mdhdBody := append(append(u32be(0), u32be(0)...), u32be(t.Timescale)...)
	mdhd := BuildFullBox("mdhd", 0, 0, mdhdBody)
	handler := "vide"
	if t.MediaType == "audio" {
		handler = "soun"
	}
	var hdlrBody []byte
	hdlrBody = append(hdlrBody, u32be(0)...) // pre_defined
	hdlrBody = append(hdlrBody, []byte(handler)...)
	hdlrBody = append(hdlrBody, 0) // name, NUL terminated
	hdlr := BuildFullBox("hdlr", 0, 0, hdlrBody)
	codec := t.CodecKey
	if codec == "" {
		codec = "undf"
	}
	stsdEntry := BuildBox(codec, []byte("cfg"))
	stsd := BuildFullBox("stsd", 0, 0, append(u32be(1), stsdEntry...))
	stbl := BuildBox("stbl", stsd)
	minf := BuildBox("minf", stbl)
	var mdia []byte
	mdia = append(mdia, mdhd...)
	mdia = append(mdia, hdlr...)
	mdia = append(mdia, minf...)
	var trak []byte
	trak = append(trak, tkhd...)
	trak = append(trak, BuildBox("mdia", mdia)...)
	return BuildBox("trak", trak)
}

// BuildTrex writes the track defaults box required by fMP4.
func BuildTrex(t TrackInfo) []byte {
	payload := make([]byte, 0, 20)
	payload = append(payload, u32be(t.TrackID)...)
	payload = append(payload, u32be(1)...) // default sample description index
	payload = append(payload, u32be(0)...) // default sample duration
	payload = append(payload, u32be(0)...) // default sample size
	payload = append(payload, u32be(0)...) // default sample flags
	return BuildFullBox("trex", 0, 0, payload)
}

// BuildInit returns a complete init segment for the given tracks.
func BuildInit(tracks []TrackInfo) []byte {
	return append(BuildFtyp(), BuildMoov(tracks)...)
}
