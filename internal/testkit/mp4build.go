package testkit

import (
	"encoding/binary"
	"os"
)

// mp4Spec parameterizes a generated fixture.
type mp4Spec struct {
	path      string
	gapAfter  int  // frame index after which a one-frame gap is inserted
	truncated bool // truncate the final mdat sample at a known offset
}

// generateMP4 hand-assembles ftyp + moov + mdat. moov is built first with
// a placeholder co64 table; once mdat layout is known the table is patched
// in place before the file is written.
func generateMP4(spec mp4Spec) error {
	nal := newNALBuilder()
	deltas := buildDeltas(spec.gapAfter)
	syncs, sizes, samples := buildSamples(nal)
	avc1 := buildAVC1(nal.avcC())

	moov := buildMoov(deltas, syncs, sizes, avc1)
	co64Table := findCo64Table(moov)
	mdat := buildMdat(samples)

	ftyp := &boxBuilder{}
	buildFtyp(ftyp)
	mdatStart := len(ftyp.buf) + 8 + len(moov)
	// First chunk sits at the mdat payload start (header is 8 bytes).
	patchCo64(moov, co64Table, uint64(mdatStart)+8, sizes)

	var out []byte
	out = append(out, ftyp.buf...)
	wrap := boxBuilder{}
	wrap.bytes("moov", moov)
	out = append(out, wrap.buf...)
	if spec.truncated {
		// Emit only half of the last sample while mdat keeps its declared
		// full size, producing a deterministic overrun at a known offset.
		cut := len(mdat) - len(samples[len(samples)-1])/2
		out = append(out, mdat[:cut]...)
	} else {
		out = append(out, mdat...)
	}
	if err := os.MkdirAll(dirOf(spec.path), 0o755); err != nil {
		return err
	}
	tmp := spec.path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, spec.path)
}

func dirOf(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[:i]
		}
	}
	return "."
}

func buildDeltas(gapAfter int) [][2]uint32 {
	if gapAfter <= 0 {
		return [][2]uint32{{frameCount, frameDelta}}
	}
	// Normal deltas up to gapAfter, then a single doubled delta whose extra
	// length opens a one-frame PTS hole. Every later step stays nominal so
	// the fixture contains exactly one anomaly interval.
	return [][2]uint32{
		{uint32(gapAfter), frameDelta},
		{1, frameDelta * 2},
		{uint32(frameCount - gapAfter - 1), frameDelta},
	}
}

func buildSamples(nal *nalBuilder) (syncs, sizes []uint32, samples [][]byte) {
	syncs = make([]uint32, 0, frameCount/keyframeEvery)
	sizes = make([]uint32, frameCount)
	samples = make([][]byte, frameCount)
	for i := 0; i < frameCount; i++ {
		isIDR := i%keyframeEvery == 0
		if isIDR {
			syncs = append(syncs, uint32(i+1))
		}
		samples[i] = nal.sample(isIDR, uint32(i))
		sizes[i] = uint32(len(samples[i]))
	}
	return syncs, sizes, samples
}

func buildFtyp(b *boxBuilder) {
	payload := []byte{'i', 's', 'o', 'm'}
	payload = append(payload, u32(0x200)...)
	payload = append(payload, []byte("isom")...)
	payload = append(payload, []byte("avc1")...)
	b.bytes("ftyp", payload)
}

// buildMoov returns the moov payload (children only).
func buildMoov(deltas [][2]uint32, syncs, sizes []uint32, avc1 []byte) []byte {
	root := &boxBuilder{}
	duration := uint32(frameCount * frameDelta)
	// mvhd (version 0)
	mvhd := make([]byte, 0, 100)
	mvhd = append(mvhd, make([]byte, 8)...)      // creation/modification
	mvhd = append(mvhd, u32(FixtureTimescale)...) // timescale
	mvhd = append(mvhd, u32(duration)...)         // duration
	mvhd = append(mvhd, u32(0x00010000)...)       // rate
	mvhd = append(mvhd, 0x01, 0x00)               // volume
	mvhd = append(mvhd, make([]byte, 10)...)      // reserved
	mvhd = append(mvhd, matrixPayload()...)
	mvhd = append(mvhd, make([]byte, 24)...) // pre_defined
	mvhd = append(mvhd, u32(2)...)           // next track id
	root.fullBox("mvhd", 0, 0, mvhd)

	trak := &boxBuilder{}
	tkhd := make([]byte, 0, 92)
	tkhd = append(tkhd, make([]byte, 8)...)
	tkhd = append(tkhd, u32(1)...)             // track id
	tkhd = append(tkhd, u32(0)...)             // reserved
	tkhd = append(tkhd, u32(duration)...)      // duration
	tkhd = append(tkhd, make([]byte, 8)...)    // reserved
	tkhd = append(tkhd, 0, 0, 0, 0)            // layer/alternate
	tkhd = append(tkhd, 0, 0)                  // volume
	tkhd = append(tkhd, 0, 0)                  // reserved
	tkhd = append(tkhd, matrixPayload()...)
	tkhd = append(tkhd, u32(uint32(width)<<16)...)
	tkhd = append(tkhd, u32(uint32(height)<<16)...)
	trak.fullBox("tkhd", 0, 3, tkhd)

	mdia := &boxBuilder{}
	mdhd := append(append([]byte{}, make([]byte, 8)...), u32(FixtureTimescale)...)
	mdhd = append(mdhd, u32(duration)...)
	mdhd = append(mdhd, 0x55, 0xC4) // ISO language 'und' packed into 16 bits
	mdhd = append(mdhd, 0, 0)       // pre_defined quality
	mdia.fullBox("mdhd", 0, 0, mdhd)
	hdlr := make([]byte, 0, 32)
	hdlr = append(hdlr, 0, 0, 0, 0) // pre_defined
	hdlr = append(hdlr, []byte("vide")...)
	hdlr = append(hdlr, make([]byte, 12)...) // reserved
	hdlr = append(hdlr, []byte("video")...)
	hdlr = append(hdlr, 0) // null terminator
	mdia.fullBox("hdlr", 0, 0, hdlr)
	minf := &boxBuilder{}
	minf.fullBox("vmhd", 0, 1, make([]byte, 8)) // graphicsmode + opcolor
	dref := &boxBuilder{}
	dref.fullBox("url ", 0, 1, nil)
	dinf := &boxBuilder{}
	drefBox := &boxBuilder{}
	drefBox.fullBox("dref", 0, 0, append(u32(1), dref.buf...))
	dinf.bytes("dinf", drefBox.buf)
	minf.bytes("dinf", dinf.buf)
	minf.buf = append(minf.buf, buildStbl(deltas, syncs, sizes, avc1)...)
	mdia.bytes("minf", minf.buf)
	trak.bytes("mdia", mdia.buf)
	root.bytes("trak", trak.buf)
	return root.buf
}

