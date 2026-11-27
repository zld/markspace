// Package sample generates deterministic, self-contained media fixtures:
// synthetic frames/audio, RTP/RTCP streams and MP4 containers (normal,
// truncated, corrupt-fragment, incomplete-index). No external files or
// network access are involved; everything derives from a seed.
package sample

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"

	mp4 "github.com/abema/go-mp4"
)

// SampleFraming: every sample stored in mdat is framed as
//
//	uint32 big-endian CRC-32 (IEEE) of payload, followed by payload.
//
// The fragment checker verifies these CRCs so a corrupted sample is
// located at its exact raw offset.
const frameHeaderSize = 4

// FrameSample wraps payload in its CRC frame.
func FrameSample(payload []byte) []byte {
	out := make([]byte, frameHeaderSize+len(payload))
	binary.BigEndian.PutUint32(out, crc32.ChecksumIEEE(payload))
	copy(out[frameHeaderSize:], payload)
	return out
}

// marshalBox serializes a leaf box including its size+type header.
// (mp4.Marshal writes only the box fields, not the header.)
func marshalBox(b mp4.IImmutableBox) []byte {
	buf := &bytes.Buffer{}
	if _, err := mp4.Marshal(buf, b, mp4.Context{}); err != nil {
		panic(err)
	}
	return box(b.GetType(), buf.Bytes())
}

// moovSize is stable for a fixed sample count, so stco can be computed
// by building once with a placeholder and measuring.
func buildMoov(timescale, frameDur uint32, sizes []uint32, chunkOffset uint32) []byte {
	n := uint32(len(sizes))
	var buf bytes.Buffer
	put := func(b []byte) { buf.Write(b) }
	put(marshalBox(&mp4.Mvhd{
		Timescale:   1000,
		DurationV0:  uint32(uint64(n) * uint64(frameDur) * 1000 / uint64(timescale)),
		NextTrackID: 2,
	}))
	// trak
	var trak bytes.Buffer
	tkhd := &mp4.Tkhd{TrackID: 1}
	tkhd.Flags = [3]byte{0, 0, 3} // enabled | in movie
	trak.Write(marshalBox(tkhd))
	// mdia
	var mdia bytes.Buffer
	mdia.Write(marshalBox(&mp4.Mdhd{
		Timescale:  timescale,
		DurationV0: uint32(uint64(n) * uint64(frameDur)),
		Language:   [3]byte{'u', 'n', 'd'},
	}))
	// minf/stbl
	var stbl bytes.Buffer
	stbl.Write(marshalBox(&mp4.Stts{
		EntryCount: 1,
		Entries:    []mp4.SttsEntry{{SampleCount: n, SampleDelta: frameDur}},
	}))
	stbl.Write(marshalBox(&mp4.Stsc{
		EntryCount: 1,
		Entries:    []mp4.StscEntry{{FirstChunk: 1, SamplesPerChunk: n, SampleDescriptionIndex: 1}},
	}))
	stsz := &mp4.Stsz{SampleCount: n, EntrySize: sizes}
	stbl.Write(marshalBox(stsz))
	stbl.Write(marshalBox(&mp4.Stco{EntryCount: 1, ChunkOffset: []uint32{chunkOffset}}))
	mdia.Write(box(mp4.BoxTypeMinf(), box(mp4.BoxTypeStbl(), stbl.Bytes())))
	trak.Write(box(mp4.BoxTypeMdia(), mdia.Bytes()))
	put(box(mp4.BoxTypeTrak(), trak.Bytes()))
	return box(mp4.BoxTypeMoov(), buf.Bytes())
}

// box wraps payload in a box header with the given type.
func box(typ mp4.BoxType, payload []byte) []byte {
	out := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(out, uint32(8+len(payload)))
	copy(out[4:], typ[:])
	copy(out[8:], payload)
	return out
}

// BuildNormalMP4 builds a self-contained MP4 holding the given framed
// samples as one chunk. timescale/frameDur describe the track clock.
func BuildNormalMP4(timescale, frameDur uint32, samples [][]byte) []byte {
	sizes := make([]uint32, len(samples))
	var mdatPayload []byte
	for i, s := range samples {
		sizes[i] = uint32(len(s))
		mdatPayload = append(mdatPayload, s...)
	}
	ftyp := marshalBox(&mp4.Ftyp{
		MajorBrand:   [4]byte{'i', 's', 'o', 'm'},
		MinorVersion: 1,
	})
	moov := buildMoov(timescale, frameDur, sizes, 0)
	chunkOffset := uint32(len(ftyp) + len(moov) + 8) // mdat header is 8 bytes
	moov = buildMoov(timescale, frameDur, sizes, chunkOffset)
	out := append(append([]byte{}, ftyp...), moov...)
	out = append(out, box(mp4.BoxTypeMdat(), mdatPayload)...)
	return out
}

// BuildTruncatedMP4 returns a normal MP4 cut at cutAt bytes.
func BuildTruncatedMP4(timescale, frameDur uint32, samples [][]byte, cutAt int) []byte {
	full := BuildNormalMP4(timescale, frameDur, samples)
	if cutAt > len(full) {
		cutAt = len(full)
	}
	return full[:cutAt]
}

// BuildIncompleteIndexMP4 builds an MP4 whose stsz declares one more
// sample than it lists, so the sample index cannot be fully read.
func BuildIncompleteIndexMP4(timescale, frameDur uint32, samples [][]byte) []byte {
	full := BuildNormalMP4(timescale, frameDur, samples)
	// Hand-craft a broken stsz: SampleCount = n+1, entries = n.
	n := uint32(len(samples))
	var stsz bytes.Buffer
	stsz.Write([]byte{0, 0, 0, 0})                    // version+flags
	binary.Write(&stsz, binary.BigEndian, uint32(0))  // SampleSize = 0 (variable)
	binary.Write(&stsz, binary.BigEndian, n+1)        // SampleCount (lies)
	for _, s := range samples {
		binary.Write(&stsz, binary.BigEndian, uint32(len(s)))
	}
	broken := box(mp4.BoxTypeStsz(), stsz.Bytes())
	// Locate the well-formed stsz inside the full file and replace it.
	good := BuildNormalMP4(timescale, frameDur, samples)
	idx := bytes.Index(good, []byte("stsz"))
	if idx < 0 {
		panic("stsz not found")
	}
	goodSize := int(binary.BigEndian.Uint32(good[idx-4:]))
	out := append([]byte{}, full[:idx-4]...)
	out = append(out, broken...)
	out = append(out, full[idx-4+goodSize:]...)
	// Fix ancestor box sizes (stbl, minf, mdia, trak, moov) by the delta.
	delta := len(broken) - goodSize
	if delta != 0 {
		out = fixAncestorSizes(out, delta, idx-4)
	}
	return out
}

// fixAncestorSizes adjusts the size fields of all boxes that contain
// the box ending region at pos, after a delta-byte length change.
func fixAncestorSizes(data []byte, delta, pos int) []byte {
	// Walk top-level boxes; for any box spanning pos, grow its size and
	// recurse into container payloads.
	var walk func(start, end int)
	walk = func(start, end int) {
		off := start
		for off+8 <= end {
			size := int(binary.BigEndian.Uint32(data[off:]))
			if size < 8 || off+size > end {
				return
			}
			if off < pos && pos < off+size {
				binary.BigEndian.PutUint32(data[off:], uint32(size+delta))
				typ := string(data[off+4 : off+8])
				switch typ {
				case "moov", "trak", "mdia", "minf", "stbl":
					walk(off+8, off+size)
				}
			}
			off += size
		}
	}
	walk(0, len(data))
	return data
}
