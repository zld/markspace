package fixture

import (
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
)

type MP4Spec struct {
	Path             string
	TrackID          uint32
	Width            uint16
	Height           uint16
	TimeScale        uint32
	SampleDuration   uint32
	Payloads         [][]byte
	SampleFields     map[string]string
	TrackFields      map[string]string
	ContainerFields  map[string]string
	BoxOverrunInTrak bool
	UnsupportedCodec string
}

func WriteMP4(spec MP4Spec) (string, error) {
	if spec.TrackID == 0 {
		spec.TrackID = 1
	}
	if spec.Width == 0 {
		spec.Width = 320
	}
	if spec.Height == 0 {
		spec.Height = 180
	}
	if spec.TimeScale == 0 {
		spec.TimeScale = 12000
	}
	if spec.SampleDuration == 0 {
		spec.SampleDuration = 2000
	}
	codec := spec.UnsupportedCodec
	if codec == "" {
		codec = "avc1"
	}

	var mdatPayload []byte
	var sizes []uint32
	for _, p := range spec.Payloads {
		mdatPayload = append(mdatPayload, p...)
		sizes = append(sizes, uint32(len(p)))
	}
	ftyp := buildFtyp()
	moovPlaceholder := buildMoov(spec, codec, sizes, 0)
	dataOffset := uint32(len(ftyp) + len(moovPlaceholder) + 8)
	moov := buildMoov(spec, codec, sizes, dataOffset)
	mdat := box("mdat", mdatPayload)

	data := append([]byte{}, ftyp...)
	data = append(data, moov...)
	data = append(data, mdat...)
	if err := os.MkdirAll(filepath.Dir(spec.Path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(spec.Path, data, 0o644); err != nil {
		return "", err
	}
	return spec.Path, nil
}

func box(typ string, payload ...[]byte) []byte {
	var p []byte
	for _, b := range payload {
		p = append(p, b...)
	}
	out := make([]byte, 8+len(p))
	binary.BigEndian.PutUint32(out[0:4], uint32(8+len(p)))
	copy(out[4:8], typ)
	copy(out[8:], p)
	return out
}

func fullBox(version byte, flags uint32, payload []byte) []byte {
	head := []byte{version, byte(flags >> 16), byte(flags >> 8), byte(flags)}
	return append(head, payload...)
}

func buildFtyp() []byte {
	p := []byte("isom")
	p = binary.BigEndian.AppendUint32(p, 0x200)
	p = append(p, []byte("isom")...)
	p = append(p, []byte("mp42")...)
	return box("ftyp", p)
}

func buildMoov(spec MP4Spec, codec string, sizes []uint32, chunkOffset uint32) []byte {
	n := uint32(len(spec.Payloads))
	mvhd := box("mvhd", fullBox(0, 0, buildMVHD(spec.TimeScale)))
	tkhd := box("tkhd", fullBox(0, 3, buildTKHD(spec.TrackID, spec.Width, spec.Height)))
	mdhd := box("mdhd", fullBox(0, 0, buildMDHD(spec.TimeScale, n*spec.SampleDuration)))
	hdlr := box("hdlr", fullBox(0, 0, buildHDLR("vide", "VideoHandler")))

	stsd := box("stsd", fullBox(0, 0, binary.BigEndian.AppendUint32(nil, 1)),
		buildVisualEntry(codec, spec.Width, spec.Height), mustProvenance(spec.SampleFields))
	sttsPayload := binary.BigEndian.AppendUint32(nil, 1)
	sttsPayload = binary.BigEndian.AppendUint32(sttsPayload, n)
	sttsPayload = binary.BigEndian.AppendUint32(sttsPayload, spec.SampleDuration)
	stts := box("stts", fullBox(0, 0, sttsPayload))

	stscPayload := binary.BigEndian.AppendUint32(nil, 1)
	stscPayload = binary.BigEndian.AppendUint32(stscPayload, 1)
	stscPayload = binary.BigEndian.AppendUint32(stscPayload, n)
	stscPayload = binary.BigEndian.AppendUint32(stscPayload, 1)
	stsc := box("stsc", fullBox(0, 0, stscPayload))

	stszPayload := binary.BigEndian.AppendUint32(nil, 0)
	stszPayload = binary.BigEndian.AppendUint32(stszPayload, n)
	for _, sz := range sizes {
		stszPayload = binary.BigEndian.AppendUint32(stszPayload, sz)
	}
	stsz := box("stsz", fullBox(0, 0, stszPayload))

	stcoPayload := binary.BigEndian.AppendUint32(nil, 1)
	stcoPayload = binary.BigEndian.AppendUint32(stcoPayload, chunkOffset)
	stco := box("stco", fullBox(0, 0, stcoPayload))

	stbl := box("stbl", stsd, stts, stsc, stsz, stco)
	minf := box("minf", box("vmhd", fullBox(1, 1, nil)), box("dinf", box("dref",
		fullBox(0, 0, binary.BigEndian.AppendUint32(nil, 1)),
		box("url ", fullBox(0, 1, nil)))), stbl)
	mdia := box("mdia", mdhd, hdlr, minf)
	trakChildren := [][]byte{tkhd, mdia, mustProvenance(spec.TrackFields)}
	if spec.BoxOverrunInTrak {
		// Only an 8-byte header is physically present in trak; its declared
		// length deliberately extends past the enclosing trak box.
		bad := make([]byte, 8)
		binary.BigEndian.PutUint32(bad[0:4], 0x7fffffff)
		copy(bad[4:8], "bad ")
		trakChildren = append(trakChildren, bad)
	}
	trak := box("trak", trakChildren...)
	moov := box("moov", mvhd, mustProvenance(spec.ContainerFields), trak)
	return moov
}

func buildMVHD(timescale uint32) []byte {
	p := make([]byte, 96) // after FullBox: total mvhd payload passed to fullBox
	binary.BigEndian.PutUint32(p[8:12], timescale)
	binary.BigEndian.PutUint32(p[12:16], 0)
	binary.BigEndian.PutUint32(p[16:20], 0x00010000)
	binary.BigEndian.PutUint16(p[20:22], 0x0100)
	matrix := []uint32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000}
	for i, v := range matrix {
		binary.BigEndian.PutUint32(p[32+i*4:36+i*4], v)
	}
	binary.BigEndian.PutUint32(p[92:96], 2)
	return p
}

func buildTKHD(id uint32, w, h uint16) []byte {
	p := make([]byte, 80) // appended after 4-byte FullBox => 84 byte payload
	binary.BigEndian.PutUint32(p[8:12], id)
	binary.BigEndian.PutUint32(p[12:16], 0) // reserved
	binary.BigEndian.PutUint32(p[16:20], 0) // duration patched unused
	matrix := []uint32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000}
	// FullBox already consumed 4 bytes; this local layout begins after it.
	for i, v := range matrix {
		binary.BigEndian.PutUint32(p[36+i*4:40+i*4], v)
	}
	binary.BigEndian.PutUint32(p[72:76], uint32(w)<<16)
	binary.BigEndian.PutUint32(p[76:80], uint32(h)<<16)
	return p
}

