// Package fixtures builds deterministic MP4 and subtitle fixtures in memory.
// Nothing is read from disk or network; every byte is generated here.
package fixtures

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
)

// MP4Spec describes a single-video-track file to generate.
type MP4Spec struct {
	TrackID       uint32
	Timescale     uint32
	Width, Height uint16
	Sizes         []uint32 // per-sample payload sizes
	Deltas        []uint32 // per-sample decode durations
	Ctts          []int64  // optional per-sample composition offsets
	Keyframes     []int    // 1-based sync sample numbers
	// OutOfRangeKeyframe appends a sample number past the end of the table so
	// readers must reject the index and fall back to scanning.
	OutOfRangeKeyframe bool
	// MissingStss omits the keyframe index entirely.
	MissingStss bool
	// TruncateSample bytes are cut from the end of the mdat payload.
	TruncateSample int
	// SamplesPerChunk controls chunk packing (default 2).
	SamplesPerChunk uint32
	// CorruptStsc makes the chunk table claim fewer samples per chunk than
	// the sample table needs, so no scan can address every sample.
	CorruptStsc bool
	// Label is embedded in the payload so frame hashes are content-derived.
	Label string
}

// MP4 returns the generated file bytes.
func MP4(spec MP4Spec) ([]byte, error) {
	n := len(spec.Sizes)
	if n == 0 || len(spec.Deltas) != n {
		return nil, errors.New("fixtures: sizes and deltas must be non-empty and equal length")
	}
	if spec.Ctts != nil && len(spec.Ctts) != n {
		return nil, errors.New("fixtures: ctts length must match samples")
	}
	if spec.TrackID == 0 {
		spec.TrackID = 1
	}
	if spec.Timescale == 0 {
		spec.Timescale = 30000
	}
	if spec.Width == 0 {
		spec.Width, spec.Height = 64, 36
	}
	if spec.SamplesPerChunk == 0 {
		spec.SamplesPerChunk = 2
	}
	if spec.TruncateSample > int(sum(spec.Sizes)) {
		return nil, errors.New("fixtures: truncation exceeds mdat size")
	}
	if spec.Label == "" {
		spec.Label = "fx"
	}

	chunks := chunkOffsetsRelative(spec)
	payload := buildPayload(spec)
	ftyp := ftypBox()

	// Build moov once with zero offsets to learn its exact size, then rebuild
	// with correct chunk offsets. Box sizes cannot change between passes.
	moovLen := len(moovBox(spec, make([]uint32, len(chunks))))
	mdatStart := uint64(len(ftyp)) + uint64(moovLen) + 8
	offsets := make([]uint32, len(chunks))
	for i, rel := range chunks {
		offsets[i] = uint32(mdatStart + uint64(rel))
	}
	moov := moovBox(spec, offsets)
	if len(moov) != moovLen {
		return nil, fmt.Errorf("fixtures: moov size changed between passes (%d vs %d)", moovLen, len(moov))
	}

	body := payload
	if spec.TruncateSample > 0 {
		body = body[:len(body)-spec.TruncateSample]
	}
	out := make([]byte, 0, len(ftyp)+len(moov)+8+len(body))
	out = append(out, ftyp...)
	out = append(out, moov...)
	out = append(out, boxHeader("mdat", uint32(len(payload)))...)
	out = append(out, body...)
	return out, nil
}

func sum(v []uint32) uint64 {
	var n uint64
	for _, x := range v {
		n += uint64(x)
	}
	return n
}

// chunkOffsetsRelative returns each chunk's byte offset within the mdat
// payload.
func chunkOffsetsRelative(spec MP4Spec) []uint32 {
	var starts []uint32
	var off uint32
	for i := 0; i < len(spec.Sizes); i += int(spec.SamplesPerChunk) {
		starts = append(starts, off)
		for j := i; j < i+int(spec.SamplesPerChunk) && j < len(spec.Sizes); j++ {
			off += spec.Sizes[j]
		}
	}
	return starts
}

// buildPayload writes deterministic sample bytes. Each sample starts with a
// readable marker so a renderer can verify it read the right sample.
func buildPayload(spec MP4Spec) []byte {
	buf := make([]byte, 0, sum(spec.Sizes))
	for i, size := range spec.Sizes {
		marker := fmt.Sprintf("%s/S%04d:", spec.Label, i)
		for j := uint32(0); j < size; j++ {
			if j < uint32(len(marker)) {
				buf = append(buf, marker[j])
			} else {
				buf = append(buf, byte((int(j)*31+int(i)*17)%251))
			}
		}
	}
	return buf
}

// ---- byte-level box builders ----

func boxHeader(typ string, payloadLen uint32) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint32(b[0:4], payloadLen+8)
	copy(b[4:8], typ)
	return b
}

func box(typ string, payload []byte) []byte {
	out := boxHeader(typ, uint32(len(payload)))
	return append(out, payload...)
}

// fullBox writes version/flags followed by the payload.
func fullBox(typ string, version byte, flags uint32, payload []byte) []byte {
	hdr := []byte{version, byte(flags >> 16), byte(flags >> 8), byte(flags)}
	return box(typ, append(hdr, payload...))
}

func be32(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}

func be16(v uint16) []byte {
	b := make([]byte, 2)
	binary.BigEndian.PutUint16(b, v)
	return b
}

func ftypBox() []byte {
	p := concat([]byte("isom"), be32(0x200), []byte("isom"), []byte("mp41"))
	return box("ftyp", p)
}

func identityMatrix() []byte {
	vals := []uint32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000}
	var out []byte
	for _, v := range vals {
		out = append(out, be32(v)...)
	}
	return out
}

