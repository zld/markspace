// Package fixture generates deterministic demo assets entirely in memory.
package fixture

import (
	"encoding/binary"
)

func be32(v uint32) []byte {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	return b[:]
}

func be16(v uint16) []byte {
	var b [2]byte
	binary.BigEndian.PutUint16(b[:], v)
	return b[:]
}

func be64(v uint64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	return b[:]
}

func cat(parts ...[]byte) []byte {
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

func box(typ string, payload ...[]byte) []byte {
	body := cat(payload...)
	return cat(be32(uint32(8+len(body))), []byte(typ), body)
}

func fullBox(typ string, version byte, flags uint32, payload ...[]byte) []byte {
	body := cat(payload...)
	head := cat(be32(uint32(8+4+len(body))), []byte(typ), []byte{version},
		be32(flags & 0x00ffffff)[1:])
	return cat(head, body)
}

// mp4Spec controls generation of one synthetic video MP4.
type mp4Spec struct {
	Timescale      uint32
	FrameDeltas    []uint32 // decode durations in track ticks
	CTTSOffsets    []int64  // per-frame composition offsets; nil means all zero
	SyncFrames     []int    // 1-based sync sample numbers; nil means every frame
	FrameSizes     []uint32
	MovieTimescale uint32
}

// buildMP4 renders a single-video-track, single-chunk MP4 with AVC config.
func buildMP4(spec mp4Spec) []byte {
	ftyp := box("ftyp", []byte("isom"), be32(512), []byte("isom"), []byte("iso2"), []byte("avc1"), []byte("mp41"))
	n := len(spec.FrameDeltas)
	duration := uint64(0)
	for _, d := range spec.FrameDeltas {
		duration += uint64(d)
	}
	if spec.FrameSizes == nil {
		spec.FrameSizes = make([]uint32, n)
		for i := range spec.FrameSizes {
			spec.FrameSizes[i] = uint32(120 + (i*7)%64)
		}
	}
	// stts entries grouped by delta.
	var sttsEntries []byte
	var cttsEntries []byte
	sttsCount := 0
	for i := 0; i < n; {
		j := i + 1
		for j < n && spec.FrameDeltas[j] == spec.FrameDeltas[i] {
			j++
		}
		sttsEntries = append(sttsEntries, be32(uint32(j-i))...)
		sttsEntries = append(sttsEntries, be32(spec.FrameDeltas[i])...)
		sttsCount++
		i = j
	}
	cttsCount := 0
	if spec.CTTSOffsets != nil {
		for i := 0; i < n; {
			j := i + 1
			for j < n && spec.CTTSOffsets[j] == spec.CTTSOffsets[i] {
				j++
			}
			cttsEntries = append(cttsEntries, be32(uint32(j-i))...)
			cttsEntries = append(cttsEntries, be32(uint32(spec.CTTSOffsets[i]))...)
			cttsCount++
			i = j
		}
	}
	var syncNumbers []byte
	syncCount := 0
	for _, s := range spec.SyncFrames {
		if s >= 1 && s <= n {
			syncNumbers = append(syncNumbers, be32(uint32(s))...)
			syncCount++
		}
	}
	var sizes []byte
	for _, sz := range spec.FrameSizes {
		sizes = append(sizes, be32(sz)...)
	}
	mdatPayload := make([]byte, 0, 1024)
	for i, sz := range spec.FrameSizes {
		sample := make([]byte, sz)
		for k := range sample {
			sample[k] = byte((i*31 + k*7) & 0xff)
		}
		mdatPayload = append(mdatPayload, sample...)
	}

	buildMoov := func(chunkOffset uint32) []byte {
		sampleEntry := box("avc1",
			make([]byte, 6),                             // reserved
			be16(1),                                     // data_reference_index
			be16(0), be16(0), be32(0), be32(0), be32(0), // predefined/reserved
			be16(640), be16(360),
			be32(0x00480000), be32(0x00480000),
			be32(0), be16(1),
			compressorName("mediad fixture"),
			be16(0x0018), be16(0xffff),
			box("avcC", []byte{1, 0x64, 0x00, 0x1f, 0xff, 0xe1}, be16(5),
				[]byte{0x67, 0x64, 0x00, 0x1f, 0xac}, []byte{0x01}, be16(4),
				[]byte{0x68, 0xeb, 0xe3, 0xcb}),
		)
		stsd := fullBox("stsd", 0, 0, be32(1), sampleEntry)
		stts := fullBox("stts", 0, 0, be32(uint32(sttsCount)), sttsEntries)
		var ctts []byte
		if spec.CTTSOffsets != nil {
			ctts = fullBox("ctts", 0, 0, be32(uint32(cttsCount)), cttsEntries)
		}
		var stss []byte
		if spec.SyncFrames != nil {
			stss = fullBox("stss", 0, 0, be32(uint32(syncCount)), syncNumbers)
		}
		stsc := fullBox("stsc", 0, 0, be32(1), be32(1), be32(uint32(n)), be32(1))
		stsz := fullBox("stsz", 0, 0, be32(0), be32(uint32(n)), sizes)
		stco := fullBox("stco", 0, 0, be32(1), be32(chunkOffset))
		stbl := box("stbl", stsd, stts, ctts, stss, stsc, stsz, stco)
		minf := box("minf", fullBox("vmhd", 0, 1, be16(0), be16(0), be16(0), be16(0)), stbl)
		mdhd := fullBox("mdhd", 0, 0, be32(0), be32(0), be32(spec.Timescale),
			be32(uint32(duration)), be16(0x55c4), be16(0))
		hdlr := fullBox("hdlr", 0, 0, be32(0), []byte("vide"), be32(0), be32(0), be32(0), []byte("VideoHandler\x00"))
		mdia := box("mdia", mdhd, hdlr, minf)
		tkhd := fullBox("tkhd", 0, 3,
			be32(0), be32(0), be32(1), be32(0), be32(uint32(duration)),
			be32(0), be32(0), be16(0), be16(0), be16(0), be16(0),
			matrix(), be32(640<<16), be32(360<<16))
		trak := box("trak", tkhd, mdia)
		mvhd := fullBox("mvhd", 0, 0, be32(0), be32(0), be32(spec.MovieTimescale),
			be32(uint32(duration)), be32(0x00010000), be16(0x0100), be16(0),
			be32(0), be32(0), matrix(), make([]byte, 24), be32(2))
		return box("moov", mvhd, trak)
	}
	moov := buildMoov(0)
	mdatStart := len(ftyp) + len(moov)
	moov = buildMoov(uint32(mdatStart + 8))
	mdat := box("mdat", mdatPayload)
	return cat(ftyp, moov, mdat)
}

func compressorName(s string) []byte {
	out := make([]byte, 32)
	if len(s) > 30 {
		s = s[:30]
	}
	out[0] = byte(len(s))
	copy(out[1:], s)
	return out
}

func matrix() []byte {
	vals := []uint32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000}
	var out []byte
	for _, v := range vals {
		out = append(out, be32(v)...)
	}
	return out
}
