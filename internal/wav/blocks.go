package wav

import (
	"encoding/binary"
	"hash/crc32"

	"assetcheck/internal/errmodel"
)

// ABLK is a generated RIFF chunk ("ABLK") carrying per-block CRCs and
// keyframe flags. It lets the offline kernel localize damaged PCM regions
// deterministically without relying on an audio codec.
const BlockTableID = "ABLK"

const blockTableVersion = uint16(1)

// Block flags.
const (
	BlockFlagKeyframe uint16 = 1 << iota
)

// BlockEntry is one integrity record.
type BlockEntry struct {
	Index      uint32 `json:"index"`
	StartFrame int64  `json:"start-frame"`
	FrameCount uint32 `json:"frame-count"`
	Flags      uint16 `json:"flags"`
	CRC32      uint32 `json:"crc32"`
}

// Keyframe reports whether the entry is flagged as a random-access point.
func (b BlockEntry) Keyframe() bool { return b.Flags&BlockFlagKeyframe != 0 }

// BlockTable is the decoded ABLK payload.
type BlockTable struct {
	Version     uint16       `json:"version"`
	Flags       uint16       `json:"flags"`
	BlockFrames uint32       `json:"block-frames"`
	Entries     []BlockEntry `json:"entries"`
}

const blockTableHeaderLen = 20
const blockEntryLen = 20

// EncodeBlockTable serializes the table to ABLK chunk payload bytes.
func EncodeBlockTable(t *BlockTable) []byte {
	b := make([]byte, blockTableHeaderLen+len(t.Entries)*blockEntryLen)
	copy(b[0:4], BlockTableID)
	binary.LittleEndian.PutUint16(b[4:6], blockTableVersion)
	binary.LittleEndian.PutUint16(b[6:8], t.Flags)
	binary.LittleEndian.PutUint32(b[8:12], t.BlockFrames)
	binary.LittleEndian.PutUint32(b[12:16], uint32(len(t.Entries)))
	binary.LittleEndian.PutUint32(b[16:20], 0) // reserved
	off := blockTableHeaderLen
	for _, e := range t.Entries {
		binary.LittleEndian.PutUint64(b[off:off+8], uint64(e.StartFrame))
		binary.LittleEndian.PutUint32(b[off+8:off+12], e.FrameCount)
		binary.LittleEndian.PutUint32(b[off+12:off+16], e.CRC32)
		binary.LittleEndian.PutUint16(b[off+16:off+18], e.Flags)
		binary.LittleEndian.PutUint16(b[off+18:off+20], 0)
		off += blockEntryLen
	}
	return b
}

// DecodeBlockTable parses an ABLK payload.
func DecodeBlockTable(p []byte) (*BlockTable, *errmodel.Error) {
	if len(p) < blockTableHeaderLen || id4(p[0:4]) != BlockTableID {
		return nil, errmodel.New(errmodel.ErrIndexSchema, "bad ABLK magic or short header (%d bytes)", len(p))
	}
	t := &BlockTable{
		Version:     binary.LittleEndian.Uint16(p[4:6]),
		Flags:       binary.LittleEndian.Uint16(p[6:8]),
		BlockFrames: binary.LittleEndian.Uint32(p[8:12]),
	}
	n := int(binary.LittleEndian.Uint32(p[12:16]))
	want := blockTableHeaderLen + n*blockEntryLen
	if len(p) < want {
		return nil, errmodel.New(errmodel.ErrIndexSchema,
			"ABLK declares %d entries needing %d bytes, have %d", n, want, len(p))
	}
	t.Entries = make([]BlockEntry, n)
	off := blockTableHeaderLen
	for i := range t.Entries {
		t.Entries[i] = BlockEntry{
			Index:      uint32(i),
			StartFrame: int64(binary.LittleEndian.Uint64(p[off : off+8])),
			FrameCount: binary.LittleEndian.Uint32(p[off+8 : off+12]),
			CRC32:      binary.LittleEndian.Uint32(p[off+12 : off+16]),
			Flags:      binary.LittleEndian.Uint16(p[off+16 : off+18]),
		}
		off += blockEntryLen
	}
	return t, nil
}

// ParseBlockTable extracts and decodes the ABLK chunk if present.
func (w *Wav) ParseBlockTable(data []byte) *BlockTable {
	c := FindChunk(w.Chunks, BlockTableID)
	if c == nil {
		return nil
	}
	t, perr := DecodeBlockTable(chunkPayload(data, c))
	if perr != nil {
		w.Findings = append(w.Findings, perr)
		return nil
	}
	return t
}

// CRCBlock computes the IEEE CRC32 used by ABLK for a PCM block.
func CRCBlock(pcm []byte) uint32 { return crc32.ChecksumIEEE(pcm) }

// BuildBlockTable creates one keyframe-flagged entry per full block plus a
// final partial entry when totalFrames is not block-aligned.
func BuildBlockTable(totalFrames int64, blockFrames int, align int, pcm []byte) *BlockTable {
	t := &BlockTable{BlockFrames: uint32(blockFrames)}
	for start := int64(0); start < totalFrames; start += int64(blockFrames) {
		n := int64(blockFrames)
		if start+n > totalFrames {
			n = totalFrames - start
		}
		byteStart := start * int64(align)
		byteEnd := byteStart + n*int64(align)
		var payload []byte
		if byteEnd <= int64(len(pcm)) {
			payload = pcm[byteStart:byteEnd]
		} else if byteStart < int64(len(pcm)) {
			payload = pcm[byteStart:]
		}
		e := BlockEntry{
			Index:      uint32(len(t.Entries)),
			StartFrame: start,
			FrameCount: uint32(n),
			Flags:      BlockFlagKeyframe,
		}
		if len(payload) == int(n)*align {
			e.CRC32 = CRCBlock(payload)
		}
		t.Entries = append(t.Entries, e)
	}
	return t
}
