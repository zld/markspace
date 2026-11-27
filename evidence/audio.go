package evidence

import (
	"encoding/binary"
	"errors"
	"math"
	"strconv"
)

// AudioStats summarizes PCM s16le mono samples.
type AudioStats struct {
	SampleCount int     `json:"sample_count"`
	PeakDBFS    float64 `json:"peak_dbfs"`
	RMSDBFS     float64 `json:"rms_dbfs"`
}

// AnalyzeAudio computes peak and RMS in dBFS. Digital silence is reported as
// negative infinity so callers can render it explicitly rather than as a
// misleading finite number.
func AnalyzeAudio(pcm []byte) (AudioStats, error) {
	if len(pcm)%2 != 0 {
		return AudioStats{}, errors.New("pcm s16le payload has odd length")
	}
	count := len(pcm) / 2
	if count == 0 {
		return AudioStats{SampleCount: 0, PeakDBFS: math.Inf(-1), RMSDBFS: math.Inf(-1)}, nil
	}
	var peak int64
	var sumSq float64
	for i := 0; i < count; i++ {
		s := int16(binary.LittleEndian.Uint16(pcm[2*i:]))
		abs := int64(s)
		if abs < 0 {
			abs = -abs
		}
		if abs > peak {
			peak = abs
		}
		sumSq += float64(s) * float64(s)
	}
	if peak == 0 {
		return AudioStats{SampleCount: count, PeakDBFS: math.Inf(-1), RMSDBFS: math.Inf(-1)}, nil
	}
	peakDB := 20 * math.Log10(float64(peak) / 32768.0)
	rms := math.Sqrt(sumSq / float64(count))
	rmsDB := 20 * math.Log10(rms / 32768.0)
	return AudioStats{SampleCount: count, PeakDBFS: peakDB, RMSDBFS: rmsDB}, nil
}

// FormatDBFS renders a dBFS value, mapping -inf to the sentinel "-inf".
func FormatDBFS(v float64) string {
	if math.IsInf(v, -1) {
		return "-inf"
	}
	return strconv.FormatFloat(v, 'f', 3, 64)
}