func moovBox(spec MP4Spec, chunkOffsets []uint32) []byte {
	duration := uint32(sum(spec.Deltas))
	mvhd := fullBox("mvhd", 0, 0, concat(
		be32(0), be32(0), be32(spec.Timescale), be32(duration),
		be32(0x00010000), be16(0x0100), be16(0), make([]byte, 8),
		identityMatrix(),
		make([]byte, 24), // pre_defined
		be32(spec.TrackID+1),
	))
	tkhd := fullBox("tkhd", 0, 3, concat(
		be32(0), be32(0), be32(spec.TrackID), be32(0), be32(duration),
		make([]byte, 8), be16(0), be16(0), be16(0), be16(0),
		identityMatrix(),
		be32(uint32(spec.Width)<<16), be32(uint32(spec.Height)<<16),
	))
	hdlr := fullBox("hdlr", 0, 0, concat(
		be32(0), []byte("vide"), make([]byte, 12), []byte("VideoHandler\x00"),
	))
	stbl := concat(
		buildStsd(spec), buildStts(spec),
		func() []byte {
			if spec.Ctts == nil {
				return nil
			}
			return buildCtts(spec)
		}(),
		buildStsc(spec), buildStsz(spec), buildStco(chunkOffsets),
		func() []byte {
			if spec.MissingStss {
				return nil
			}
			return buildStss(spec)
		}(),
	)
	mdhd := fullBox("mdhd", 0, 0, concat(
		be32(0), be32(0), be32(spec.Timescale), be32(duration), be16(0x55c4), be16(0),
	))
	minf := box("minf", box("stbl", stbl))
	mdia := box("mdia", concat(mdhd, hdlr, minf))
	trak := box("trak", concat(tkhd, mdia))
	return box("moov", concat(mvhd, trak))
}

func concat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func buildStsd(spec MP4Spec) []byte {
	entry := make([]byte, 0, 78)
	entry = append(entry, make([]byte, 6)...)  // reserved
	entry = append(entry, be16(1)...)          // data reference index
	entry = append(entry, make([]byte, 16)...) // pre_defined/reserved/pre_defined2
	entry = append(entry, be16(spec.Width)...)
	entry = append(entry, be16(spec.Height)...)
	entry = append(entry, be32(0x00480000)...) // horizresolution 72 dpi
	entry = append(entry, be32(0x00480000)...) // vertresolution
	entry = append(entry, make([]byte, 4)...)  // reserved
	entry = append(entry, be16(1)...)          // frame_count
	entry = append(entry, make([]byte, 32)...) // compressorname
	entry = append(entry, be16(0x0018)...)     // depth
	entry = append(entry, be16(0xffff)...)     // pre_defined = -1
	avc1 := box("avc1", entry)
	return fullBox("stsd", 0, 0, concat(be32(1), avc1))
}

func buildStts(spec MP4Spec) []byte {
	type run struct{ count, delta uint32 }
	var runs []run
	for _, d := range spec.Deltas {
		if n := len(runs); n > 0 && runs[n-1].delta == d {
			runs[n-1].count++
			continue
		}
		runs = append(runs, run{1, d})
	}
	p := be32(uint32(len(runs)))
	for _, r := range runs {
		p = append(p, be32(r.count)...)
		p = append(p, be32(r.delta)...)
	}
	return fullBox("stts", 0, 0, p)
}

func buildCtts(spec MP4Spec) []byte {
	type run struct{ count, off uint32 }
	var runs []run
	for _, o := range spec.Ctts {
		if n := len(runs); n > 0 && runs[n-1].off == uint32(o) {
			runs[n-1].count++
			continue
		}
		runs = append(runs, run{1, uint32(o)})
	}
	p := be32(uint32(len(runs)))
	for _, r := range runs {
		p = append(p, be32(r.count)...)
		p = append(p, be32(r.off)...)
	}
	return fullBox("ctts", 0, 0, p)
}

func buildStsc(spec MP4Spec) []byte {
	spc := spec.SamplesPerChunk
	if spec.CorruptStsc {
		spc = 1
	}
	p := concat(be32(1), be32(1), be32(spc), be32(1))
	return fullBox("stsc", 0, 0, p)
}

func buildStsz(spec MP4Spec) []byte {
	p := concat(be32(0), be32(uint32(len(spec.Sizes))))
	for _, s := range spec.Sizes {
		p = append(p, be32(s)...)
	}
	return fullBox("stsz", 0, 0, p)
}

func buildStco(offsets []uint32) []byte {
	p := be32(uint32(len(offsets)))
	for _, o := range offsets {
		p = append(p, be32(o)...)
	}
	return fullBox("stco", 0, 0, p)
}

func buildStss(spec MP4Spec) []byte {
	nums := make([]uint32, 0, len(spec.Keyframes)+1)
	for _, k := range spec.Keyframes {
		nums = append(nums, uint32(k))
	}
	if spec.OutOfRangeKeyframe {
		nums = append(nums, uint32(len(spec.Sizes)+3))
	}
	p := be32(uint32(len(nums)))
	for _, n := range nums {
		p = append(p, be32(n)...)
	}
	return fullBox("stss", 0, 0, p)
}

// CorruptIndexBytes rewrites the stss fourcc to an unknown type, leaving the
// box in place but unreadable as a keyframe index.
func CorruptIndexBytes(data []byte) ([]byte, error) {
	idx := bytes.Index(data, []byte("stss"))
	if idx < 4 {
		return nil, errors.New("fixtures: no stss box to corrupt")
	}
	out := append([]byte(nil), data...)
	copy(out[idx:idx+4], []byte("zzzz"))
	return out, nil
}