func buildMDHD(timescale, duration uint32) []byte {
	p := make([]byte, 20) // after FullBox => 24
	binary.BigEndian.PutUint32(p[8:12], timescale)
	binary.BigEndian.PutUint32(p[12:16], duration)
	binary.BigEndian.PutUint16(p[16:18], 0x55c4) // undetermined-ish language
	return p
}

func buildHDLR(handler, name string) []byte {
	p := make([]byte, 20) // predef4 + type4 + reserved12 after FullBox
	copy(p[4:8], handler)
	return append(p, append([]byte(name), 0)...)
}

func buildVisualEntry(codec string, w, h uint16) []byte {
	p := make([]byte, 78)
	binary.BigEndian.PutUint16(p[6:8], 1) // data reference index
	binary.BigEndian.PutUint16(p[24:26], w)
	binary.BigEndian.PutUint16(p[26:28], h)
	binary.BigEndian.PutUint32(p[28:32], 0x00480000)
	binary.BigEndian.PutUint32(p[32:36], 0x00480000)
	binary.BigEndian.PutUint16(p[40:42], 1) // frame count
	p[42] = 32                              // compressor name length Pascal string max
	binary.BigEndian.PutUint16(p[74:76], 0x0018)
	binary.BigEndian.PutUint16(p[76:78], 0xffff)
	return box(codec, p)
}

func mustProvenance(fields map[string]string) []byte {
	if len(fields) == 0 {
		return nil
	}
	data, err := json.Marshal(fields)
	if err != nil {
		panic(err)
	}
	return box("mprv", data)
}
