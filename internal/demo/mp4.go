package demo

import (
	"encoding/binary"
)

// box builds a minimal MP4 box (4-byte size + type + payload).
func box(typ string, payload []byte) []byte {
	b := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(b[0:4], uint32(8+len(payload)))
	copy(b[4:8], typ)
	copy(b[8:], payload)
	return b
}

// fullBox prefixes a version/flags full-box header.
func fullBox(typ string, version uint8, payload []byte) []byte {
	return box(typ, append([]byte{version, 0, 0, 0}, payload...))
}

// mp4Segment builds a small, structurally valid fMP4-style file with one
// video track. sampleCount samples of constant size occupy an mdat that
// is exactly sized, so stco/stsc/stsz stay mutually consistent.
func mp4Segment(trackID uint32, timescale uint32, sampleCount, sampleSize uint32, keyframeEvery uint32) []byte {
	ftyp := box("ftyp", []byte("isom\x00\x00\x00\x00isommp41"))

	// tkhd v0: 80-byte payload, track id placed at offset 8.
	tkhdPayload := make([]byte, 80)
	binary.BigEndian.PutUint32(tkhdPayload[8:12], trackID)
	tkhd := fullBox("tkhd", 0, tkhdPayload)

	// mdhd v0: 20-byte payload, timescale at 8, duration at 12.
	mdhdPayload := make([]byte, 20)
	binary.BigEndian.PutUint32(mdhdPayload[8:12], timescale)
	binary.BigEndian.PutUint32(mdhdPayload[12:16], sampleCount*100)
	mdhd := fullBox("mdhd", 0, mdhdPayload)

	// hdlr: pre_defined(4)+handler(4)+reserved(12)+empty name(1).
	hdlrPayload := make([]byte, 21)
	copy(hdlrPayload[4:8], "vide")
	hdlr := fullBox("hdlr", 0, hdlrPayload)

	// stts: one run of sampleCount samples, delta 100 ticks each.
	sttsPayload := make([]byte, 12)
	binary.BigEndian.PutUint32(sttsPayload[0:4], 1)
	binary.BigEndian.PutUint32(sttsPayload[4:8], sampleCount)
	binary.BigEndian.PutUint32(sttsPayload[8:12], 100)
	stts := fullBox("stts", 0, sttsPayload)

	// stsc: all samples in chunk 1.
	stscPayload := make([]byte, 16)
	binary.BigEndian.PutUint32(stscPayload[0:4], 1)
	binary.BigEndian.PutUint32(stscPayload[4:8], 1)
	binary.BigEndian.PutUint32(stscPayload[8:12], sampleCount)
	stsc := fullBox("stsc", 0, stscPayload)

	// stsz: constant sample size.
	stszPayload := make([]byte, 12)
	binary.BigEndian.PutUint32(stszPayload[0:4], sampleSize)
	binary.BigEndian.PutUint32(stszPayload[4:8], sampleCount)
	stsz := fullBox("stsz", 0, stszPayload)

	// stss: keyframes at every keyframeEvery sample.
	var keySamples []uint32
	for s := uint32(1); s <= sampleCount; s++ {
		if keyframeEvery == 0 || (s-1)%keyframeEvery == 0 {
			keySamples = append(keySamples, s)
		}
	}
	stssPayload := make([]byte, 4+4*len(keySamples))
	binary.BigEndian.PutUint32(stssPayload[0:4], uint32(len(keySamples)))
	for i, n := range keySamples {
		binary.BigEndian.PutUint32(stssPayload[4+4*i:8+4*i], n)
	}
	stss := fullBox("stss", 0, stssPayload)

	// stco: single chunk offset patched after assembly.
	stcoPayload := make([]byte, 8)
	binary.BigEndian.PutUint32(stcoPayload[0:4], 1)
	stco := fullBox("stco", 0, stcoPayload)

	stsd := fullBox("stsd", 0, make([]byte, 4)) // entry_count=0
	stbl := box("stbl", joinBytes(stsd, stts, stsc, stsz, stss, stco))
	minf := box("minf", stbl)
	mdia := box("mdia", joinBytes(mdhd, hdlr, minf))
	trak := box("trak", joinBytes(tkhd, mdia))
	moov := box("moov", trak)

	mediaLen := int64(sampleCount) * int64(sampleSize)
	mdat := box("mdat", make([]byte, mediaLen))
	raw := joinBytes(ftyp, moov, mdat)

	// Patch the stco entry. The "stco" type tag is followed by fullbox(4)
	// + count(4) + entry(4).
	stcoType := indexTag(raw, "stco")
	mdatType := indexTag(raw, "mdat")
	payloadOff := uint32(mdatType + 4)
	binary.BigEndian.PutUint32(raw[stcoType+12:stcoType+16], payloadOff)
	return raw
}

// joinBytes concatenates byte slices without a bytes.Buffer.
func joinBytes(parts ...[]byte) []byte {
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

// indexTag returns the first aligned occurrence of a 4-byte box type tag.
func indexTag(raw []byte, typ string) int {
	for i := 4; i+4 <= len(raw); i++ {
		if string(raw[i:i+4]) == typ {
			return i
		}
	}
	return -1
}
