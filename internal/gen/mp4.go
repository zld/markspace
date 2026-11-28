// Package gen synthesizes deterministic MP4 containers and RTP trace files
// purely from code, so tests and the demo never depend on external assets.
package gen

import (
	"bytes"
	"encoding/binary"

	mp4 "github.com/abema/go-mp4"
)

// TrackSpec describes one synthetic track to embed in a generated MP4.
type TrackSpec struct {
	ID              uint32
	Timescale       uint32
	SampleDelta     uint32 // ticks between consecutive samples
	SamplesPerChunk uint32
	SampleSizes     []uint32
}

// SampleByte returns the deterministic payload byte for sample sampleIdx of
// track trackID at position pos inside the sample.
func SampleByte(trackID uint32, sampleIdx, pos int) byte {
	return byte((int(trackID)*31 + sampleIdx*7 + pos) % 251)
}

func leaf(box mp4.IImmutableBox) []byte {
	var payload bytes.Buffer
	if _, err := mp4.Marshal(&payload, box, mp4.Context{}); err != nil {
		panic(err)
	}
	// mp4.Marshal emits only the payload; prepend the box header.
	out := make([]byte, 0, 8+payload.Len())
	var hdr [8]byte
	binary.BigEndian.PutUint32(hdr[0:4], uint32(8+payload.Len()))
	copy(hdr[4:8], box.GetType().String())
	out = append(out, hdr[:]...)
	return append(out, payload.Bytes()...)
}

func container(typ string, children ...[]byte) []byte {
	size := 8
	for _, c := range children {
		size += len(c)
	}
	out := make([]byte, 0, size)
	var hdr [8]byte
	binary.BigEndian.PutUint32(hdr[0:4], uint32(size))
	copy(hdr[4:8], typ)
	out = append(out, hdr[:]...)
	for _, c := range children {
		out = append(out, c...)
	}
	return out
}

func buildMoov(specs []TrackSpec, chunkOffsets [][]uint32) []byte {
	var traks [][]byte
	for i, ts := range specs {
		var sttsEntries []mp4.SttsEntry
		sttsEntries = append(sttsEntries, mp4.SttsEntry{
			SampleCount: uint32(len(ts.SampleSizes)), SampleDelta: ts.SampleDelta,
		})
		nChunks := (len(ts.SampleSizes) + int(ts.SamplesPerChunk) - 1) / int(ts.SamplesPerChunk)
		stts := leaf(&mp4.Stts{EntryCount: uint32(len(sttsEntries)), Entries: sttsEntries})
		stsc := leaf(&mp4.Stsc{EntryCount: 1, Entries: []mp4.StscEntry{{
			FirstChunk: 1, SamplesPerChunk: ts.SamplesPerChunk, SampleDescriptionIndex: 1,
		}}})
		stsz := leaf(&mp4.Stsz{SampleCount: uint32(len(ts.SampleSizes)), EntrySize: ts.SampleSizes})
		stco := leaf(&mp4.Stco{EntryCount: uint32(nChunks), ChunkOffset: chunkOffsets[i]})
		stbl := container("stbl", stts, stsc, stsz, stco)
		minf := container("minf", stbl)
		mdhd := leaf(&mp4.Mdhd{Timescale: ts.Timescale,
			DurationV0: ts.SampleDelta * uint32(len(ts.SampleSizes))})
		mdia := container("mdia", mdhd, minf)
		tkhd := leaf(&mp4.Tkhd{TrackID: ts.ID,
			DurationV0: ts.SampleDelta * uint32(len(ts.SampleSizes))})
		traks = append(traks, container("trak", tkhd, mdia))
	}
	mvhd := leaf(&mp4.Mvhd{Timescale: 1000, NextTrackID: uint32(len(specs) + 1)})
	return container("moov", append([][]byte{mvhd}, traks...)...)
}

// chunkLayouts computes per-track chunk payload sizes and payloads.
func chunkLayouts(specs []TrackSpec) (sizes [][]uint32, payloads [][]byte) {
	for _, ts := range specs {
		var cs []uint32
		var pl []byte
		for i, sz := range ts.SampleSizes {
			for p := 0; p < int(sz); p++ {
				pl = append(pl, SampleByte(ts.ID, i, p))
			}
			cs = append(cs, sz)
		}
		// Group sample sizes into chunks.
		var chunkSizes []uint32
		var acc uint32
		for i, sz := range cs {
			acc += sz
			if (i+1)%int(ts.SamplesPerChunk) == 0 || i == len(cs)-1 {
				chunkSizes = append(chunkSizes, acc)
				acc = 0
			}
		}
		sizes = append(sizes, chunkSizes)
		payloads = append(payloads, pl)
	}
	return sizes, payloads
}

// BuildMP4 renders a deterministic MP4 container for the given track specs.
// Chunk offsets in stco point at the real sample payloads inside mdat.
func BuildMP4(specs []TrackSpec) []byte {
	ftyp := leaf(&mp4.Ftyp{MajorBrand: [4]byte{'i', 's', 'o', 'm'}})
	chunkSizes, payloads := chunkLayouts(specs)

	zero := make([][]uint32, len(specs))
	for i, cs := range chunkSizes {
		zero[i] = make([]uint32, len(cs))
	}
	moovLen := len(buildMoov(specs, zero))

	// Second pass with real offsets: ftyp | moov | mdat header | payloads.
	dataStart := uint32(len(ftyp) + moovLen + 8)
	offsets := make([][]uint32, len(specs))
	cur := dataStart
	for i, cs := range chunkSizes {
		for _, sz := range cs {
			offsets[i] = append(offsets[i], cur)
			cur += sz
		}
	}
	moov := buildMoov(specs, offsets)

	var mdatPayload []byte
	for _, pl := range payloads {
		mdatPayload = append(mdatPayload, pl...)
	}
	var hdr [8]byte
	binary.BigEndian.PutUint32(hdr[0:4], uint32(8+len(mdatPayload)))
	copy(hdr[4:8], "mdat")

	out := append(ftyp, moov...)
	out = append(out, hdr[:]...)
	return append(out, mdatPayload...)
}