func buildStbl(deltas [][2]uint32, syncs, sizes []uint32, avc1 []byte) []byte {
	stbl := &boxBuilder{}
	// stsd is one full box: version/flags + entry_count + raw avc1 entry.
	stbl.fullBox("stsd", 0, 0, append(u32(1), avc1...))
	sttsPayload := u32(uint32(len(deltas)))
	for _, d := range deltas {
		sttsPayload = append(sttsPayload, u32(d[0])...)
		sttsPayload = append(sttsPayload, u32(d[1])...)
	}
	stbl.fullBox("stts", 0, 0, sttsPayload)
	stss := u32(uint32(len(syncs)))
	for _, s := range syncs {
		stss = append(stss, u32(s)...)
	}
	stbl.fullBox("stss", 0, 0, stss)
	stsc := append(u32(1), u32(1)...)
	stsc = append(stsc, u32(1)...)
	stsc = append(stsc, u32(1)...)
	stbl.fullBox("stsc", 0, 0, stsc)
	stsz := append(u32(0), u32(uint32(len(sizes)))...)
	for _, s := range sizes {
		stsz = append(stsz, u32(s)...)
	}
	stbl.fullBox("stsz", 0, 0, stsz)
	co64 := u32(uint32(len(sizes)))
	co64 = append(co64, make([]byte, 8*len(sizes))...) // placeholders
	stbl.fullBox("co64", 0, 0, co64)
	return stbl.buf
}

// buildMdat concatenates every sample payload under one mdat header.
func buildMdat(samples [][]byte) []byte {
	mdat := &boxBuilder{}
	var payload []byte
	for _, s := range samples {
		payload = append(payload, s...)
	}
	mdat.bytes("mdat", payload)
	return mdat.buf
}

// findCo64Table returns the offset of the first co64 chunk entry inside
// the moov payload: 4-byte type tag + 8-byte box header remainder is not
// needed; the table follows type(4)+size already passed, so we locate the
// type tag and add 4 (type) + 4 (version/flags) + 4 (entry count).
func findCo64Table(moov []byte) int {
	idx := indexOf(moov, []byte("co64"))
	if idx < 0 {
		return -1
	}
	return idx + 4 + 4 + 4
}

func patchCo64(moov []byte, tablePos int, base uint64, sizes []uint32) {
	off := base
	for i, sz := range sizes {
		binary.BigEndian.PutUint64(moov[tablePos+i*8:], off)
		off += uint64(sz)
	}
}

func indexOf(haystack, needle []byte) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if string(haystack[i:i+len(needle)]) == string(needle) {
			return i
		}
	}
	return -1
}

// buildAVC1 constructs the raw avc1 sample entry including its nested
// avcC box by hand, avoiding any codec-specific marshalling.
func buildAVC1(avcC []byte) []byte {
	const entryBody = 78
	avc1Size := 8 + entryBody + 8 + len(avcC)
	buf := make([]byte, avc1Size)
	binary.BigEndian.PutUint32(buf[0:4], uint32(avc1Size))
	copy(buf[4:8], "avc1")
	binary.BigEndian.PutUint16(buf[14:16], 1) // data_reference_index
	binary.BigEndian.PutUint32(buf[24:28], 0x00480000)
	binary.BigEndian.PutUint32(buf[28:32], 0x00480000)
	binary.BigEndian.PutUint16(buf[38:40], 1)
	binary.BigEndian.PutUint16(buf[40:42], uint16(width))
	binary.BigEndian.PutUint16(buf[42:44], uint16(height))
	binary.BigEndian.PutUint16(buf[70:72], 0x0018)
	binary.BigEndian.PutUint16(buf[72:74], 0xFFFF)
	off := 8 + entryBody
	binary.BigEndian.PutUint32(buf[off:off+4], uint32(8+len(avcC)))
	copy(buf[off+4:off+8], "avcC")
	copy(buf[off+8:], avcC)
	return buf
}
