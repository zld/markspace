package wav

import (
	"encoding/binary"
	"io"
	"math"

	"continuity/internal/media"
)

// WritePCM writes interleaved little-endian PCM samples as a minimal
// RIFF/WAVE file. pcm length must be a whole number of frames.
func WritePCM(w io.Writer, sampleRate uint32, channels, bits uint16, pcm []byte) error {
	if channels == 0 {
		return media.NewError(media.CatLayout, "wav write: zero channels")
	}
	blockAlign := channels * bits / 8
	if len(pcm)%int(blockAlign) != 0 {
		return media.NewError(media.CatInvalidInput, "wav write: partial trailing frame")
	}
	var hdr [44]byte
	copy(hdr[0:4], "RIFF")
	binary.LittleEndian.PutUint32(hdr[4:8], uint32(36+len(pcm)))
	copy(hdr[8:12], "WAVE")
	copy(hdr[12:16], "fmt ")
	binary.LittleEndian.PutUint32(hdr[16:20], 16)
	binary.LittleEndian.PutUint16(hdr[20:22], 1) // PCM
	binary.LittleEndian.PutUint16(hdr[22:24], channels)
	binary.LittleEndian.PutUint32(hdr[24:28], sampleRate)
	binary.LittleEndian.PutUint32(hdr[28:32], sampleRate*uint32(blockAlign))
	binary.LittleEndian.PutUint16(hdr[32:34], blockAlign)
	binary.LittleEndian.PutUint16(hdr[34:36], bits)
	copy(hdr[36:40], "data")
	binary.LittleEndian.PutUint32(hdr[40:44], uint32(len(pcm)))
	if _, err := w.Write(hdr[:]); err != nil {
		return media.WrapError(media.CatIO, "wav write: header", err)
	}
	if _, err := w.Write(pcm); err != nil {
		return media.WrapError(media.CatIO, "wav write: pcm", err)
	}
	return nil
}

// EncodeStereo16 converts interleaved float64 samples in [-1,1] into
// 16-bit little-endian PCM bytes (L,R,L,R,...).
func EncodeStereo16(samples []float64) []byte {
	out := make([]byte, len(samples)*2)
	for i, s := range samples {
		v := int(math.Round(s * 32767))
		if v > 32767 {
			v = 32767
		} else if v < -32768 {
			v = -32768
		}
		binary.LittleEndian.PutUint16(out[i*2:], uint16(v))
	}
	return out
}
