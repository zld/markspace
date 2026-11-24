package sample

import (
	"bytes"
	"encoding/json"

	"archive/internal/scan"
)

// buildMoov assembles the moov box. When chunks is nil a placeholder moov
// (zero offsets, identical size) is produced so the caller can measure the
// final moov length before computing real chunk offsets.
func buildMoov(opts MP4Options, vSizes, aSizes, vChunks, aChunks []uint32) []byte {
	if vChunks == nil {
		vChunks = make([]uint32, opts.VideoSamples)
		aChunks = make([]uint32, opts.AudioSamples)
		vSizes = make([]uint32, opts.VideoSamples)
		aSizes = make([]uint32, opts.AudioSamples)
	}
	video := buildTrak(1, "vide", opts, vSizes, vChunks)
	audio := buildTrak(2, "soun", opts, aSizes, aChunks)
	vDurMs := uint32(int64(opts.VideoSamples) * videoDelta * 1000 / videoTimescale)
	aDurMs := uint32(int64(opts.AudioSamples) * audioDelta * 1000 / audioTimescale)
	durMs := vDurMs
	if aDurMs > durMs {
		durMs = aDurMs
	}
	mvhdPayload := bytes.Join([][]byte{
		u32(0), u32(0), u32(1000), u32(durMs),
		u32(0x00010000), u16(0x0100), u16(0), make([]byte, 8),
		matrixFor(0), make([]byte, 24), u32(3),
	}, nil)
	return box("moov", bytes.Join([][]byte{fullbox("mvhd", 0, 0, mvhdPayload), video, audio}, nil))
}

func buildTrak(id int, handler string, opts MP4Options, sizes, chunks []uint32) []byte {
	isVideo := handler == "vide"
	timescale := uint32(audioTimescale)
	delta := uint32(audioDelta)
	durUnits := uint64(len(sizes)) * uint64(delta)
	volume := uint16(0x0100)
	width, height := uint32(0), uint32(0)
	rotation := 0
	if isVideo {
		timescale = videoTimescale
		delta = videoDelta
		durUnits = uint64(len(sizes)) * uint64(delta)
		volume = 0
		width, height = 1920<<16, 1080<<16
		rotation = opts.Rotation
	}
	durMs := uint32(durUnits * 1000 / uint64(timescale))
	tkhdPayload := bytes.Join([][]byte{
		u32(0), u32(0), u32(uint32(id)), u32(0), u32(durMs), make([]byte, 8),
		u16(0), u16(0), u16(volume), u16(0), matrixFor(rotation), u32(width), u32(height),
	}, nil)
	mdhdPayload := bytes.Join([][]byte{
		u32(0), u32(0), u32(timescale), u32(uint32(durUnits)), u16(0), u16(0),
	}, nil)
	hdlrPayload := bytes.Join([][]byte{
		u32(0), []byte(handler), make([]byte, 12), []byte("archive\x00"),
	}, nil)
	trak := bytes.Join([][]byte{
		fullbox("tkhd", 0, 7, tkhdPayload),
		buildMetaUUID(id, opts),
		box("mdia", bytes.Join([][]byte{
			fullbox("mdhd", 0, 0, mdhdPayload),
			fullbox("hdlr", 0, 0, hdlrPayload),
			box("minf", buildMinf(isVideo, sizes, chunks, delta)),
		}, nil)),
	}, nil)
	return box("trak", trak)
}

// buildMetaUUID writes the trak-level metadata box: crop plus two colour
// descriptions that conflict when opts.ColorConflict is set.
func buildMetaUUID(track int, opts MP4Options) []byte {
	extra := scan.MetaExtra{}
	if track == 1 {
		extra.Crop = &scan.Crop{Top: 8, Bottom: 8, Left: 16, Right: 16}
		extra.Color = &scan.ColorDesc{Primaries: 1, Transfer: 1, Matrix: 1, FullRange: false}
		if opts.ColorConflict {
			extra.ColorAlt = &scan.ColorDesc{Primaries: 9, Transfer: 16, Matrix: 9, FullRange: true}
		}
	}
	payload, _ := json.Marshal(extra)
	return box("uuid", append(scan.MetaUUID[:], payload...))
}

func buildMinf(isVideo bool, sizes, chunks []uint32, delta uint32) []byte {
	var mediaHeader, entry []byte
	if isVideo {
		mediaHeader = fullbox("vmhd", 0, 1, bytes.Join([][]byte{u16(0), u16(0), u16(0), u16(0)}, nil))
		entry = box("mp4v", bytes.Join([][]byte{
			make([]byte, 6), u16(1), make([]byte, 16),
			u16(1920), u16(1080), u32(0x00480000), u32(0x00480000), u32(0), u16(1),
			make([]byte, 32), u16(0x18), u16(0xFFFF),
		}, nil))
	} else {
		mediaHeader = fullbox("smhd", 0, 0, bytes.Join([][]byte{u16(0), u16(0)}, nil))
		entry = box("mp4a", bytes.Join([][]byte{
			make([]byte, 6), u16(1), make([]byte, 8),
			u16(2), u16(16), u16(0), u16(0), u32(44100 << 16),
		}, nil))
	}
	dref := fullbox("dref", 0, 0, append(u32(1), fullbox("url ", 0, 1, nil)...))
	stsd := fullbox("stsd", 0, 0, append(u32(1), entry...))
	stts := fullbox("stts", 0, 0, bytes.Join([][]byte{u32(1), u32(uint32(len(sizes))), u32(delta)}, nil))
	stsc := fullbox("stsc", 0, 0, bytes.Join([][]byte{u32(1), u32(1), u32(1), u32(1)}, nil))
	szEntries := make([]byte, 0, 4*len(sizes))
	for _, s := range sizes {
		szEntries = append(szEntries, u32(s)...)
	}
	stsz := fullbox("stsz", 0, 0, bytes.Join([][]byte{u32(0), u32(uint32(len(sizes))), szEntries}, nil))
	coEntries := make([]byte, 0, 4*len(chunks))
	for _, c := range chunks {
		coEntries = append(coEntries, u32(c)...)
	}
	stco := fullbox("stco", 0, 0, append(u32(uint32(len(chunks))), coEntries...))
	stbl := box("stbl", bytes.Join([][]byte{stsd, stts, stsc, stsz, stco}, nil))
	return bytes.Join([][]byte{mediaHeader, box("dinf", dref), stbl}, nil)
}
