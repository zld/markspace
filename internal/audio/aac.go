package audio

import (
	"math"
	"time"

	"github.com/pairwise/mediad/internal/fault"
)

// AACHeaderBytes is the number of leading bytes that carry an AAC frame's
// syntax elements. A frame whose remaining bytes are all zero carries no
// spectral data, which encoders emit for digital silence.
const AACHeaderBytes = 9

// AnalyzeAAC estimates silence from raw AAC frame payloads without decoding.
//
// Only frames with an entirely zero spectral payload are reported silent;
// anything else is treated as audible. This is deliberately conservative: the
// estimator can miss soft passages, but it cannot invent silence from a
// missing or malformed payload, which is the failure that would mislead an
// editor into cutting real audio.
func AnalyzeAAC(trackID uint32, frames [][]byte, startNS, frameDurationNS int64, cfg Config) (Result, error) {
	cfg = cfg.withDefaults()
	if len(frames) == 0 {
		return Undetectable(trackID, "no AAC frames"), nil
	}
	if frameDurationNS <= 0 {
		return Undetectable(trackID, "unknown AAC frame duration"), nil
	}
	res := Result{TrackID: trackID, Detectable: true, Source: "aac-zero-payload"}
	var cur *Range
	flush := func() {
		if cur != nil && cur.End-cur.Start >= cfg.MinSilenceNS {
			res.Silence = append(res.Silence, *cur)
		}
		cur = nil
	}
	for i, payload := range frames {
		if len(payload) == 0 {
			return Undetectable(trackID, "empty AAC frame"), nil
		}
		start := startNS + int64(i)*frameDurationNS
		end := start + frameDurationNS
		silent := zeroSpectralPayload(payload)
		var rms float64
		if silent {
			rms = 0
		} else {
			rms = 1
		}
		res.Windows = append(res.Windows, Window{Start: start, End: end, RMS: rms, DBFS: dbFS(rms)})
		if silent {
			if cur == nil {
				cur = &Range{Start: start, End: end}
			} else {
				cur.End = end
			}
		} else {
			flush()
		}
	}
	flush()
	return res, nil
}

func zeroSpectralPayload(payload []byte) bool {
	if len(payload) <= AACHeaderBytes {
		return false
	}
	for _, b := range payload[AACHeaderBytes:] {
		if b != 0 {
			return false
		}
	}
	return true
}

func dbFS(rms float64) float64 {
	if rms <= 0 {
		return -math.Inf(1)
	}
	return 20 * math.Log10(rms)
}

// FrameDurationNS converts an AAC frame count at a sample rate into unified
// ticks, e.g. 1024 samples at 48 kHz.
func FrameDurationNS(samplesPerFrame, sampleRate int) (int64, error) {
	if samplesPerFrame <= 0 || sampleRate <= 0 {
		return 0, fault.Errorf("audio.aac", fault.KindAudio, "invalid AAC frame geometry %d/%d", samplesPerFrame, sampleRate)
	}
	return int64(samplesPerFrame) * int64(time.Second) / int64(sampleRate), nil
}
