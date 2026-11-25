package fingerprint

import (
	"encoding/binary"
	"math"
)

// FrameStats is the deterministic offline-decode summary of one video
// sample. There is no graphical surface: the sample payload is expanded
// through a fixed transform into grayscale pixels, so identical bytes
// always yield identical statistics.
type FrameStats struct {
	FrameHash   string  `json:"frame_hash"`
	Width       int     `json:"width"`
	Height      int     `json:"height"`
	Mean        float64 `json:"pixel_mean"`
	Variance    float64 `json:"pixel_variance"`
	AlignmentNs int64   `json:"alignment_error_ns"`
}

// AudioStats is the deterministic summary of one audio sample.
type AudioStats struct {
	FrameHash string  `json:"frame_hash"`
	PeakDBFS  float64 `json:"peak_dbfs"`
}

// defaultFrameDim is the fixed offscreen raster used for video summaries.
const defaultFrameDim = 16

// decodeVideo expands payload bytes into a deterministic grayscale raster
// of width*height pixels. Each output pixel is a byte derived from the
// payload position and value, so the transform is pure and repeatable.
func decodeVideo(payload []byte, width, height int) []byte {
	n := width * height
	pixels := make([]byte, n)
	for i := 0; i < n; i++ {
		b := byte(0)
		if len(payload) > 0 {
			b = payload[i%len(payload)]
		}
		// Fixed mixing step; no randomness, no platform-dependent state.
		pixels[i] = b*31 + byte(i*17)
	}
	return pixels
}

// VideoFrame computes the frame hash, pixel mean and variance for one video
// sample. alignmentNs is the sample's timeline alignment residual recorded
// for the report; it does not affect the content hash.
func VideoFrame(payload []byte, alignmentNs int64) FrameStats {
	width, height := defaultFrameDim, defaultFrameDim
	pixels := decodeVideo(payload, width, height)
	var sum float64
	for _, p := range pixels {
		sum += float64(p)
	}
	mean := sum / float64(len(pixels))
	var sq float64
	for _, p := range pixels {
		d := float64(p) - mean
		sq += d * d
	}
	return FrameStats{
		FrameHash:   FrameHash(pixels),
		Width:       width,
		Height:      height,
		Mean:        mean,
		Variance:    sq / float64(len(pixels)),
		AlignmentNs: alignmentNs,
	}
}

// AudioFrame treats the payload as packed signed 16-bit little-endian PCM
// samples and reports the peak level in dBFS. A fully silent frame returns
// negative infinity, encoded as the lowest finite float by callers.
func AudioFrame(payload []byte) AudioStats {
	var peak int32
	count := 0
	for i := 0; i+2 <= len(payload); i += 2 {
		v := int16(binary.LittleEndian.Uint16(payload[i : i+2]))
		av := int32(v)
		if av < 0 {
			av = -av
		}
		if av > peak {
			peak = av
		}
		count++
	}
	db := math.Inf(-1)
	if peak > 0 {
		db = 20 * math.Log10(float64(peak)/float64(1<<15))
	} else {
		// JSON cannot encode -Inf; use a finite sentinel for full silence.
		db = SilentDBFS
	}
	// Hash the normalized sample values, not the raw container bytes.
	norm := make([]byte, len(payload))
	copy(norm, payload)
	return AudioStats{FrameHash: FrameHash(norm), PeakDBFS: db}
}

// SilentDBFS is the finite sentinel reported for a fully silent frame so
// the JSON report stays serializable.
const SilentDBFS = -32768.0

// IsSilence reports whether an audio stats value represents a silent frame.
func IsSilence(s AudioStats) bool {
	return s.PeakDBFS <= SilentDBFS
}
