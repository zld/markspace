package wav

import (
	"crypto/sha256"
	"encoding/hex"
	"hash"
)

func hexHash(h hash.Hash) string { return "sha256:" + hex.EncodeToString(h.Sum(nil)) }

// ContentFingerprint hashes the framed PCM essence only. Replacing samples
// at the same path changes it even when container and metadata are untouched.
func ContentFingerprint(w *Wav) string {
	h := sha256.New()
	h.Write(w.FramedPCM)
	return hexHash(h)
}

// StructureFingerprint hashes container structure: ordered chunk ids and
// declared sizes plus the exact fmt payload. It deliberately excludes chunk
// offsets (derivable) and metadata payload bytes.
func StructureFingerprint(data []byte, w *Wav) string {
	h := sha256.New()
	fmtChunk := FindChunk(w.Chunks, "fmt ")
	var fmtPayload []byte
	if fmtChunk != nil {
		fmtPayload = chunkPayload(data, fmtChunk)
	}
	lenBuf := make([]byte, 8)
	for _, c := range w.Chunks {
		h.Write([]byte(c.ID))
		writeU64(lenBuf, uint64(c.Size))
		h.Write(lenBuf)
		if c.ID == "fmt " {
			h.Write(fmtPayload)
		}
	}
	return hexHash(h)
}

// MetadataFingerprint hashes metadata-bearing chunk payloads (LIST/INFO and
// broadcast-audio extension). Offsets are excluded so pure container moves
// do not look like metadata edits.
func MetadataFingerprint(data []byte, w *Wav) string {
	h := sha256.New()
	lenBuf := make([]byte, 8)
	for _, c := range w.Chunks {
		if c.ID != "LIST" && c.ID != "bext" {
			continue
		}
		p := chunkPayload(data, &c)
		h.Write([]byte(c.ID))
		writeU64(lenBuf, uint64(len(p)))
		h.Write(lenBuf)
		h.Write(p)
	}
	return hexHash(h)
}

func writeU64(buf []byte, v uint64) {
	buf[0] = byte(v)
	buf[1] = byte(v >> 8)
	buf[2] = byte(v >> 16)
	buf[3] = byte(v >> 24)
	buf[4] = byte(v >> 32)
	buf[5] = byte(v >> 40)
	buf[6] = byte(v >> 48)
	buf[7] = byte(v >> 56)
}
