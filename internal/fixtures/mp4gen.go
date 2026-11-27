// Package fixtures generates every input used by the review pipeline and its
// tests. Nothing here reads external media: MP4, captions and RTP captures are
// assembled byte by byte from deterministic descriptions.
package fixtures

import (
	"encoding/binary"
)

// MP4 describes a minimal single-track MP4 to synthesize.
type MP4 struct {
	Timescale   uint32
	SampleCount int
	SampleDelta uint32
	SampleSize  uint32
	// OversizeSample, when >= 0, inflates one sample's size so its byte
	// range runs past the end of the file.
	OversizeSample int
	// ZeroDeltaAfter makes every sample from that index onward carry a zero
	// duration, so DTS stops increasing.
	ZeroDeltaAfter int
}

// DefaultMP4 is a healthy track of one sample per second at a 12800 timescale,
// so its samples land on the review event instants.
func DefaultMP4() MP4 {
	return MP4{Timescale: 12800, SampleCount: 5, SampleDelta: 12800, SampleSize: 16, OversizeSample: -1}
}

// box encodes one ISO-BMFF box.
func box(typ string, payload ...[]byte) []byte {
	n := 8
	for _, p := range payload {
		n += len(p)
	}
	out := make([]byte, 8, n)
	binary.BigEndian.PutUint32(out[0:4], uint32(n))
	copy(out[4:8], typ)
	for _, p := range payload {
		out = append(out, p...)
	}
	return out
}

// fullBox encodes a version/flags box.
func fullBox(typ string, version byte, flags uint32, payload ...[]byte) []byte {
	head := make([]byte, 4)
	head[0] = version
	head[1] = byte(flags >> 16)
	head[2] = byte(flags >> 8)
	head[3] = byte(flags)
	return box(typ, append([][]byte{head}, payload...)...)
}

func u32(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}

func u16(v uint16) []byte {
	b := make([]byte, 2)
	binary.BigEndian.PutUint16(b, v)
	return b
}

func zeros(n int) []byte { return make([]byte, n) }

var identityMatrix = func() []byte {
	vals := []uint32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000}
	var b []byte
	for _, v := range vals {
		b = append(b, u32(v)...)
	}
	return b
}()

// Bytes builds the MP4.
func (m MP4) Bytes() []byte {
	if m.Timescale == 0 {
		m.Timescale = 12800
	}
	if m.SampleCount <= 0 {
		m.SampleCount = 1
	}
	if m.SampleDelta == 0 {
		m.SampleDelta = 512
	}
	if m.SampleSize == 0 {
		m.SampleSize = 16
	}

	ftyp := box("ftyp", []byte("isom"), u32(0x200), []byte("isomiso2mp41"))

	mvhd := fullBox("mvhd", 0, 0,
		u32(0), u32(0), u32(1000), u32(uint32(m.SampleCount)*1000/25),
		u32(0x00010000), u16(0x0100), u16(0), zeros(8),
		identityMatrix, zeros(24), u32(2))

	// stts entries: run-length encoded sample deltas.
	type sttsEntry struct{ count, delta uint32 }
	var entries []sttsEntry
	switch {
	case m.ZeroDeltaAfter > 0 && m.ZeroDeltaAfter < m.SampleCount:
		entries = append(entries, sttsEntry{uint32(m.ZeroDeltaAfter), m.SampleDelta})
		entries = append(entries, sttsEntry{uint32(m.SampleCount - m.ZeroDeltaAfter), 0})
	default:
		entries = append(entries, sttsEntry{uint32(m.SampleCount), m.SampleDelta})
	}
	sttsPayload := u32(uint32(len(entries)))
	for _, e := range entries {
		sttsPayload = append(sttsPayload, u32(e.count)...)
		sttsPayload = append(sttsPayload, u32(e.delta)...)
	}
	stts := fullBox("stts", 0, 0, sttsPayload)

	// stsc: a single chunk holding every sample.
	stsc := fullBox("stsc", 0, 0, u32(1), u32(1), u32(uint32(m.SampleCount)), u32(1))

	// stsz with explicit per-sample sizes.
	var stszPayload []byte
	stszPayload = append(stszPayload, u32(0)...)
	stszPayload = append(stszPayload, u32(uint32(m.SampleCount))...)
	for i := 0; i < m.SampleCount; i++ {
		size := m.SampleSize
		if m.OversizeSample >= 0 && i == m.OversizeSample {
			size = 0xffffff
		}
		stszPayload = append(stszPayload, u32(size)...)
	}
	stsz := fullBox("stsz", 0, 0, stszPayload)

	mdia := func(mdatPayloadOffset uint32) []byte {
		mdhd := fullBox("mdhd", 0, 0,
			u32(0), u32(0), u32(m.Timescale),
			u32(uint32(m.SampleCount)*m.SampleDelta),
			u16(0x55c4), u16(0))
		hdlr := fullBox("hdlr", 0, 0, u32(0), []byte("vide"), zeros(12), []byte("VideoHandler\x00"))
		vmhd := fullBox("vmhd", 0, 1, u16(0), u16(0), u16(0), u16(0))
		url := fullBox("url ", 0, 1)
		dref := fullBox("dref", 0, 0, u32(1), url)
		dinf := box("dinf", dref)
		stsd := fullBox("stsd", 0, 0, u32(0))
		stco := fullBox("stco", 0, 0, u32(1), u32(mdatPayloadOffset))
		stbl := box("stbl", stsd, stts, stsc, stsz, stco)
		minf := box("minf", vmhd, dinf, stbl)
		return box("mdia", mdhd, hdlr, minf)
	}

	tkhd := func() []byte {
		return fullBox("tkhd", 0, 3,
			u32(0), u32(0), u32(1), u32(0),
			u32(uint32(m.SampleCount)*m.SampleDelta),
			zeros(8), u16(0), u16(0), u16(0), u16(0),
			identityMatrix, u32(640<<16), u32(360<<16))
	}

	// Payload is sized over the samples that are expected to be present in the
	// file; an intentionally oversize sample has no bytes, so its range runs
	// past the end and the indexer must reject it. Samples after it also fall
	// outside because the declared range swallows the remaining payload.
	totalPayload := int(m.SampleSize) * m.SampleCount
	if m.OversizeSample >= 0 && m.OversizeSample < m.SampleCount {
		totalPayload = int(m.SampleSize)*m.OversizeSample + 1
	}
	if totalPayload < 1 {
		totalPayload = 1
	}
	if totalPayload <= 0 {
		totalPayload = 1
	}

	// Two-pass: compute the moov size to know where mdat payload starts.
	buildMoov := func(mdatOff uint32) []byte {
		trak := box("trak", tkhd(), mdia(mdatOff))
		return box("moov", mvhd, trak)
	}
	moovLen := len(buildMoov(0))
	mdatPayload := len(ftyp) + moovLen + 8
	moov := buildMoov(uint32(mdatPayload))

	mdat := box("mdat", make([]byte, totalPayload))
	return concat(ftyp, moov, mdat)
}

func concat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}
