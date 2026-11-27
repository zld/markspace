package sample

import (
	"encoding/binary"
	"math"
)

// Deterministic synthetic media parameters. All "decoded" content is
// computed procedurally from the seed; nothing is read from disk or net.
const (
	VideoWidth         = 64
	VideoHeight        = 48
	VideoFrames        = 30
	VideoTimescale     = 90000
	VideoFrameDuration = 3000 // 30 fps at 90 kHz

	AudioRate    = 48000
	AudioSamples = 4800 // 100 ms
)

// splitmix64 is a platform-independent mixing function.
func splitmix64(x uint64) uint64 {
	x += 0x9E3779B97F4A7C15
	x = (x ^ (x >> 30)) * 0xBF58476D1CE4E5B9
	x = (x ^ (x >> 27)) * 0x94D049BB133111EB
	return x ^ (x >> 31)
}

// Frame renders frame i as raw RGB24, deterministically from the seed.
func Frame(seed int64, i int) []byte {
	out := make([]byte, VideoWidth*VideoHeight*3)
	base := uint64(seed)*0x100000001B3 + uint64(i)*0x9E3779B1
	for y := 0; y < VideoHeight; y++ {
		for x := 0; x < VideoWidth; x++ {
			h := splitmix64(base + uint64(y*VideoWidth+x))
			o := (y*VideoWidth + x) * 3
			out[o] = byte(h)
			out[o+1] = byte(h >> 8)
			out[o+2] = byte((uint64(x)*3 + uint64(y)*5 + uint64(i)) & 0xFF)
		}
	}
	return out
}

// Frames renders all video frames.
func Frames(seed int64) [][]byte {
	out := make([][]byte, VideoFrames)
	for i := range out {
		out[i] = Frame(seed, i)
	}
	return out
}

// PixelStats summarises a set of raw frames.
type PixelStats struct {
	Mean float64 `json:"mean"`
	Peak int     `json:"peak"`
}

// ComputePixelStats aggregates mean and peak over all frames' bytes.
func ComputePixelStats(frames [][]byte) PixelStats {
	var sum uint64
	var n, peak int
	for _, f := range frames {
		for _, b := range f {
			sum += uint64(b)
			n++
			if int(b) > peak {
				peak = int(b)
			}
		}
	}
	if n == 0 {
		return PixelStats{}
	}
	return PixelStats{Mean: float64(sum) / float64(n), Peak: peak}
}

// AudioPCM renders a deterministic sine-plus-harmonic signal, s16le.
func AudioPCM(seed int64) []int16 {
	out := make([]int16, AudioSamples)
	freq := 440.0 + float64(splitmix64(uint64(seed))%80)
	for i := range out {
		t := float64(i) / AudioRate
		v := 0.6*math.Sin(2*math.Pi*freq*t) + 0.25*math.Sin(2*math.Pi*2*freq*t)
		out[i] = int16(v * 30000)
	}
	return out
}

// LoudnessPeak returns the peak absolute amplitude and its dBFS value.
func LoudnessPeak(pcm []int16) (peak int, dbfs float64) {
	for _, s := range pcm {
		v := int(s)
		if v < 0 {
			v = -v
		}
		if v > peak {
			peak = v
		}
	}
	if peak == 0 {
		return 0, -math.Inf(1)
	}
	return peak, 20 * math.Log10(float64(peak)/32768.0)
}

// PCMBytes encodes s16 samples little-endian.
func PCMBytes(pcm []int16) []byte {
	out := make([]byte, len(pcm)*2)
	for i, s := range pcm {
		binary.LittleEndian.PutUint16(out[i*2:], uint16(s))
	}
	return out
}
