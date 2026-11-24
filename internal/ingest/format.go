package ingest

import (
	"crypto/sha256"
	"encoding/binary"

	"mediarc/internal/model"
)

var colorPrimariesCodes = map[string]byte{
	"": 0, "unknown": 0, "bt709": 1, "bt601": 2, "bt2020": 3,
}
var colorTransferCodes = map[string]byte{
	"": 0, "unknown": 0, "bt709": 1, "smpte2084": 2, "hlg": 3,
}
var colorMatrixCodes = map[string]byte{
	"": 0, "unknown": 0, "bt709": 1, "bt601": 2, "bt2020nc": 3,
}
var colorRangeCodes = map[string]byte{"": 0, "unknown": 0, "limited": 1, "full": 2}

func codeOrZero(m map[string]byte, s string) byte { return m[s] }

func sha256Sum(b []byte) [32]byte { return sha256.Sum256(b) }

func decodeColor(b []byte) model.ColorMetadata {
	primaries := []string{"unknown", "bt709", "bt601", "bt2020"}
	transfers := []string{"unknown", "bt709", "smpte2084", "hlg"}
	matrices := []string{"unknown", "bt709", "bt601", "bt2020nc"}
	ranges := []string{"unknown", "limited", "full"}
	at := func(table []string, code byte) string {
		if int(code) < len(table) {
			return table[code]
		}
		return "unknown"
	}
	return model.ColorMetadata{
		Primaries: at(primaries, b[0]),
		Transfer:  at(transfers, b[1]),
		Matrix:    at(matrices, b[2]),
		Range:     at(ranges, b[3]),
	}
}

func encodeColor(c model.ColorMetadata) [4]byte {
	return [4]byte{
		codeOrZero(colorPrimariesCodes, c.Primaries),
		codeOrZero(colorTransferCodes, c.Transfer),
		codeOrZero(colorMatrixCodes, c.Matrix),
		codeOrZero(colorRangeCodes, c.Range),
	}
}

type preparedFrame struct {
	logicalIndex int
	record       []byte
	badSHA       bool
	truncated    bool
}

func encodeFrame(material string, index uint32, duration uint32, width, height uint16, color model.ColorMetadata, payload []byte, key bool) []byte {
	env := make([]byte, FrameHeaderSize+len(payload))
	var materialBytes [16]byte
	copy(materialBytes[:], material)
	copy(env[0:16], materialBytes[:])
	binary.BigEndian.PutUint32(env[16:20], index)
	binary.BigEndian.PutUint64(env[20:28], uint64(index)*uint64(duration))
	binary.BigEndian.PutUint32(env[28:32], duration)
	binary.BigEndian.PutUint16(env[32:34], width)
	binary.BigEndian.PutUint16(env[34:36], height)
	var flags uint16
	if key {
		flags = 1
	}
	binary.BigEndian.PutUint16(env[36:38], flags)
	binary.BigEndian.PutUint32(env[38:42], uint32(len(payload)))
	sum := sha256Sum(payload)
	copy(env[42:74], sum[:])
	cc := encodeColor(color)
	copy(env[74:78], cc[:])
	copy(env[FrameHeaderSize:], payload)

	out := make([]byte, 8+len(env))
	copy(out[0:4], FrameMagic)
	binary.BigEndian.PutUint32(out[4:8], uint32(len(env)))
	copy(out[8:], env)
	return out
}
