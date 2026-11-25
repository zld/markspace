// Package fixture generates small deterministic synthetic media assets used
// by tests and the local demo. It never reads external media files.
package fixture

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"math"
)

const (
	FrameWidth    = 16
	FrameHeight   = 16
	FrameChannels = 3
	FrameBytes    = FrameWidth * FrameHeight * FrameChannels
	FrameCount    = 12

	VideoTimescale = 90000
	FrameDuration  = 9000 // 100 ms at 90 kHz

	AudioTimescale = 48000
	AudioBlock     = 4800 // 100 ms at 48 kHz
	AudioChannels  = 1
	// AudioAmplitude is about -8.7 dBFS, avoiding clipping and silence.
	AudioAmplitude = 12000
)

// Frame is one deterministic RGB24 video frame.
type Frame struct {
	Index int     `json:"index"`
	Hash  string  `json:"sha256"`
	RGB   []byte  `json:"-"`
	Min   int     `json:"pixel_min"`
	Max   int     `json:"pixel_max"`
	Mean  float64 `json:"pixel_mean"`
}

// AudioBlockStats contains samples and computed peak information.
type AudioBlockStats struct {
	Index    int     `json:"index"`
	Samples  int     `json:"samples"`
	Peak     int     `json:"peak"`
	PeakDBFS float64 `json:"peak_dbfs"`
	RMS      float64 `json:"rms"`
	Hash     string  `json:"sha256"`
	PCM      []int16 `json:"-"`
}

func materialNumber(id string) int {
	switch id {
	case "mat-a":
		return 1
	case "mat-b":
		return 2
	case "mat-c":
		return 3
	default:
		h := sha256.Sum256([]byte(id))
		return int(h[0])
	}
}

// GenerateFrame creates a frame. Identical materialID/localFrame pairs produce
// byte-identical pixels, which is how the repeated clip is represented.
func GenerateFrame(seed int64, materialID string, localFrame int, globalIndex int) Frame {
	rgb := make([]byte, FrameBytes)
	mn := materialNumber(materialID)
	var sum int
	min, max := 255, 0
	for y := 0; y < FrameHeight; y++ {
		for x := 0; x < FrameWidth; x++ {
			i := (y*FrameWidth + x) * 3
			r := byte(int(seed+int64(mn)*17+int64(localFrame)*29+int64(x)*7+int64(y)*11) % 256)
			g := byte(int(seed/7+int64(mn)*31+int64(localFrame)*13+int64(x)*5+int64(y)*17) % 256)
			b := byte(int(seed/13+int64(mn)*47+int64(localFrame)*7+int64(x)*19+int64(y)*3) % 256)
			rgb[i], rgb[i+1], rgb[i+2] = r, g, b
			for _, v := range []byte{r, g, b} {
				sum += int(v)
				if int(v) < min {
					min = int(v)
				}
				if int(v) > max {
					max = int(v)
				}
			}
		}
	}
	// Force stable extrema without changing dimensions.
	rgb[0], rgb[1], rgb[2] = 0, 0, 0
	rgb[FrameBytes-3], rgb[FrameBytes-2], rgb[FrameBytes-1] = 255, 255, 255
	min, max = 0, 255
	sum = 0
	for _, v := range rgb {
		sum += int(v)
	}
	h := sha256.Sum256(rgb)
	return Frame{
		Index: globalIndex,
		Hash:  hex.EncodeToString(h[:]),
		RGB:   rgb,
		Min:   min,
		Max:   max,
		Mean:  float64(sum) / float64(len(rgb)),
	}
}

// GenerateAudio produces continuous deterministic mono S16LE samples.
func GenerateAudio(seed int64) []AudioBlockStats {
	blocks := make([]AudioBlockStats, FrameCount)
	phase := float64(seed % 37)
	total := 0
	sumSq := 0.0
	peak := 0
	for b := 0; b < FrameCount; b++ {
		pcm := make([]int16, AudioBlock)
		blockPeak := 0
		blockSumSq := 0.0
		for i := 0; i < AudioBlock; i++ {
			// 440 Hz at 48 kHz.
			v := int16(float64(AudioAmplitude) * math.Sin(2*math.Pi*440*phase/float64(AudioTimescale)))
			phase++
			if phase >= float64(AudioTimescale)/440 {
				phase -= float64(AudioTimescale) / 440
			}
			pcm[i] = v
			a := int(v)
			if a < 0 {
				a = -a
			}
			if a > blockPeak {
				blockPeak = a
			}
			blockSumSq += float64(v) * float64(v)
		}
		raw := make([]byte, len(pcm)*2)
		for i, v := range pcm {
			binary.LittleEndian.PutUint16(raw[i*2:], uint16(v))
		}
		h := sha256.Sum256(raw)
		rms := math.Sqrt(blockSumSq / float64(len(pcm)))
		blocks[b] = AudioBlockStats{
			Index: b, Samples: len(pcm), Peak: blockPeak,
			PeakDBFS: PeakDBFS(blockPeak), RMS: rms,
			Hash: hex.EncodeToString(h[:]), PCM: pcm,
		}
		if blockPeak > peak {
			peak = blockPeak
		}
		sumSq += blockSumSq
		total += len(pcm)
	}
	return blocks
}

func PeakDBFS(peak int) float64 {
	if peak <= 0 {
		return math.Inf(-1)
	}
	return 20 * math.Log10(float64(peak)/32767.0)
}

// AggregateHashes returns a stable hash over ordered item hashes.
func AggregateHashes(hashes []string) string {
	h := sha256.New()
	for _, item := range hashes {
		_, _ = h.Write([]byte(item))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}
