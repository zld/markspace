package audio

import (
	"encoding/binary"
	"math"

	"mediad/internal/mederr"
)

// EncodeS16LE interleaves planar float PCM as signed 16-bit little-endian.
func EncodeS16LE(b *Buffer) ([]byte, error) {
	if err := b.Validate(); err != nil {
		return nil, err
	}
	frames := b.Frames()
	ch := b.Layout.Channels
	out := make([]byte, frames*ch*2)
	pos := 0
	for i := 0; i < frames; i++ {
		for c := 0; c < ch; c++ {
			v := math.Max(-1, math.Min(1, b.Planar[c][i]))
			q := int16(math.Round(v * 32767))
			binary.LittleEndian.PutUint16(out[pos:pos+2], uint16(q))
			pos += 2
		}
	}
	return out, nil
}

// DecodeS16LE converts interleaved signed 16-bit little-endian PCM to planar.
func DecodeS16LE(pcm []byte, rate int, layout Layout) (*Buffer, error) {
	if rate <= 0 || layout.Channels <= 0 {
		return nil, mederr.New(mederr.CatAudio, "pcm", -1, -1, false, "invalid PCM format")
	}
	frameBytes := layout.Channels * 2
	if frameBytes <= 0 || len(pcm)%frameBytes != 0 {
		return nil, mederr.New(mederr.CatParse, "pcm", -1, -1, true,
			"PCM length %d not aligned to %d-byte frames", len(pcm), frameBytes)
	}
	frames := len(pcm) / frameBytes
	b, err := NewBuffer(rate, layout, frames)
	if err != nil {
		return nil, err
	}
	for i := 0; i < frames; i++ {
		for c := 0; c < layout.Channels; c++ {
			off := (i*layout.Channels + c) * 2
			b.Planar[c][i] = float64(int16(binary.LittleEndian.Uint16(pcm[off:off+2]))) / 32768.0
		}
	}
	return b, nil
}
