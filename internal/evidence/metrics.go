// Package evidence computes the off-screen verification artifacts: content
// hashes, frame hashes, synthetic pixel statistics and an audio loudness peak
// in dBFS. Because the kernel never decodes compressed media, the pixel and
// loudness statistics are deterministic proxies derived from the preserved
// compressed sample bytes: identical bytes therefore produce identical
// statistics, which is exactly the regression property delivery verification
// needs. Every routine is integer- or IEEE-deterministic and allocates no
// goroutine-global state.
package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"math"

	"mediacheck/internal/demux"
)

// Metrics aggregates everything the acceptance report prints per asset.
type Metrics struct {
	// InputSHA256 hashes the complete source container.
	InputSHA256 string
	// OutputSHA256 hashes the complete remuxed container.
	OutputSHA256 string
	// FrameHash chains every video sample hash into one digest.
	FrameHash string
	// PixelMean / PixelStddev are the byte-domain proxy statistics over all
	// video sample payloads.
	PixelMean   float64
	PixelStddev float64
	// AudioPeakDBFS is the loudest full-scale-normalized PCM proxy value.
	AudioPeakDBFS float64
	// PerFrameHash exposes the individual video sample digests.
	PerFrameHash []string
}

// HashBytes returns the hex SHA-256 of a byte slice.
func HashBytes(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// FrameHashes returns one hex digest per video sample, preserving order.
func FrameHashes(tr *demux.Track, read func(*demux.Track, int) ([]byte, error)) ([]string, error) {
	out := make([]string, 0, len(tr.Samples))
	for i := range tr.Samples {
		payload, err := read(tr, i)
		if err != nil {
			return nil, err
		}
		h := sha256.Sum256(payload)
		out = append(out, hex.EncodeToString(h[:]))
	}
	return out, nil
}

// ChainFrameHashes folds ordered per-frame digests into a single digest so
// any reordering or replacement changes the result.
func ChainFrameHashes(perFrame []string) string {
	h := sha256.New()
	for _, d := range perFrame {
		h.Write([]byte(d))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// PixelStats treats every payload byte as an 8-bit luminance proxy and
// computes its population mean and standard deviation. The two passes keep
// the result numerically stable regardless of sample ordering.
func PixelStats(values []byte) (mean, stddev float64) {
	if len(values) == 0 {
		return 0, 0
	}
	var sum float64
	for _, v := range values {
		sum += float64(v)
	}
	mean = sum / float64(len(values))
	var sq float64
	for _, v := range values {
		d := float64(v) - mean
		sq += d * d
	}
	stddev = math.Sqrt(sq / float64(len(values)))
	return mean, stddev
}

// AudioPeakDBFS converts the loudest audio sample byte to a dBFS value
// relative to unsigned 8-bit full-scale 255 (PCM proxy). A silent track
// returns negative infinity, which callers render as the sentinel "-inf".
func AudioPeakDBFS(values []byte) float64 {
	peak := 0
	for _, v := range values {
		if int(v) > peak {
			peak = int(v)
		}
	}
	if peak == 0 {
		return math.Inf(-1)
	}
	return 20 * math.Log10(float64(peak)/255.0)
}

// Collect runs every metric for one parsed source/output pair. readSource and
// readOutput fetch sample bytes from the two containers by track and index.
func Collect(c *demux.Container, inputBytes, outputBytes []byte,
	readSource, readOutput func(*demux.Track, int) ([]byte, error)) (Metrics, error) {

	m := Metrics{
		InputSHA256:  HashBytes(inputBytes),
		OutputSHA256: HashBytes(outputBytes),
	}

	var videoBytes []byte
	var audioBytes []byte
	var perFrame []string

	for _, tr := range c.Tracks {
		for i := range tr.Samples {
			payload, err := readSource(tr, i)
			if err != nil {
				return m, err
			}
			switch tr.Kind {
			case demux.KindVideo:
				videoBytes = append(videoBytes, payload...)
				h := sha256.Sum256(payload)
				perFrame = append(perFrame, hex.EncodeToString(h[:]))
			case demux.KindAudio:
				audioBytes = append(audioBytes, payload...)
			}
		}
	}
	m.PerFrameHash = perFrame
	m.FrameHash = ChainFrameHashes(perFrame)
	m.PixelMean, m.PixelStddev = PixelStats(videoBytes)
	m.AudioPeakDBFS = AudioPeakDBFS(audioBytes)
	return m, nil
}
