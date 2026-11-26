package main

import (
	"bytes"
	"encoding/binary"
)

func box(typ string, parts ...[]byte) []byte {
	body := bytes.Join(parts, nil)
	out := make([]byte, 8+len(body))
	binary.BigEndian.PutUint32(out[:4], uint32(8+len(body)))
	copy(out[4:8], typ)
	copy(out[8:], body)
	return out
}

func fullBox(version byte, payload []byte) []byte {
	return append([]byte{version, 0, 0, 0}, payload...)
}

func be32(values ...uint32) []byte {
	out := make([]byte, 4*len(values))
	for i, v := range values {
		binary.BigEndian.PutUint32(out[4*i:], v)
	}
	return out
}

func be16(values ...uint16) []byte {
	out := make([]byte, 2*len(values))
	for i, v := range values {
		binary.BigEndian.PutUint16(out[2*i:], v)
	}
	return out
}

var identityMatrix = []byte{
	0x00, 0x01, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 0x00, 0x01, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	0x40, 0, 0, 0,
}

// buildAACMP4 builds a small non-fragmented MP4 whose stts/stsc/stsz/stco
// tables describe six undecoded AAC sample frames. Variable deltas exercise
// duration conversion instead of assuming every container packet is equal.
func buildAACMP4(sizes, deltas []uint32) []byte {
	frameCount := uint32(len(sizes))
	if len(deltas) != len(sizes) {
		deltas = make([]uint32, frameCount)
		for i := range deltas {
			deltas[i] = 1024
		}
	}
	var totalDuration uint32
	for _, delta := range deltas {
		totalDuration += delta
	}
	ftyp := box("ftyp", []byte("M4A "), be32(0), []byte("M4A "), []byte("isom"))

	mvhdPayload := bytes.Join([][]byte{
		be32(0, 0, 44100, totalDuration),
		be32(0x00010000),
		be16(0x0100),
		make([]byte, 10),
		identityMatrix,
		make([]byte, 24),
		be32(2),
	}, nil)
	mvhd := box("mvhd", fullBox(0, mvhdPayload))

	tkhdPayload := bytes.Join([][]byte{
		be32(0, 0, 1, 0, totalDuration),
		make([]byte, 8),
		be16(0, 0, 0, 0),
		identityMatrix,
		be32(0, 0),
	}, nil)
	tkhd := box("tkhd", fullBox(0, tkhdPayload))
	mdhd := box("mdhd", fullBox(0,
		append(be32(0, 0, 44100, totalDuration), be16(0x55c4, 0)...)))
	hdlr := box("hdlr", fullBox(0,
		bytes.Join([][]byte{be32(0), []byte("soun"), be32(0, 0, 0), []byte("AAC\x00")}, nil)))

	sttsEntries := be32(uint32(len(deltas)))
	for _, delta := range deltas {
		sttsEntries = append(sttsEntries, be32(1, delta)...)
	}
	stts := box("stts", fullBox(0, sttsEntries))
	stsc := box("stsc", fullBox(0, append(be32(1), be32(1, frameCount, 1)...)))
	stszPayload := append(be32(0, frameCount), make([]byte, 4*frameCount)...)
	for i, size := range sizes {
		binary.BigEndian.PutUint32(stszPayload[8+4*i:], size)
	}
	stsz := box("stsz", fullBox(0, stszPayload))
	stcoZero := box("stco", fullBox(0, append(be32(1), be32(0)...)))
	stbl := box("stbl", stts, stsc, stsz, stcoZero)
	smhd := box("smhd", fullBox(0, append(be16(0), be16(0)...)))
	minf := box("minf", smhd, stbl)
	mdia := box("mdia", mdhd, hdlr, minf)
	trak := box("trak", tkhd, mdia)
	moov := box("moov", mvhd, trak)

	payload := make([]byte, 0)
	for _, size := range sizes {
		payload = append(payload, make([]byte, int(size))...)
	}
	dataOffset := uint32(len(ftyp) + len(moov) + 8)
	stco := box("stco", fullBox(0, append(be32(1), be32(dataOffset)...)))
	moov = bytes.Replace(moov, stcoZero, stco, 1)
	mdat := box("mdat", payload)
	return bytes.Join([][]byte{ftyp, moov, mdat}, nil)
}
