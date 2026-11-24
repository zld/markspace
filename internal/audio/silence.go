package audio

import (
	"fmt"
	"math"

	"mediapipe/internal/model"
)

// Detector classifies fixed-duration amplitude windows. Corrupt or uncovered
// windows are Unknown; they are never reported as silent.
type Detector struct {
	WindowMs  int64
	Threshold float64
}

func NewDetector(windowMs int64, threshold float64) *Detector {
	if windowMs <= 0 {
		windowMs = 20
	}
	if threshold <= 0 {
		threshold = 300
	}
	return &Detector{WindowMs: windowMs, Threshold: threshold}
}

type windowAccumulator struct {
	startMs      int64
	endMs        int64
	sumSquares   float64
	sampleWeight float64
	coverageMs   float64
	bad          bool
	reason       string
}

// Detect analyzes one segment's ordered, expanded packets.
func (d *Detector) Detect(segment model.Segment, packets []model.Packet) []model.SilenceWindow {
	duration := segment.EndMs - segment.StartMs
	if duration <= 0 {
		return nil
	}
	count := duration / d.WindowMs // fixed windows only; grid is frame aligned
	windows := make([]*windowAccumulator, count)
	results := make([]model.SilenceWindow, 0, count)
	for i := int64(0); i < count; i++ {
		start := segment.StartMs + i*d.WindowMs
		windows[i] = &windowAccumulator{startMs: start, endMs: start + d.WindowMs}
	}

	for _, p := range packets {
		frameMs := p.FrameMs
		if frameMs <= 0 {
			frameMs = 20
		}
		pStart, pEnd := p.MediaMs, p.MediaMs+frameMs
		for _, w := range windows {
			overlapStart := maxInt64(pStart, w.startMs)
			overlapEnd := minInt64(pEnd, w.endMs)
			if overlapEnd <= overlapStart {
				continue
			}
			overlap := float64(overlapEnd - overlapStart)
			w.coverageMs += overlap
			if !p.PayloadValid {
				w.bad = true
				if p.PayloadReason != "" {
					w.reason = p.PayloadReason
				} else {
					w.reason = "invalid_audio_payload"
				}
				continue
			}
			samples, _, err := DecodePayload(p.Payload)
			if err != nil {
				w.bad = true
				w.reason = err.Error()
				continue
			}
			weight := overlap / float64(frameMs)
			var sumSq float64
			for _, s := range samples {
				v := float64(s)
				sumSq += v * v
			}
			w.sumSquares += sumSq * weight
			w.sampleWeight += float64(len(samples)) * weight
		}
	}

	for i, w := range windows {
		sw := model.SilenceWindow{
			SegmentIndex: segment.Index,
			WindowIndex:  int(i),
			StartMs:      w.startMs,
			EndMs:        w.endMs,
		}
		expected := float64(w.endMs - w.startMs)
		switch {
		case w.bad:
			sw.State = model.WindowUnknown
			sw.Reason = w.reason
		case w.coverageMs+0.001 < expected:
			sw.State = model.WindowUnknown
			sw.Reason = fmt.Sprintf("missing_coverage %.0f/%.0fms", w.coverageMs, expected)
		case w.sampleWeight <= 0:
			sw.State = model.WindowUnknown
			sw.Reason = "no_samples"
		default:
			rms := sqrtSafe(w.sumSquares / w.sampleWeight)
			sw.Amplitude = round2(rms)
			if rms < d.Threshold {
				sw.State = model.WindowSilent
			} else {
				sw.State = model.WindowVoiced
			}
		}
		results = append(results, sw)
	}
	return results
}

func sqrtSafe(x float64) float64 {
	return math.Sqrt(x)
}

func round2(v float64) float64 {
	return float64(int64(v*100+0.5)) / 100
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
