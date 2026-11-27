package sample

import (
	"bytes"
	"encoding/binary"

	mp4 "github.com/abema/go-mp4"
)

// BuildFragmentedMP4 builds ftyp + moov + (moof+mdat) fragments. Each
// fragment holds CRC-framed samples described by a trun box whose
// data_offset is relative to its moof (per ISO BMFF default).
func BuildFragmentedMP4(timescale, frameDur uint32, fragments [][][]byte) []byte {
	// moov declares the track but no samples (fragmented layout).
	moov := buildMoov(timescale, frameDur, nil, 0)
	ftyp := marshalBox(&mp4.Ftyp{
		MajorBrand:   [4]byte{'i', 's', 'o', 'm'},
		MinorVersion: 1,
	})
	out := append(append([]byte{}, ftyp...), moov...)
	for i, frag := range fragments {
		moofStart := len(out)
		moof := buildMoof(uint32(i+1), frag, 0)
		mdatStart := moofStart + len(moof)
		dataOffset := int32(mdatStart + 8 - moofStart) // past mdat header
		moof = buildMoof(uint32(i+1), frag, dataOffset)
		out = append(out, moof...)
		var payload []byte
		for _, s := range frag {
			payload = append(payload, s...)
		}
		out = append(out, box(mp4.BoxTypeMdat(), payload)...)
	}
	return out
}

func buildMoof(seq uint32, samples [][]byte, dataOffset int32) []byte {
	mfhd := &mp4.Mfhd{SequenceNumber: seq}
	tfhd := &mp4.Tfhd{TrackID: 1}
	trun := &mp4.Trun{DataOffset: dataOffset, SampleCount: uint32(len(samples))}
	trun.Flags = [3]byte{0x00, 0x02, 0x01} // data-offset-present | sample-size-present
	for _, s := range samples {
		trun.Entries = append(trun.Entries, mp4.TrunEntry{SampleSize: uint32(len(s))})
	}
	traf := box(mp4.BoxTypeTraf(), append(marshalBox(tfhd), marshalBox(trun)...))
	return box(mp4.BoxTypeMoof(), append(marshalBox(mfhd), traf...))
}

// CorruptSampleStart flips one byte at the start of the sample-th sample
// of fragment frag (its CRC field), returning the modified data and the
// exact injected offset. The fragment checker reports that same offset.
func CorruptSampleStart(data []byte, frag, sampleIdx int) ([]byte, int64) {
	off := sampleStartOffset(data, frag, sampleIdx)
	out := append([]byte{}, data...)
	out[off] ^= 0xFF
	return out, int64(off)
}

// sampleStartOffset locates the absolute offset of a sample within a
// fragmented MP4 built by BuildFragmentedMP4.
func sampleStartOffset(data []byte, frag, sampleIdx int) int64 {
	// Walk top-level boxes, pairing moof/mdat.
	pos := 0
	fragSeen := -1
	for pos+8 <= len(data) {
		size := int(binary.BigEndian.Uint32(data[pos:]))
		typ := string(data[pos+4 : pos+8])
		if typ == "moof" {
			fragSeen++
		}
		if typ == "mdat" && fragSeen == frag {
			// Parse the preceding moof's trun for sample sizes.
			sizes := trunSampleSizes(data, pos)
			off := pos + 8
			for i, sz := range sizes {
				if i == sampleIdx {
					return int64(off)
				}
				off += int(sz)
			}
			panic("sample index out of range")
		}
		pos += size
	}
	panic("fragment not found")
}

// trunSampleSizes reads sample sizes from the moof immediately before
// the mdat at mdatPos.
func trunSampleSizes(data []byte, mdatPos int) []int {
	// The moof is the previous top-level box; find it by scanning back
	// through top-level boxes from the start.
	pos := 0
	var moofPos int
	for pos+8 <= len(data) {
		size := int(binary.BigEndian.Uint32(data[pos:]))
		if pos+size == mdatPos && string(data[pos+4:pos+8]) == "moof" {
			moofPos = pos
		}
		if pos >= mdatPos {
			break
		}
		pos += size
	}
	// Parse trun inside moof: moof > traf > trun; trun layout (version 0,
	// flags 0x201): header 8, ver/flags 4, sample_count 4, data_offset 4,
	// then entries of 4 bytes (size only).
	moof := data[moofPos:mdatPos]
	idx := bytes.Index(moof, []byte("trun"))
	if idx < 0 {
		panic("trun not found")
	}
	p := moof[idx+4:]
	count := int(binary.BigEndian.Uint32(p[4:]))
	sizes := make([]int, count)
	for i := range sizes {
		sizes[i] = int(binary.BigEndian.Uint32(p[12+i*4:]))
	}
	return sizes
}
