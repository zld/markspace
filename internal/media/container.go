// Package media writes and reads the released audio container.
//
// The container is an ISO base media file carrying 16-bit little-endian
// linear PCM ("ipcm" + "pcmC") so that the released bytes are a lossless,
// inspectable record of exactly what the audio stage produced.  The box
// writer, the sample tables and the index reader all come from go-mp4; the
// index is re-read from the finished bytes and cross-checked against the
// audio report before a manifest may reference it.
package media

import (
	"crypto/sha256"
	"encoding/hex"

	"example.com/assetpack/internal/fault"
)

// Container constants.
const (
	// ChunkFrames is the number of audio frames per media sample.
	ChunkFrames   = 4800 // 100 ms at 48 kHz
	bitsPerSample = 16
)

// Index is the reusable description of a written container.
type Index struct {
	Timescale   uint32 `json:"timescale"`
	Duration    uint64 `json:"duration"`
	Rate        int    `json:"rate"`
	Channels    int    `json:"channels"`
	Frames      int64  `json:"frames"`
	Samples     int    `json:"samples"`
	Chunks      int    `json:"chunks"`
	SampleBytes int    `json:"bytes_per_frame"`
	Digest      string `json:"digest"`
	Size        int64  `json:"size"`
}

// Validate checks the index against an audio report and container digest.
func (ix Index) Validate(frames int64, rate int, data []byte) error {
	if ix.Rate != rate {
		return fault.New(fault.StageAudio, fault.KindInvalidArg,
			"container rate %d does not match target %d", ix.Rate, rate)
	}
	if ix.Frames != frames {
		return fault.New(fault.StageAudio, fault.KindInvalidArg,
			"container holds %d frames, audio report says %d", ix.Frames, frames)
	}
	sum := sha256.Sum256(data)
	if ix.Digest != "" && ix.Digest != hex.EncodeToString(sum[:]) {
		return fault.New(fault.StageAudio, fault.KindDigest, "container digest changed after indexing")
	}
	return nil
}
