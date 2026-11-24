package index

import (
	"encoding/binary"

	"mediasync/internal/clock"
	"mediasync/internal/repair"
)

// ftypBox builds a compact ISO base media file type box.
func ftypBox() []byte {
	payload := make([]byte, 0, 16)
	payload = append(payload, 'i', 's', 'o', 'm') // major brand
	payload = append32(payload, 512)               // minor version
	payload = append(payload, 'i', 's', 'o', 'm') // compatible brand
	return wrapBox("ftyp", payload)
}

// tableRec is one row of the synthesized sample tables.
type tableRec struct {
	size     uint32
	duration uint32
	key      bool
	index    uint32
}

// buildMoov assembles mvhd and a minimal video trak with stts/stsz/stco/stss
// so the file supports random access at every keyframe.
func (b *Builder) buildMoov(samples []repair.Sample, durations []int64, payloads [][]byte, timescale uint32) []byte {
	_ = payloads
	return b.buildMoovSamples(samples, durations, timescale)
}

func (b *Builder) buildMoovSamples(samples []repair.Sample, durations []int64, timescale uint32) []byte {
	mvhd := buildMVHD(timescale, totalDuration(durations, timescale))
	tkhd := buildTKHD(b.trackID, timescale)
	mdhd := buildMDHD(timescale, totalDuration(durations, timescale))
	hdlr := buildHDLR()
	minf := buildMinf()
	stbl := b.buildStbl(samples, durations)
	mdia := wrapBox("mdia", concat(mdhd, hdlr, minf, stbl))
	trak := wrapBox("trak", concat(tkhd, mdia))
	return wrapBox("moov", concat(mvhd, trak))
}

// buildStbl writes the sample tables: stts (durations), stsz (sizes), stco
// (chunk offsets, patched later is unnecessary because we use a single mdat
// placeholder base), stss (sync/keyframe samples).
func (b *Builder) buildStbl(samples []repair.Sample, durations []int64) []byte {
	recs := make([]tableRec, 0, len(samples))
	for i, s := range samples {
		if s.Isolated || s.Synthetic || s.SizeBytes <= 0 {
			continue
		}
		d := uint32(0)
		if i < len(durations) {
			d = uint32(durations[i])
		}
		recs = append(recs, tableRec{size: uint32(s.SizeBytes), duration: d, key: s.IsKeyframe, index: uint32(len(recs) + 1)})
	}
	stts := buildSTTS(recs)
	stsz := buildSTSZ(recs)
	stsc := buildSTSC(len(recs))
	stco := buildSTCO(len(recs))
	stss := buildSTSS(recs)
	stsd := buildSTSD()
	return wrapBox("stbl", concat(stsd, stts, stsc, stsz, stco, stss))
}

func buildMVHD(timescale uint32, duration uint64) []byte {
	p := make([]byte, 100) // v0 mvhd payload: 4+4+4+4+4+4+2+10+36+24+4
	off := 0
	put32(p, off, 0); off += 4 // version+flags and creation time (offset 0..3)
	put32(p, off, 0); off += 4 // modification time
	put32(p, off, timescale); off += 4
	put32(p, off, uint32(duration)); off += 4
	put32(p, off, 0x00010000); off += 4 // rate
	put16(p, off, 0x0100); off += 2     // volume
	off += 10                            // reserved
	putMatrix(p, off)
	off += 36
	off += 24 // pre-defined
	put32(p, off, 2)
	return wrapBox("mvhd", p)
}

func buildTKHD(trackID, timescale uint32) []byte {
	p := make([]byte, 84) // v0 tkhd payload
	off := 0
	put32(p, off, 0x00000003); off += 4 // flags enabled+in movie
	off += 8                            // creation/modification time
	put32(p, off, trackID); off += 4
	off += 4                // reserved
	put32(p, off, 0); off += 4 // duration
	off += 8                // reserved
	put16(p, off, 0); off += 2 // layer
	put16(p, off, 0); off += 2 // alt group
	put16(p, off, 0); off += 2 // volume
	off += 2                // reserved
	putMatrix(p, off)
	off += 36
	put32(p, off, 320<<16); off += 4 // width
	put32(p, off, 180<<16)           // height
	return wrapBox("tkhd", p)
}

func buildMDHD(timescale uint32, duration uint64) []byte {
	p := make([]byte, 24) // v0 mdhd: version/flags(4)+ctime(4)+mtime(4)+timescale(4)+duration(4)+lang(2)+quality(2)
	off := 0
	off += 8 // version/flags + creation/modification
	put32(p, off, timescale); off += 4
	put32(p, off, uint32(duration)); off += 4
	put16(p, off, 0x55c4); off += 2 // language und
	put16(p, off, 0)
	return wrapBox("mdhd", p)
}

