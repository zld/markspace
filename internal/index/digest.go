// Package index builds reproducible content and container digests and
// persists them together with block checkpoints. Digests are pure
// functions of normalized inputs, so identical content always hashes
// identically regardless of read pacing or process restarts.
package index

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"math"

	"continuity/internal/wav"
)

// ContentInput is everything that defines one block's content digest:
// the normalized little-endian PCM bytes, the canonical channel layout
// and the inclusive sample range on the global timeline.
type ContentInput struct {
	PCM         []byte
	Layout      wav.Layout
	StartSample int64
	EndSample   int64
}

// canonicalJSON renders the non-PCM inputs in a fixed key order so the
// digest does not depend on map iteration or struct padding.
func (c ContentInput) canonicalJSON() []byte {
	type canon struct {
		StartSample int64    `json:"start_sample"`
		EndSample   int64    `json:"end_sample"`
		Channels    uint16   `json:"channels"`
		BitDepth    uint16   `json:"bit_depth"`
		SampleRate  uint32   `json:"sample_rate"`
		Order       []string `json:"channel_order"`
		PCMLength   int      `json:"pcm_length"`
	}
	b, _ := json.Marshal(canon{
		StartSample: c.StartSample,
		EndSample:   c.EndSample,
		Channels:    c.Layout.Channels,
		BitDepth:    c.Layout.BitDepth,
		SampleRate:  c.Layout.SampleRate,
		Order:       c.Layout.Order,
		PCMLength:   len(c.PCM),
	})
	return b
}

// ContentDigest computes the SHA-256 content digest of one block. The
// hash covers the PCM bytes, the layout and the sample range.
func ContentDigest(in ContentInput) string {
	h := sha256.New()
	h.Write([]byte("continuity-pcm-v1\n"))
	h.Write(in.canonicalJSON())
	h.Write([]byte{'\n'})
	h.Write(in.PCM)
	return hex.EncodeToString(h.Sum(nil))
}

// LoudnessStats holds deterministic per-block audio statistics derived
// from 16-bit interleaved PCM. Values are reproducible bit-for-bit.
type LoudnessStats struct {
	PeakDBFS float64 `json:"peak_dbfs"`
	// PeakAmplitude is the largest absolute integer sample observed.
	PeakAmplitude int `json:"peak_amplitude"`
}

// PeakLoudness computes peak loudness in dBFS for interleaved 16-bit
// PCM. Silence is reported as -96 dBFS (16-bit noise floor convention).
func PeakLoudness(pcm16 []byte) LoudnessStats {
	peak := 0
	for i := 0; i+1 < len(pcm16); i += 2 {
		// Decode little-endian unsigned first, then reinterpret as signed.
		u := binary.LittleEndian.Uint16(pcm16[i : i+2])
		v := int(int16(u))
		if v < 0 {
			v = -v
		}
		if v > peak {
			peak = v
		}
	}
	if peak == 0 {
		return LoudnessStats{PeakDBFS: -96, PeakAmplitude: 0}
	}
	// Clamp the amplitude to the 16-bit positive scale so dBFS can never
	// exceed zero, even for the -32768 code.
	if peak > 32768 {
		peak = 32768
	}
	db := 20 * math.Log10(float64(peak)/32768)
	return LoudnessStats{PeakDBFS: round2(db), PeakAmplitude: peak}
}

func round2(v float64) float64 {
	return math.Round(v*100) / 100
}

// ShortHash returns the first 12 hex chars of a digest for reports.
func ShortHash(digest string) string {
	if len(digest) > 12 {
		return digest[:12]
	}
	return digest
}
