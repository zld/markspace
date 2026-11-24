package sample

import (
	"bytes"
	"encoding/binary"
	"os"

	"archive/internal/model"
)

// MP4Options tunes the generated MP4 fixture.
type MP4Options struct {
	VideoSamples   int   // default 12 (25fps, 90kHz)
	AudioSamples   int   // default 18 (1024 samples @ 44.1kHz)
	VideoPacketLen int   // essence bytes per video sample, default 512
	AudioPacketLen int   // essence bytes per audio sample, default 256
	Rotation       int   // tkhd matrix rotation, default 90
	ColorConflict  bool  // write a conflicting second colour description
	CorruptAt      int64 // flip one byte here; -1 disables
	TruncateAt     int64 // cut the file here; -1 disables
}

const (
	videoTimescale = 90000
	videoDelta     = 3600 // 25fps
	audioTimescale = 44100
	audioDelta     = 1024
)

// GenMP4 writes a deterministic MP4 (moov before mdat) with one video and
// one audio track, then applies the requested damage.
func GenMP4(path string, opts MP4Options) (*Info, error) {
	if opts.VideoSamples == 0 {
		opts.VideoSamples = 12
	}
	if opts.AudioSamples == 0 {
		opts.AudioSamples = 18
	}
	if opts.VideoPacketLen == 0 {
		opts.VideoPacketLen = 512
	}
	if opts.AudioPacketLen == 0 {
		opts.AudioPacketLen = 256
	}
	if opts.Rotation == 0 {
		opts.Rotation = 90
	}
	var merged []mergedSample
	for i := 0; i < opts.VideoSamples; i++ {
		pts := int64(i) * videoDelta
		merged = append(merged, mergedSample{1, model.ToMaster(pts, model.Timescale(videoTimescale)),
			model.PackEssence(1, i%4 == 0, pts, fill(1, i, opts.VideoPacketLen-model.EssenceHdrSize))})
	}
	for i := 0; i < opts.AudioSamples; i++ {
		pts := int64(i) * audioDelta
		merged = append(merged, mergedSample{2, model.ToMaster(pts, model.Timescale(audioTimescale)),
			model.PackEssence(2, false, pts, fill(2, i, opts.AudioPacketLen-model.EssenceHdrSize))})
	}
	sortMerged(merged)

	// Assemble per-track tables in file order (one chunk per sample).
	var vSizes, aSizes []uint32
	var vChunks, aChunks []uint32
	info := &Info{CorruptAt: opts.CorruptAt, TruncateAt: opts.TruncateAt}
	moovLen := int64(len(buildMoov(opts, nil, nil, nil, nil)))
	ftyp := buildFtyp()
	mdatData := int64(len(ftyp)) + moovLen + 8
	off := mdatData
	var mdat bytes.Buffer
	for _, m := range merged {
		if m.track == 1 {
			vSizes = append(vSizes, uint32(len(m.data)))
			vChunks = append(vChunks, uint32(off))
		} else {
			aSizes = append(aSizes, uint32(len(m.data)))
			aChunks = append(aChunks, uint32(off))
		}
		info.Samples = append(info.Samples, SampleInfo{Track: m.track, Offset: off, Size: int64(len(m.data))})
		mdat.Write(m.data)
		off += int64(len(m.data))
	}
	moov := buildMoov(opts, vSizes, aSizes, vChunks, aChunks)

	var out bytes.Buffer
	out.Write(ftyp)
	out.Write(moov)
	out.Write(box("mdat", mdat.Bytes()))
	if err := os.WriteFile(path, out.Bytes(), 0o644); err != nil {
		return nil, err
	}
	if err := applyDamage(path, opts.CorruptAt, opts.TruncateAt); err != nil {
		return nil, err
	}
	return info, nil
}

// fill produces deterministic pseudo-media bytes for (track, seq).
func fill(track, seq, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(track*31 + seq*7 + i)
	}
	return b
}

func box(typ string, payload []byte) []byte {
	b := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(b[0:4], uint32(len(b)))
	copy(b[4:8], typ)
	copy(b[8:], payload)
	return b
}

func fullbox(typ string, version byte, flags uint32, payload []byte) []byte {
	h := []byte{version, byte(flags >> 16), byte(flags >> 8), byte(flags)}
	return box(typ, append(h, payload...))
}

func u32(v uint32) []byte { b := make([]byte, 4); binary.BigEndian.PutUint32(b, v); return b }
func u16(v uint16) []byte { b := make([]byte, 2); binary.BigEndian.PutUint16(b, v); return b }

func buildFtyp() []byte {
	return box("ftyp", []byte("isom\x00\x00\x00\x00isom"))
}

func matrixFor(rotation int) []byte {
	const one = 0x10000
	m := [9]int32{one, 0, 0, 0, one, 0, 0, 0, 0x40000000}
	switch rotation {
	case 90:
		m = [9]int32{0, one, 0, -one, 0, 0, 0, 0, 0x40000000}
	case 180:
		m = [9]int32{-one, 0, 0, 0, -one, 0, 0, 0, 0x40000000}
	case 270:
		m = [9]int32{0, -one, 0, one, 0, 0, 0, 0, 0x40000000}
	}
	b := make([]byte, 36)
	for i, v := range m {
		binary.BigEndian.PutUint32(b[i*4:], uint32(v))
	}
	return b
}
