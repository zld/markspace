package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
)

// frameStat holds the offscreen-decode measurements of one frame.
type frameStat struct {
	sha  string
	min  int
	max  int
	mean float64
}

// measureFrame hashes the frame and computes pixel statistics.
func measureFrame(f []byte) frameStat {
	sum := sha256.Sum256(f)
	mn, mx, total := 255, 0, 0
	for _, b := range f {
		if int(b) < mn {
			mn = int(b)
		}
		if int(b) > mx {
			mx = int(b)
		}
		total += int(b)
	}
	return frameStat{
		sha:  hex.EncodeToString(sum[:]),
		min:  mn,
		max:  mx,
		mean: float64(total) / float64(len(f)),
	}
}

// loudnessPeakDBFS returns the sample peak in dBFS (always <= 0 for
// 16-bit PCM).
func loudnessPeakDBFS(pcm []int16) float64 {
	peak := 0
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
		return -120
	}
	return 20 * math.Log10(float64(peak)/32768.0)
}