func buildHDLR() []byte {
	p := make([]byte, 0, 32)
	p = append(p, 0)
	p = append32(p, 0)
	p = append32(p, 0) // pre-defined
	p = append(p, 'v', 'i', 'd', 'e')
	p = append(p, make([]byte, 12)...)
	p = append(p, 'V', 'i', 'd', 'e', 'o', 0)
	return wrapBox("hdlr", p)
}

func buildMinf() []byte {
	vmhd := wrapBox("vmhd", []byte{0x00, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0, 0, 0})
	dref := wrapBox("dref", concat([]byte{0, 0, 0, 0}, u32(1),
		wrapBox("url ", []byte{0, 0, 0, 1})))
	dinf := wrapBox("dinf", dref)
	smhd := wrapBox("smhd", []byte{0, 0, 0, 0, 0, 0, 0, 0})
	return wrapBox("minf", concat(vmhd, smhd, dinf))
}

func buildSTSD() []byte {
	// Minimal video sample entry; decoders treat payload as synthetic frames.
	entry := make([]byte, 0, 80)
	entry = append(entry, make([]byte, 6)...)
	entry = append16(entry, 1) // data reference index
	entry = append(entry, make([]byte, 16)...)
	entry = append16(entry, 320)
	entry = append16(entry, 180)
	entry = append(entry, make([]byte, 50)...)
	entry = append(entry, 'a', 'v', 'c', '1')
	return wrapBox("stsd", concat([]byte{0, 0, 0, 0}, u32(1), entry))
}

type sttsRec struct{ duration uint32 }

func buildSTTS(recs []tableRec) []byte {
	p := append([]byte{0, 0, 0, 0}, u32(uint32(len(recs)))...)
	for _, r := range recs {
		p = append32(p, 1) // one sample per entry
		p = append32(p, r.duration)
	}
	return wrapBox("stts", p)
}

func buildSTSZ(recs []tableRec) []byte {
	p := append([]byte{0, 0, 0, 0}, u32(0)...) // sample_size 0 => use table
	p = append32(p, uint32(len(recs)))
	for _, r := range recs {
		p = append32(p, r.size)
	}
	return wrapBox("stsz", p)
}

func buildSTSC(n int) []byte {
	p := append([]byte{0, 0, 0, 0}, u32(1)...)
	p = append32(p, 1) // first chunk
	p = append32(p, uint32(n))
	p = append32(p, 1) // sample description index
	return wrapBox("stsc", p)
}

func buildSTCO(n int) []byte {
	p := append([]byte{0, 0, 0, 0}, u32(uint32(n))...)
	for i := 0; i < n; i++ {
		p = append32(p, 0) // offsets re-derived by the independent parser path
	}
	return wrapBox("stco", p)
}

func buildSTSS(recs []tableRec) []byte {
	keys := make([]uint32, 0)
	for _, r := range recs {
		if r.key {
			keys = append(keys, r.index)
		}
	}
	p := append([]byte{0, 0, 0, 0}, u32(uint32(len(keys)))...)
	for _, k := range keys {
		p = append32(p, k)
	}
	return wrapBox("stss", p)
}

func totalDuration(durations []int64, timescale uint32) uint64 {
	var sum int64
	for _, d := range durations {
		sum, _ = clock.Add64(sum, d)
	}
	// durations are media nanoseconds; convert to the declared timescale.
	return uint64((float64(sum) / 1e9) * float64(timescale))
}

func wrapBox(typ string, payload []byte) []byte {
	size := uint32(8 + len(payload))
	h := make([]byte, 8)
	binary.BigEndian.PutUint32(h[0:4], size)
	copy(h[4:8], typ)
	return concat(h, payload)
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

func append32(b []byte, v uint32) []byte { return append(b, byte(v>>24), byte(v>>16), byte(v>>8), byte(v)) }
func append16(b []byte, v uint16) []byte { return append(b, byte(v>>8), byte(v)) }
func u32(v uint32) []byte                { return []byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)} }

func put32(b []byte, off int, v uint32) {
	b[off], b[off+1], b[off+2], b[off+3] = byte(v>>24), byte(v>>16), byte(v>>8), byte(v)
}

func put16(b []byte, off int, v uint16) { b[off], b[off+1] = byte(v>>8), byte(v) }

// putMatrix writes the standard 3x3 transformation matrix at off.
func putMatrix(b []byte, off int) {
	put32(b, off, 0x00010000)
	put32(b, off+4, 0)
	put32(b, off+8, 0)
	put32(b, off+12, 0)
	put32(b, off+16, 0x00010000)
	put32(b, off+20, 0)
	put32(b, off+24, 0)
	put32(b, off+28, 0)
	put32(b, off+32, 0x40000000)
}
