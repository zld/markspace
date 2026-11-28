package render

import (
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"math"
)

// PixelStats summarises a rendered frame set.
type PixelStats struct {
	// Frames is the frame count.
	Frames int
	// MeanR, MeanG and MeanB are channel means over every pixel.
	MeanR, MeanG, MeanB float64
	// MeanLuma is the Rec.601 luma mean.
	MeanLuma float64
	// MinLuma and MaxLuma bound the per-pixel luma.
	MinLuma, MaxLuma float64
	// RecoveredFrames counts frames standing in for missing media.
	RecoveredFrames int
}

// AudioStats is the exported audio measurement.
type AudioStats struct {
	Chunks int
	// PeakDBFS is the highest absolute sample level.
	PeakDBFS float64
	// RMSDBFS is the root-mean-square level.
	RMSDBFS float64
	// RecoveredChunks counts silence chunks inserted for lost packets.
	RecoveredChunks int
	// SilenceChunks counts chunks whose every sample is zero.
	SilenceChunks int
}

// Metrics is the full measurement of a render pass.
type Metrics struct {
	Pixels PixelStats
	Audio  AudioStats
	// FrameHash is a combined hash over every frame, in render order.
	FrameHash uint64
	// AudioHash is a combined hash over every audio chunk, in render order.
	AudioHash uint64
	// FrameBytes and AudioBytes are the rendered payload sizes.
	FrameBytes int64
	AudioBytes int64
}

// Measure computes metrics for the rendered frames and audio chunks.
func Measure(frames []Frame, chunks []AudioChunk) Metrics {
	var metrics Metrics
	var lumaSum, pixelCount float64
	var sumR, sumG, sumB float64
	minLuma := math.Inf(1)
	maxLuma := math.Inf(-1)
	frameHasher := fnv.New64a()
	for _, frame := range frames {
		if frame.Recovered {
			metrics.Pixels.RecoveredFrames++
		}
		metrics.FrameBytes += int64(len(frame.Pixels) * 3)
		writeUint64(frameHasher, frame.Hash)
		for _, pixel := range frame.Pixels {
			r, g, b := float64(pixel.R), float64(pixel.G), float64(pixel.B)
			sumR += r
			sumG += g
			sumB += b
			luma := 0.299*r + 0.587*g + 0.114*b
			lumaSum += luma
			pixelCount++
			if luma < minLuma {
				minLuma = luma
			}
			if luma > maxLuma {
				maxLuma = luma
			}
		}
	}
	metrics.FrameHash = frameHasher.Sum64()
	metrics.Pixels.Frames = len(frames)
	if pixelCount > 0 {
		metrics.Pixels.MeanR = sumR / pixelCount
		metrics.Pixels.MeanG = sumG / pixelCount
		metrics.Pixels.MeanB = sumB / pixelCount
		metrics.Pixels.MeanLuma = lumaSum / pixelCount
		metrics.Pixels.MinLuma = minLuma
		metrics.Pixels.MaxLuma = maxLuma
	}

	audioHasher := fnv.New64a()
	var peak float64
	var sumSquares float64
	var samples float64
	silentChunks := 0
	for _, chunk := range chunks {
		if chunk.Recovered {
			metrics.Audio.RecoveredChunks++
		}
		metrics.AudioBytes += int64(len(chunk.Data))
		writeUint64(audioHasher, chunk.Hash)
		allZero := true
		for i := 0; i+1 < len(chunk.Data); i += 2 {
			sample := int16(binary.LittleEndian.Uint16(chunk.Data[i:]))
			value := float64(sample) / 32768.0
			if value != 0 {
				allZero = false
			}
			if abs := math.Abs(value); abs > peak {
				peak = abs
			}
			sumSquares += value * value
			samples++
		}
		if allZero && len(chunk.Data) > 0 {
			silentChunks++
		}
	}
	metrics.AudioHash = audioHasher.Sum64()
	metrics.Audio.Chunks = len(chunks)
	metrics.Audio.SilenceChunks = silentChunks
	metrics.Audio.PeakDBFS = dbfs(peak)
	if samples > 0 {
		metrics.Audio.RMSDBFS = dbfs(math.Sqrt(sumSquares / samples))
	}
	return metrics
}

// dbfs converts a linear amplitude to dBFS, flooring at the 16-bit noise floor.
func dbfs(amplitude float64) float64 {
	if amplitude <= 0 {
		return -96
	}
	value := 20 * math.Log10(amplitude)
	if value < -96 {
		return -96
	}
	return value
}

func writeUint64(h interface{ Write([]byte) (int, error) }, value uint64) {
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], value)
	_, _ = h.Write(buf[:])
}

// SegmentSummary is a deterministic, human-readable description of a render
// pass, used as the review checklist written to the output directory.
type SegmentSummary struct {
	FrameCount  int
	AudioCount  int
	FirstHash   uint64
	LastHash    uint64
	MeanLuma    float64
	PeakDBFS    float64
	FrameHash   uint64
	AudioHash   uint64
	RecoveredNS int64
}

// Summarise condenses a render pass for the acceptance report.
func Summarise(frames []Frame, chunks []AudioChunk) SegmentSummary {
	metrics := Measure(frames, chunks)
	summary := SegmentSummary{
		FrameCount: len(frames),
		AudioCount: len(chunks),
		MeanLuma:   metrics.Pixels.MeanLuma,
		PeakDBFS:   metrics.Audio.PeakDBFS,
		FrameHash:  metrics.FrameHash,
		AudioHash:  metrics.AudioHash,
	}
	if len(frames) > 0 {
		summary.FirstHash = frames[0].Hash
		summary.LastHash = frames[len(frames)-1].Hash
	}
	for _, chunk := range chunks {
		if chunk.Recovered {
			summary.RecoveredNS += chunk.DurationNS
		}
	}
	return summary
}

// FormatLoudness renders the peak level for terminal output.
func FormatLoudness(peakDBFS float64) string {
	return fmt.Sprintf("%.2f dBFS", peakDBFS)
}
