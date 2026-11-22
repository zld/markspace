package fixture

import (
	"encoding/binary"
)

// boxWriter builds ISO BMFF boxes as plain big-endian bytes. The output is
// valid fragmented MP4 that go-mp4 can walk (ftyp/moov init segment and
// moof/mdat media segments); payloads are synthetic, never real media.
type boxWriter struct {
	buf []byte
}

func (w *boxWriter) bytes() []byte { return w.buf }

func (w *boxWriter) box(typ string, payload []byte) {
	hdr := make([]byte, 8)
	binary.BigEndian.PutUint32(hdr, uint32(8+len(payload)))
	copy(hdr[4:], typ)
	w.buf = append(w.buf, hdr...)
	w.buf = append(w.buf, payload...)
}

// fullBox prefixes version(1)+flags(3).
func fullBox(version byte, flags uint32, payload []byte) []byte {
	out := make([]byte, 4, 4+len(payload))
	out[0] = version
	out[1] = byte(flags >> 16)
	out[2] = byte(flags >> 8)
	out[3] = byte(flags)
	return append(out, payload...)
}

func u32(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}

func u64(v uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	return b
}

func u16(v uint16) []byte {
	b := make([]byte, 2)
	binary.BigEndian.PutUint16(b, v)
	return b
}

// InitSegment builds an fMP4 initialization segment containing ftyp and a
// moov with mvhd/trak/mdia/mdhd (carrying the track timescale).
func InitSegment(timescale uint32, trackID uint32) []byte {
	var w boxWriter
	ftypPayload := append([]byte("iso5"), u32(0x200)...)
	ftypPayload = append(ftypPayload, []byte("iso5dash")...)
	w.box("ftyp", ftypPayload)

	mvhd := fullBox(0, 0, nil)
	mvhd = append(mvhd, make([]byte, 8)...)       // creation/modification
	mvhd = append(mvhd, u32(timescale)...)        // mvhd timescale
	mvhd = append(mvhd, u32(0)...)                // duration
	mvhd = append(mvhd, make([]byte, 80)...)      // rate/volume/matrix/predefined
	mvhd = append(mvhd, u32(2)...)                // next track id

	mdhd := fullBox(0, 0, nil)
	mdhd = append(mdhd, make([]byte, 8)...)
	mdhd = append(mdhd, u32(timescale)...)
	mdhd = append(mdhd, u32(0)...)
	mdhd = append(mdhd, make([]byte, 4)...) // language + predefined

	hdlr := fullBox(0, 0, nil)
	hdlr = append(hdlr, make([]byte, 8)...)
	hdlr = append(hdlr, []byte("vide")...)
	hdlr = append(hdlr, make([]byte, 12)...)
	hdlr = append(hdlr, append([]byte("VideoHandler"), 0)...)

	var mdia, minf, trak boxWriter
	minf.box("stbl", []byte{}) // empty sample tables: fragmented track
	mdia.box("mdhd", mdhd)
	mdia.box("hdlr", hdlr)
	mdia.box("minf", minf.bytes())

	tkhd := fullBox(0, 0x7, nil) // enabled+inMovie+inPreview
	tkhd = append(tkhd, make([]byte, 8)...)
	tkhd = append(tkhd, u32(trackID)...)
	tkhd = append(tkhd, u32(0)...)
	tkhd = append(tkhd, make([]byte, 8)...)
	tkhd = append(tkhd, make([]byte, 2+2+12+8+36+24)...)
	trak.box("tkhd", tkhd)
	trak.box("mdia", mdia.bytes())

	var moov boxWriter
	moov.box("mvhd", mvhd)
	moov.box("trak", trak.bytes())
	w.box("moov", moov.bytes())
	return w.bytes()
}

// MediaSegment builds one moof+mdat fragmented segment.
//
// baseTS is the first sample's composition timestamp (ticks); samples
// carry per-sample durations so VFR spacing is represented explicitly.
// truncate cuts the final byte count to simulate a truncated backhaul.
func MediaSegment(seq, trackID uint32, baseTS uint32,
	durations []uint32, payloadSize int, truncate bool) []byte {
	var mdatPayload []byte
	var trunEntries []byte
	for _, d := range durations {
		sample := make([]byte, payloadSize)
		for i := range sample {
			sample[i] = byte((int(baseTS) + i) & 0xFF)
		}
		mdatPayload = append(mdatPayload, sample...)
		trunEntries = append(trunEntries, u32(d)...) // sample duration
		trunEntries = append(trunEntries, u32(uint32(payloadSize))...)
	}

	mfhd := fullBox(0, 0, u32(seq))
	tfhd := fullBox(0, 0x020000, u32(trackID)) // default-base-is-moof
	// trun flags: data-offset(0x1) + first-sample-flags(0x4) +
	// sample-duration(0x100) + sample-size(0x200).
	trunFlags := uint32(0x000305)
	trun := fullBox(0, trunFlags, u32(uint32(len(durations))))
	trun = append(trun, u32(0)...) // data offset, patched below
	trun = append(trun, make([]byte, 4)...) // first sample flags
	trun = append(trun, trunEntries...)

	tfdt := fullBox(1, 0, u64(uint64(baseTS)))

	var traf boxWriter
	traf.box("tfhd", tfhd)
	traf.box("tfdt", tfdt)
	traf.box("trun", trun)

	// Assemble the moof container box (its own 8-byte header included).
	var moofInner boxWriter
	moofInner.box("mfhd", mfhd)
	moofInner.box("traf", traf.bytes())
	var moof boxWriter
	moof.box("moof", moofInner.bytes())
	moofBytes := moof.bytes()

	// Patch the trun data offset to point at mdat payload start.
	offset := len(moofBytes) + 8
	patchTrunOffset(moofBytes, uint32(offset))

	var w boxWriter
	w.buf = append(w.buf, moofBytes...)
	w.box("mdat", mdatPayload)
	out := w.bytes()
	if truncate && len(out) > 16 {
		out = out[:len(out)-16] // cut through the final sample
	}
	return out
}

// patchTrunOffset recursively locates the trun box inside moof and writes
// the absolute mdat payload offset into its data-offset field.
func patchTrunOffset(buf []byte, offset uint32) {
	var walk func(start, end int) bool
	walk = func(start, end int) bool {
		for i := start; i+8 <= end; {
			size := int(binary.BigEndian.Uint32(buf[i:]))
			typ := string(buf[i+4 : i+8])
			if size <= 0 || i+size > end {
				return false
			}
			if typ == "trun" {
				// box hdr(8) + fullbox(4) + samplecount(4) + dataoffset(4)
				binary.BigEndian.PutUint32(buf[i+16:], offset)
				return true
			}
			if walk(i+8, i+size) {
				return true
			}
			i += size
		}
		return false
	}
	walk(0, len(buf))
}
