package demo

import "encoding/binary"

// BuildMP4Fixture builds a small but probe-readable MP4 container. The same
// elementary stream can be packaged with different free-box padding, producing
// a different container digest without changing stream bytes.
func BuildMP4Fixture(trackID, timescale uint32, stream []byte, freePadding int) []byte {
	ftyp := box("ftyp", []byte("isom"+uint32Bytes(0x200)+"isomiso2mp41"))
	mvhd := make([]byte, 100)
	binary.BigEndian.PutUint32(mvhd[12:16], 1000) // movie timescale
	binary.BigEndian.PutUint32(mvhd[16:20], 400)  // 400 ms

	tkhd := make([]byte, 84)
	binary.BigEndian.PutUint32(tkhd[12:16], trackID)
	binary.BigEndian.PutUint32(tkhd[20:24], 400)

	mdhd := make([]byte, 24)
	binary.BigEndian.PutUint32(mdhd[12:16], timescale)
	binary.BigEndian.PutUint32(mdhd[16:20], 36000)

	stco := make([]byte, 12)
	binary.BigEndian.PutUint32(stco[4:8], 1)  // entry count
	binary.BigEndian.PutUint32(stco[8:12], 0) // chunk offset patched/irrelevant for probe

	stts := make([]byte, 16)
	binary.BigEndian.PutUint32(stts[4:8], 1)   // entry count
	binary.BigEndian.PutUint32(stts[8:12], 12) // sample count
	binary.BigEndian.PutUint32(stts[12:16], 3000)

	stsc := make([]byte, 20)
	binary.BigEndian.PutUint32(stsc[4:8], 1)  // entry count
	binary.BigEndian.PutUint32(stsc[8:12], 1) // first chunk
	binary.BigEndian.PutUint32(stsc[12:16], 12)
	binary.BigEndian.PutUint32(stsc[16:20], 1)

	stbl := box("stbl", append(append(box("stco", stco), box("stts", stts)...), box("stsc", stsc)...))
	minf := box("minf", stbl)
	mdia := box("mdia", append(box("mdhd", mdhd), minf...))
	trak := box("trak", append(box("tkhd", tkhd), mdia...))
	moov := box("moov", append(box("mvhd", mvhd), trak...))

	var out []byte
	out = append(out, ftyp...)
	if freePadding > 0 {
		out = append(out, box("free", make([]byte, freePadding))...)
	}
	out = append(out, moov...)
	out = append(out, box("mdat", stream)...)
	return out
}

// WithMetadataTimescale returns a fixture whose normalized track metadata
// changes while the elementary stream remains byte-identical.
func WithMetadataTimescale(mp4 []byte, trackTimescale uint32) []byte {
	out := append([]byte(nil), mp4...)
	// The generated mdhd timescale is the first 0x00 0x01 0x5f 0x90 (90000)
	// after the unique mdhd box header. Patching it keeps box sizes valid.
	old := make([]byte, 4)
	binary.BigEndian.PutUint32(old, 90000)
	next := make([]byte, 4)
	binary.BigEndian.PutUint32(next, trackTimescale)
	for i := 0; i+4 <= len(out); i++ {
		if out[i] == old[0] && out[i+1] == old[1] && out[i+2] == old[2] && out[i+3] == old[3] {
			copy(out[i:i+4], next)
			return out
		}
	}
	return out
}

func box(typ string, payload []byte) []byte {
	b := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(b[0:4], uint32(len(b)))
	copy(b[4:8], typ)
	copy(b[8:], payload)
	return b
}

func uint32Bytes(v uint32) string {
	b := []byte{0, 0, 0, 0}
	binary.BigEndian.PutUint32(b, v)
	return string(b)
}
