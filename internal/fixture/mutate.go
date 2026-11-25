package fixture

import (
	"encoding/binary"
	"fmt"

	"assetcheck/internal/wav"
)

// MutateContent replaces PCM sample bytes in-place and refreshes the affected
// ABLK CRC, modeling a legitimate same-path asset replacement rather than
// corruption. Chunk layout and sizes stay identical, so only the content
// fingerprint changes.
func MutateContent(b []byte) ([]byte, error) {
	w, err := wav.Parse(b)
	if err != nil {
		return nil, fmt.Errorf("mutate content: %w", err)
	}
	chunks, _ := wav.WalkChunks(b, 12)
	ablk := wav.FindChunk(chunks, wav.BlockTableID)
	if ablk == nil {
		return nil, fmt.Errorf("mutate content: no ABLK integrity chunk")
	}
	const blockIndex = 10
	align := int64(w.Config.BlockAlign)
	blockFrames := int64(512)
	byteStart := blockIndex * blockFrames * align
	byteEnd := byteStart + blockFrames*align
	if byteEnd > int64(len(w.FramedPCM)) {
		return nil, fmt.Errorf("mutate content: block out of range")
	}
	out := append([]byte(nil), b...)
	fileStart := w.DataOffset + byteStart
	for i := int64(0); i < 16; i++ {
		out[fileStart+i] ^= 0x3C
	}
	// Recompute CRC over the modified block and patch its ABLK entry.
	newCRC := wav.CRCBlock(out[w.DataOffset+byteStart : w.DataOffset+byteEnd])
	crcOff := ablk.DataOffset + 20 + blockIndex*20 + 12
	binary.LittleEndian.PutUint32(out[crcOff:crcOff+4], newCRC)
	return out, nil
}

// MutateContainer inserts a JUNK chunk immediately before data and fixes the
// RIFF size. PCM essence and metadata payload bytes stay identical.
func MutateContainer(b []byte) ([]byte, error) {
	chunks, _ := wav.WalkChunks(b, 12)
	dc := wav.FindChunk(chunks, "data")
	if dc == nil {
		return nil, fmt.Errorf("mutate container: no data chunk")
	}
	junk := makeChunkBytes("JUNK", make([]byte, 24))
	out := append([]byte(nil), b[:dc.HeaderOffset]...)
	out = append(out, junk...)
	out = append(out, b[dc.HeaderOffset:]...)
	binary.LittleEndian.PutUint32(out[4:8], uint32(len(out)-8))
	return out, nil
}

// MutateMetadata changes one fixed-length INFO/INAM byte in place, so chunk
// layout and sizes are unchanged while the metadata fingerprint changes.
func MutateMetadata(b []byte) ([]byte, error) {
	chunks, _ := wav.WalkChunks(b, 12)
	list := wav.FindChunk(chunks, "LIST")
	if list == nil {
		return nil, fmt.Errorf("mutate metadata: no LIST chunk")
	}
	out := append([]byte(nil), b...)
	p := list.DataOffset
	if string(out[p:p+4]) != "INFO" {
		return nil, fmt.Errorf("mutate metadata: LIST is not INFO")
	}
	// INFO payload: "INFO" then "INAM" + u32 size + NUL-padded value.
	nameOff := p + 4
	if string(out[nameOff:nameOff+4]) != "INAM" {
		return nil, fmt.Errorf("mutate metadata: no INAM tag")
	}
	valOff := nameOff + 8
	if out[valOff] == 0 {
		return nil, fmt.Errorf("mutate metadata: empty INAM value")
	}
	out[valOff]++ // same-length deterministic edit
	return out, nil
}

func makeChunkBytes(id string, payload []byte) []byte {
	b := []byte(id)
	var sz [4]byte
	binary.LittleEndian.PutUint32(sz[:], uint32(len(payload)))
	b = append(b, sz[:]...)
	b = append(b, payload...)
	if len(payload)&1 == 1 {
		b = append(b, 0)
	}
	return b
}
