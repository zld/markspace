// Package fixtures builds deterministic sample inputs for tests and the demo.
// MP4 samples are written box by box so tests can corrupt a single byte at a
// known offset; parsing is always done by the production packages.
package fixtures

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
)

// MP4Spec describes a synthetic video track.
type MP4Spec struct {
	Timescale   uint32   // media timescale, e.g. 24000
	SampleDelta []uint32 // per-sample decode delta in timescale units
	SampleSizes []uint32 // payload bytes per sample
	Keyframes   []int    // 1-based sample numbers that are sync samples
	Width       uint16
	Height      uint16
	// FreePad inserts a 'free' box of this many bytes, derived from the seed.
	FreePad int
	// Fill is a deterministic payload pattern; nil means zeros.
	Fill []byte
}

// MP4Layout records offsets that tests use to truncate the sample table.
type MP4Layout struct {
	Path       string
	Size       int64
	StszOffset int64
	StcoOffset int64
	MdatOffset int64
	StszSize   int64
}

func be16(v uint16) []byte { b := make([]byte, 2); binary.BigEndian.PutUint16(b, v); return b }
func be32(v uint32) []byte { b := make([]byte, 4); binary.BigEndian.PutUint32(b, v); return b }

func makeBox(typ string, parts ...[]byte) []byte {
	n := 8
	for _, p := range parts {
		n += len(p)
	}
	out := make([]byte, 0, n)
	out = append(out, be32(uint32(n))...)
	out = append(out, typ...)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func makeFullBox(typ string, version byte, flags uint32, parts ...[]byte) []byte {
	head := []byte{version, byte(flags >> 16), byte(flags >> 8), byte(flags)}
	return makeBox(typ, append([][]byte{head}, parts...)...)
}

func countBox(typ string, entries ...[]byte) []byte {
	return makeFullBox(typ, 0, 0, append([][]byte{be32(uint32(len(entries)))}, entries...)...)
}

// WriteMP4 writes a valid MP4 with an uncompressed (size-only) video track.
func WriteMP4(path string, spec MP4Spec, seed int64) (MP4Layout, error) {
	n := len(spec.SampleDelta)
	if n == 0 || n != len(spec.SampleSizes) {
		return MP4Layout{}, fmt.Errorf("fixtures: sample delta/size mismatch")
	}
	if spec.Width == 0 {
		spec.Width, spec.Height = 640, 360
	}

	ftyp := makeBox("ftyp", []byte("isom"), be32(0x200), []byte("isomiso2avc1mp41"))
	// Media payload.
	var mdatPayload []byte
	offsets := make([]uint32, n)
	for i, sz := range spec.SampleSizes {
		offsets[i] = uint32(len(mdatPayload))
		chunk := make([]byte, sz)
		if len(spec.Fill) > 0 {
			for k := range chunk {
				chunk[k] = spec.Fill[(i*7+k)%len(spec.Fill)]
			}
		}
		mdatPayload = append(mdatPayload, chunk...)
	}

	var extra []byte
	if spec.FreePad > 0 {
		pad := make([]byte, spec.FreePad)
		for i := range pad {
			pad[i] = byte(seed + int64(i))
		}
		extra = makeBox("free", pad)
	}
	mdatOffset := int64(len(ftyp) + len(extra) + 8)
	mdatAbs := makeBox("mdat", mdatPayload)

	// Sample tables.
	stts := countBox("stts")
	deltas := compressDeltas(spec.SampleDelta)
	var sttsEntries [][]byte
	for _, d := range deltas {
		sttsEntries = append(sttsEntries, be32(d.count), be32(d.delta))
	}
	stts = makeFullBox("stts", 0, 0, append([][]byte{be32(uint32(len(deltas)))}, sttsEntries...)...)

	var syncNums [][]byte
	for _, k := range spec.Keyframes {
		syncNums = append(syncNums, be32(uint32(k)))
	}
	stss := makeFullBox("stss", 0, 0, append([][]byte{be32(uint32(len(syncNums)))}, syncNums...)...)

	stszParts := [][]byte{be32(0), be32(uint32(n))}
	for _, s := range spec.SampleSizes {
		stszParts = append(stszParts, be32(s))
	}
	stszPayloadLen := 4 + 4 + 4*n
	stsz := makeFullBox("stsz", 0, 0, stszParts...)

	// One chunk holding all samples; offsets are absolute file offsets.
	chunkOffsets := make([][]byte, n)
	for i := range offsets {
		chunkOffsets[i] = be32(uint32(mdatOffset) + offsets[i])
	}
	stco := makeFullBox("stco", 0, 0, append([][]byte{be32(uint32(n))}, chunkOffsets...)...)
	stsc := makeFullBox("stsc", 0, 0, be32(1), be32(1), be32(uint32(n)), be32(1))

	avcC := makeBox("avcC", []byte{1, 0x42, 0xC0, 0x1F, 0xFF, 0xE1, 0, 2, 0x67, 0x42, 1, 0, 2, 0x68, 0xCE})
	avc1Payload := make([]byte, 78)
	copy(avc1Payload[24:26], be16(spec.Width))
	copy(avc1Payload[26:28], be16(spec.Height))
	copy(avc1Payload[28:32], be32(0x00480000))
	copy(avc1Payload[32:36], be32(0x00480000))
	copy(avc1Payload[40:42], be16(1))
	copy(avc1Payload[74:76], be16(24))
	avc1 := makeBox("avc1", avc1Payload, avcC)
	stsd := makeFullBox("stsd", 0, 0, be32(1), avc1)

	vmhd := makeFullBox("vmhd", 0, 1, be16(0), be16(0), be16(0), be16(0))
	url := makeFullBox("url ", 0, 1)
	dref := makeFullBox("dref", 0, 0, be32(1), url)
	dinf := makeBox("dinf", dref)
	stbl := makeBox("stbl", stsd, stts, stss, stsz, stsc, stco)
	minf := makeBox("minf", vmhd, dinf, stbl)

	lang := make([]byte, 2)
	binary.BigEndian.PutUint16(lang, ((uint16('u'-0x60) << 10) | (uint16('n'-0x60) << 5) | uint16('d'-0x60)))
	mdhd := makeFullBox("mdhd", 0, 0, be32(0), be32(0), be32(spec.Timescale),
		be32(totalDuration(spec.SampleDelta)), lang, be16(0))
	hdlr := makeFullBox("hdlr", 0, 0, be32(0), []byte("vide"), be32(0), be32(0), be32(0),
		append([]byte("mediasync video"), 0))
	mdia := makeBox("mdia", mdhd, hdlr, minf)

	matrix := make([]byte, 36)
	copy(matrix[0:4], be32(0x00010000))
	copy(matrix[16:20], be32(0x00010000))
	copy(matrix[32:36], be32(0x40000000))
	tkhd := makeFullBox("tkhd", 0, 3, be32(0), be32(0), be32(1), be32(0),
		be32(totalDuration(spec.SampleDelta)), be32(0), be32(0), be16(0), be16(0),
		be16(0), be16(0), matrix, be32(uint32(spec.Width)<<16), be32(uint32(spec.Height)<<16))
	trak := makeBox("trak", tkhd, mdia)

	mvhdMatrix := make([]byte, 36)
	copy(mvhdMatrix, matrix)
	mvhd := makeFullBox("mvhd", 0, 0, be32(0), be32(0), be32(1000),
		be32(totalDuration(spec.SampleDelta)), be32(0x00010000), be16(0x0100), be16(0),
		be32(0), be32(0), mvhdMatrix, make([]byte, 24), be32(2))
	moov := makeBox("moov", mvhd, trak)

	// Locate stsz/stco inside moov for layout metadata.
	moovBytes := moov
	stszOff := int64(len(ftyp)+len(extra)+len(mdatAbs)) + findBox(moovBytes, "stsz")
	stcoOff := int64(len(ftyp)+len(extra)+len(mdatAbs)) + findBox(moovBytes, "stco")

	out := make([]byte, 0, len(ftyp)+len(extra)+len(mdatAbs)+len(moovBytes))
	out = append(out, ftyp...)
	out = append(out, extra...)
	out = append(out, mdatAbs...)
	out = append(out, moovBytes...)
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return MP4Layout{}, err
	}
	return MP4Layout{
		Path: path, Size: int64(len(out)),
		StszOffset: stszOff, StcoOffset: stcoOff,
		MdatOffset: mdatOffset, StszSize: int64(stszPayloadLen + 12),
	}, nil
}

