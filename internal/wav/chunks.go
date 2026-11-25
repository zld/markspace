package wav

import (
	"encoding/binary"

	"assetcheck/internal/errmodel"
)

// ChunkInfo describes one RIFF chunk's boundaries inside a WAV file.
type ChunkInfo struct {
	ID           string `json:"id"`
	Index        int    `json:"index"`
	HeaderOffset int64  `json:"header-offset"`
	DataOffset   int64  `json:"data-offset"`
	Size         int64  `json:"declared-size"`
	DeclaredEnd  int64  `json:"declared-end"`
	Available    int64  `json:"available-bytes"`
	OutOfBounds  bool   `json:"out-of-bounds"`
}

// id4 decodes a four-character RIFF identifier.
func id4(b []byte) string {
	return string([]byte{b[0], b[1], b[2], b[3]})
}

// WalkChunks enumerates RIFF chunks starting at start (usually 12), honoring
// the even-byte padding rule. Out-of-bounds or truncated chunk headers are
// reported as findings rather than panics; enumeration stops at the first
// chunk whose declared body is past EOF because subsequent boundaries are
// then unknowable.
func WalkChunks(data []byte, start int64) ([]ChunkInfo, []*errmodel.Error) {
	var (
		chunks   []ChunkInfo
		findings []*errmodel.Error
	)
	fileLen := int64(len(data))
	off := start
	idx := 0
	for {
		if off == fileLen {
			break
		}
		if off > fileLen {
			findings = append(findings, errmodel.New(
				errmodel.ErrChunkOutOfBounds, "chunk walk offset %d past EOF %d", off, fileLen).
				AtOffset(off))
			break
		}
		if fileLen-off < 8 {
			findings = append(findings, errmodel.New(
				errmodel.ErrChunkTruncated,
				"truncated chunk header at offset %d: %d byte(s) left, need 8",
				off, fileLen-off).AtOffset(off))
			break
		}
		id := id4(data[off : off+4])
		size := int64(binary.LittleEndian.Uint32(data[off+4 : off+8]))
		dataOff := off + 8
		end := dataOff + size
		ci := ChunkInfo{
			ID: id, Index: idx, HeaderOffset: off, DataOffset: dataOff,
			Size: size, DeclaredEnd: end,
		}
		if end > fileLen {
			ci.OutOfBounds = true
			if dataOff <= fileLen {
				ci.Available = fileLen - dataOff
			}
			chunks = append(chunks, ci)
			findings = append(findings, errmodel.New(
				errmodel.ErrChunkOutOfBounds,
				"chunk %q at offset %d declares %d body bytes ending at %d, file ends at %d (%d byte(s) short)",
				id, off, size, end, fileLen, end-fileLen).AtOffset(off))
			break
		}
		ci.Available = size
		chunks = append(chunks, ci)
		idx++
		// RIFF chunks are padded to an even byte boundary.
		off = end + (size & 1)
	}
	return chunks, findings
}

// FindChunk returns the first chunk with the given ID.
func FindChunk(chunks []ChunkInfo, id string) *ChunkInfo {
	for i := range chunks {
		if chunks[i].ID == id {
			return &chunks[i]
		}
	}
	return nil
}

// chunkPayload returns the exact declared payload slice for an in-bounds chunk.
func chunkPayload(data []byte, c *ChunkInfo) []byte {
	end := c.DataOffset + c.Size
	if end > int64(len(data)) {
		end = int64(len(data))
	}
	if c.DataOffset > int64(len(data)) {
		return nil
	}
	return data[c.DataOffset:end]
}
