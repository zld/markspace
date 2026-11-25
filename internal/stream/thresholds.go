package stream

import (
	"fmt"

	"assetcheck/internal/errmodel"
)

// Thresholds are the business acceptance limits.
type Thresholds struct {
	SampleRate         int     `json:"sample-rate"`
	Channels           int     `json:"channels"`
	MinDurationSeconds float64 `json:"min-duration-seconds"`
	MaxGapFrames       int64   `json:"max-gap-frames"`
	MaxCorruptRatio    float64 `json:"max-corrupt-ratio"`
}

// DefaultThresholds matches the documented local acceptance profile.
func DefaultThresholds() Thresholds {
	return Thresholds{
		SampleRate:         48000,
		Channels:           6,
		MinDurationSeconds: 1.0,
		MaxGapFrames:       1024,
		MaxCorruptRatio:    0.001,
	}
}

// Item is one threshold row: measured value, threshold and signed deviation.
type Item struct {
	Key        string         `json:"key"`
	Label      string         `json:"label"`
	Pass       bool           `json:"pass"`
	Comparator string         `json:"comparator"` // eq | min | max
	Actual     float64        `json:"actual"`
	Threshold  float64        `json:"threshold"`
	Deviation  float64        `json:"deviation"` // actual - threshold
	Unit       string         `json:"unit"`
	ActualText string         `json:"actual-text"`
	Code       errmodel.Code  `json:"code,omitempty"`
}

// Evaluation is the full itemized verdict.
type Evaluation struct {
	Items []Item `json:"items"`
	Pass  bool   `json:"pass"`
}

// LayoutLabel maps channel count to a human layout name.
func LayoutLabel(ch int) string {
	switch ch {
	case 1:
		return "mono"
	case 2:
		return "stereo"
	case 6:
		return "5.1"
	case 8:
		return "7.1"
	default:
		return fmt.Sprintf("%d-channel", ch)
	}
}

// Evaluate checks every business threshold.
func Evaluate(rep *Report, t Thresholds) *Evaluation {
	ev := &Evaluation{Pass: true}
	add := func(it Item) {
		it.Deviation = it.Actual - it.Threshold
		ev.Items = append(ev.Items, it)
		if !it.Pass {
			ev.Pass = false
		}
	}

	srOK := rep.SampleRate == t.SampleRate
	add(Item{
		Key: "sample-rate", Label: "Sample rate", Comparator: "eq", Unit: "Hz",
		Actual: float64(rep.SampleRate), Threshold: float64(t.SampleRate),
		ActualText: fmt.Sprintf("%d Hz", rep.SampleRate), Pass: srOK,
		Code: errmodel.ErrThresholdSampleRate,
	})

	chOK := rep.Channels == t.Channels
	add(Item{
		Key: "channels", Label: "Channel layout", Comparator: "eq", Unit: "ch",
		Actual: float64(rep.Channels), Threshold: float64(t.Channels),
		ActualText: fmt.Sprintf("%d ch (%s)", rep.Channels, LayoutLabel(rep.Channels)),
		Pass:       chOK, Code: errmodel.ErrThresholdChannels,
	})

	durSec := float64(rep.Clock.FramesToNanos(rep.TotalFrames)) / 1e9
	durOK := durSec+1e-12 >= t.MinDurationSeconds
	add(Item{
		Key: "min-duration-seconds", Label: "Duration", Comparator: "min", Unit: "s",
		Actual: durSec, Threshold: t.MinDurationSeconds,
		ActualText: fmt.Sprintf("%.3f s (%d frames)", durSec, rep.TotalFrames),
		Pass:       durOK, Code: errmodel.ErrThresholdDuration,
	})

	maxGap := rep.Interval.MaxFrames
	gapOK := maxGap <= t.MaxGapFrames
	add(Item{
		Key: "max-gap-frames", Label: "Max keyframe interval", Comparator: "max", Unit: "frames",
		Actual: float64(maxGap), Threshold: float64(t.MaxGapFrames),
		ActualText: fmt.Sprintf("%d frames (min=%d avg=%d)", maxGap, rep.Interval.MinFrames, rep.Interval.AvgFrames),
		Pass:       gapOK, Code: errmodel.ErrThresholdMaxGap,
	})

	crOK := rep.CorruptRatio <= t.MaxCorruptRatio
	add(Item{
		Key: "max-corrupt-ratio", Label: "Corrupt frame ratio", Comparator: "max", Unit: "ratio",
		Actual: rep.CorruptRatio, Threshold: t.MaxCorruptRatio,
		ActualText: fmt.Sprintf("%.6f (%d/%d frames)", rep.CorruptRatio, rep.CorruptFrames, rep.TotalFrames),
		Pass:       crOK, Code: errmodel.ErrThresholdCorrupt,
	})
	return ev
}

// Violation returns the threshold error code for the first failing item.
func (e *Evaluation) Violation() *errmodel.Error {
	for _, it := range e.Items {
		if !it.Pass {
			return errmodel.New(it.Code, "%s check failed: actual=%v threshold=%v deviation=%v",
				it.Key, it.Actual, it.Threshold, it.Deviation)
		}
	}
	return nil
}