type deltaRun struct {
	count uint32
	delta uint32
}

func compressDeltas(d []uint32) []deltaRun {
	var out []deltaRun
	for _, v := range d {
		if len(out) > 0 && out[len(out)-1].delta == v {
			out[len(out)-1].count++
			continue
		}
		out = append(out, deltaRun{count: 1, delta: v})
	}
	return out
}

func totalDuration(d []uint32) uint32 {
	var t uint32
	for _, v := range d {
		t += v
	}
	return t
}

// findBox returns the offset of the first box with the given type, searching
// the top level and the one level of nesting used by the fixture.
func findBox(data []byte, typ string) int64 {
	off := int64(0)
	for off+8 <= int64(len(data)) {
		size := int64(binary.BigEndian.Uint32(data[off : off+4]))
		kind := string(data[off+4 : off+8])
		if size < 8 || off+size > int64(len(data)) {
			return -1
		}
		if kind == typ {
			return off
		}
		if kind == "moov" || kind == "trak" || kind == "mdia" || kind == "minf" || kind == "stbl" {
			if inner := findBox(data[off+8:off+size], typ); inner >= 0 {
				return off + 8 + inner
			}
		}
		off += size
	}
	return -1
}

// BaseName returns the report label for a fixture file.
func BaseName(path string) string { return filepath.Base(path) }
