package wav

import (
	"encoding/binary"
	"math"

	"rtx/internal/model"
)

// Block is one interleaved multi-channel sample frame. Samples holds
// channels values normalized to signed 64-bit PCM integers; raw byte
// width is available from the header.
type Block struct {
	Index      int
	ByteOffset int64 // raw offset of the block inside the input buffer
	Samples    []int64
}

// ParseResult keeps complete blocks plus recoverable anomalies.
type ParseResult struct {
	Header  *Header
	Blocks  []Block
	Anomaly []model.Anomaly
}

// tailChunk describes an identifiable chunk found after truncated data.
type tailChunk struct {
	id     string
	offset int64
}

// ParseBlocks walks the data region of a validated WAV buffer. A
// truncated final block is reported with its first raw byte offset; all
// preceding complete blocks are retained and parsing continues so that
// identifiable trailing chunks can still be reported.
func ParseBlocks(buf []byte, h *Header, limits model.Limits) (*ParseResult, error) {
	if limits.MaxBlocks <= 0 {
		limits = model.DefaultLimits()
	}
	if len(buf) > limits.MaxBytes && limits.MaxBytes > 0 {
		return nil, model.NewStructuredError(model.StageWAV, model.ReasonLimit,
			0, int64(len(buf)), "input exceeds configured byte limit",
			"reject input", nil)
	}
	res := &ParseResult{Header: h}
	align := int64(h.BlockAlign)
	dataEnd := h.DataOffset + int64(h.DataLength)
	physicalEnd := int64(len(buf))
	if dataEnd > physicalEnd {
		dataEnd = physicalEnd
	}
	pos := h.DataOffset
	idx := 0
	for pos+align <= dataEnd {
		if idx >= limits.MaxBlocks {
			return nil, model.NewStructuredError(model.StageWAV, model.ReasonLimit,
				pos, int64(idx), "block count exceeds configured limit",
				"reject input", nil)
		}
		res.Blocks = append(res.Blocks, readBlock(buf[pos:pos+align], h, idx, pos))
		pos += align
		idx++
	}
	if pos < dataEnd {
		// Partial block: exact first raw byte offset, no fabricated samples.
		res.Anomaly = append(res.Anomaly, model.Anomaly{
			Stage: model.StageWAV, Reason: model.ReasonTruncatedBlock,
			Offset: pos, RawValue: dataEnd - pos,
			Message:  "partial sample block: " + itoa(dataEnd-pos) + " of " + itoa(align) + " bytes",
			Recovery: "drop partial block; keep preceding complete blocks",
		})
	}
	if h.DeclaredTrunc && pos == dataEnd && dataEnd-h.DataOffset > 0 &&
		(dataEnd-h.DataOffset)%align == 0 {
		// File ended exactly on a block boundary but before the declared
		// length: still a truncation event at the physical end.
		res.Anomaly = append(res.Anomaly, model.Anomaly{
			Stage: model.StageWAV, Reason: model.ReasonTruncatedBlock,
			Offset: dataEnd, RawValue: 0,
			Message:  "data chunk ends before its declared length",
			Recovery: "keep complete blocks; stop at physical end",
		})
	}
	// Continue inspection beyond the declared data for recognizable boxes.
	scanStart := h.DataOffset + int64(h.DataLength)
	for _, c := range scanChunks(buf, scanStart) {
		res.Anomaly = append(res.Anomaly, model.Anomaly{
			Stage: model.StageWAV, Reason: model.ReasonTruncatedBlock,
			Offset: c.offset, RawValue: 0,
			Message:  "trailing chunk " + c.id + " found after truncated data",
			Recovery: "ignore trailing chunk; data region ends at truncation",
		})
	}
	return res, nil
}

func readBlock(raw []byte, h *Header, idx int, offset int64) Block {
	b := Block{Index: idx, ByteOffset: offset, Samples: make([]int64, h.Channels)}
	bytesPer := int(h.BitsPerSample / 8)
	for ch := uint16(0); ch < h.Channels; ch++ {
		p := raw[int(ch)*bytesPer : int(ch)*bytesPer+bytesPer]
		switch h.BitsPerSample {
		case 16:
			b.Samples[ch] = int64(int16(binary.LittleEndian.Uint16(p)))
		case 24:
			v := uint32(p[0]) | uint32(p[1])<<8 | uint32(p[2])<<16
			if v&0x800000 != 0 {
				v |= 0xFF000000
			}
			b.Samples[ch] = int64(int32(v))
		case 32:
			b.Samples[ch] = int64(int32(binary.LittleEndian.Uint32(p)))
		}
	}
	return b
}

// scanChunks looks for well-formed fourcc/size pairs at or after start,
// tolerating the 0..1 padding byte that follows an odd-sized data chunk.
func scanChunks(buf []byte, start int64) []tailChunk {
	var out []tailChunk
	for _, p := range []int64{start, start + 1} {
		if p+8 > int64(len(buf)) {
			continue
		}
		id := buf[p : p+4]
		size := int64(binary.LittleEndian.Uint32(buf[p+4 : p+8]))
		if !printableFourCC(id) || size <= 0 || p+8+size > int64(len(buf)) {
			continue
		}
		out = append(out, tailChunk{id: string(id), offset: p})
	}
	return out
}

func printableFourCC(b []byte) bool {
	for _, c := range b {
		if !(c >= 'a' && c <= 'z') && !(c >= 'A' && c <= 'Z') && !(c >= '0' && c <= '9') && c != ' ' {
			return false
		}
	}
	return true
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// PeakLevel returns the maximum absolute normalized amplitude across all
// retained blocks and channels, scaled to [0,1].
func PeakLevel(blocks []Block, bits uint16) float64 {
	var maxAbs int64
	for _, b := range blocks {
		for _, s := range b.Samples {
			if s < 0 {
				s = -s
			}
			if s > maxAbs {
				maxAbs = s
			}
		}
	}
	var fullScale float64
	switch bits {
	case 16:
		fullScale = 32768
	case 24:
		fullScale = 1 << 23
	case 32:
		fullScale = math.MaxInt32 + 1
	default:
		fullScale = 1
	}
	return float64(maxAbs) / fullScale
}
